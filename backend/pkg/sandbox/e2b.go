package sandbox

// e2b.go E2B 云端沙箱驱动（阶段四第三驱动）。
//
// 官方只提供 Python/JS SDK，执行协议（envd 的 Connect RPC + 文件 REST）由本文件
// 依据官方 SDK 源码直接实现，不引第三方依赖：
//
//	生命周期   POST/DELETE https://api.e2b.app/sandboxes（X-API-Key 鉴权）
//	           创建响应含 sandbox_id 与 envd_access_token
//	命令执行   POST https://sandbox.e2b.app/process.Process/Start（Connect 流式 JSON）
//	           路由头 E2b-Sandbox-Id / E2b-Sandbox-Port: 49983 / X-Access-Token / Basic user:
//	           argv 形如 /bin/bash -l -c <cmd>，流式返回 base64 的 stdout/stderr 与 exitCode
//	文件传输   GET/POST https://sandbox.e2b.app/files?path=&username=user（上传支持 gzip）
//
// 云端沙箱没有宿主绑定挂载，工作区一致性由驱动维护：每次执行前把宿主工作区的
// 变更打包上传解压（tar.gz），执行后把沙箱内新增/变更的文件拉回宿主（mtime 标记法）。
// 沙箱被服务端 TTL 回收后句柄失效，下次执行自动重建并重新同步，数据不丢。
import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"backend/pkg/errors"
)

const (
	e2bDefaultAPIBase  = "https://api.e2b.app"
	e2bDefaultEnvdBase = "https://sandbox.e2b.app"
	e2bDefaultTemplate = "base"
	e2bDefaultTTL      = 30 * time.Minute
	e2bEnvdPort        = 49983
	e2bWorkspace       = "/workspace" // 工作区在云端沙箱内的路径
	e2bUser            = "user"       // 沙箱默认用户（envd Basic 鉴权与文件参数都用它）
	e2bSyncMarker      = ".sbx_synced"
	e2bMaxSyncBytes    = 256 << 20 // 单次工作区同步包上限
	e2bExecKillGrace   = 5         // 内层 timeout 命令的 KILL 宽限（秒）
	e2bUploadTimeout   = 5 * time.Minute
)

// e2bDriver 每会话一个云端沙箱句柄，懒创建、显式销毁。
type e2bDriver struct {
	cfg      Config
	apiBase  string // 测试可覆写
	envdBase string // 测试可覆写
	http     *http.Client

	mu   sync.Mutex
	boxes map[string]*e2bBox // sessionID -> 句柄
}

// e2bBox 单个云端沙箱句柄。synced 记录最近一次上传后的宿主快照（rel -> size:mtime），
// 用于跳过无变更时的上传；云端侧的变更检测靠沙箱内 marker 文件的 mtime。
type e2bBox struct {
	sandboxID    string
	accessToken  string
	lastUsed     time.Time
	synced       map[string]string
}

func newE2BDriver(cfg Config) *e2bDriver {
	if cfg.E2BTemplate == "" {
		cfg.E2BTemplate = e2bDefaultTemplate
	}
	if cfg.E2BTimeoutSec <= 0 {
		cfg.E2BTimeoutSec = int(e2bDefaultTTL / time.Second)
	}
	d := &e2bDriver{
		cfg:      cfg,
		apiBase:  e2bDefaultAPIBase,
		envdBase: e2bDefaultEnvdBase,
		http:     &http.Client{Timeout: 0}, // 超时逐请求用 ctx 控制（命令流可能很长）
		boxes:    map[string]*e2bBox{},
	}
	fmt.Printf("[INFO] 执行沙箱驱动: e2b (模板: %s, TTL: %ds)\n", cfg.E2BTemplate, cfg.E2BTimeoutSec)
	return d
}

func (d *e2bDriver) Name() string { return "e2b" }

func (d *e2bDriver) EnvNote() string {
	return "- 运行环境是 E2B 云端隔离的 Linux 沙箱（模板 " + d.cfg.E2BTemplate + "），与宿主机完全隔离；\n" +
		"- 启动目录 /workspace 就是会话工作区（系统自动与本地双向同步），用户上传与工具产出的文件都在这里；\n" +
		"- Python 解释器命令是 `python3`；跑 Python 优先用 python_exec 工具；\n" +
		"- 沙箱内没有系统依赖安装权限（sudo/apt 不可用），缺什么需要管理员预装进模板；\n" +
		"- 命令受硬超时约束，超时会被强制结束。"
}

func (d *e2bDriver) RunBash(ctx context.Context, sessionID, hostDir, command string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, hostDir, timeoutSecs, []string{"/bin/bash", "-l", "-c", command})
}

func (d *e2bDriver) RunPython(ctx context.Context, sessionID, hostDir, scriptRel string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, hostDir, timeoutSecs, []string{"/usr/bin/python3", e2bWorkspace + "/" + scriptRel})
}

// run 完整执行一轮：确保沙箱 → 同步上行 → 执行命令 → 同步下行。
// 沙箱句柄失效（TTL 回收等）时重建一次并整体重试。
func (d *e2bDriver) run(ctx context.Context, sessionID, hostDir string, timeoutSecs int, argv []string) Result {
	box, err := d.ensure(ctx, sessionID, hostDir)
	if err != nil {
		return Result{Err: err}
	}
	res, stale := d.runOnce(ctx, box, sessionID, hostDir, timeoutSecs, argv)
	if stale {
		slog.Info("sandbox(e2b) 句柄失效，重建后重试", "session", sessionID)
		d.forget(sessionID)
		if box, err = d.ensure(ctx, sessionID, hostDir); err != nil {
			return Result{Err: err}
		}
		res, _ = d.runOnce(ctx, box, sessionID, hostDir, timeoutSecs, argv)
	}
	return res
}

// runOnce 单次执行；stale 表示沙箱句柄已失效（应重建重试）
func (d *e2bDriver) runOnce(ctx context.Context, box *e2bBox, sessionID, hostDir string, timeoutSecs int, argv []string) (Result, bool) {
	if err := d.syncUp(ctx, box, hostDir); err != nil {
		if errors.Is(err, errE2BStale) {
			return Result{Err: nil}, true
		}
		return Result{Err: infraErr(err, "同步工作区到云端沙箱失败")}, false
	}

	// 外层包 timeout 做命令级硬超时（base 模板为 Ubuntu，含 coreutils）
	wrapped := append([]string{"/usr/bin/timeout", "-k", strconv.Itoa(e2bExecKillGrace), strconv.Itoa(timeoutSecs)}, argv...)
	out, err := d.startProcess(ctx, box, wrapped)
	if err != nil {
		if errors.Is(err, errE2BStale) {
			return Result{}, true
		}
		return Result{Err: infraErr(err, "在云端沙箱执行命令失败")}, false
	}

	// 命令本身失败也要回拉文件（半成品产物不丢），但 infra 失败则跳过
	if err := d.syncDown(ctx, box, hostDir); err != nil {
		slog.Warn("sandbox(e2b) 回拉工作区失败", "session", sessionID, "err", err)
	}
	box.lastUsed = time.Now()
	killed := out.ExitCode == 124 || out.ExitCode == 137
	return Result{
		Stdout:   strings.ToValidUTF8(out.Stdout, ""),
		Stderr:   strings.ToValidUTF8(out.Stderr, ""),
		ExitCode: out.ExitCode,
		Killed:   killed,
	}, false
}

func (d *e2bDriver) Close(sessionID string) {
	d.mu.Lock()
	box := d.boxes[sessionID]
	delete(d.boxes, sessionID)
	d.mu.Unlock()
	if box == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.killSandbox(ctx, box.sandboxID); err != nil {
		slog.Warn("sandbox(e2b) 销毁失败", "session", sessionID, "err", err)
	}
}

func (d *e2bDriver) forget(sessionID string) {
	d.mu.Lock()
	delete(d.boxes, sessionID)
	d.mu.Unlock()
}

// ensure 取（必要时创建）会话的云端沙箱
func (d *e2bDriver) ensure(ctx context.Context, sessionID, hostDir string) (*e2bBox, error) {
	d.mu.Lock()
	if box := d.boxes[sessionID]; box != nil {
		box.lastUsed = time.Now()
		d.mu.Unlock()
		return box, nil
	}
	d.mu.Unlock()

	box, err := d.createSandbox(ctx)
	if err != nil {
		return nil, infraErr(err, "创建 E2B 云端沙箱失败")
	}
	box.synced = nil // 尚未同步过，首次执行会全量上传
	d.mu.Lock()
	if existing := d.boxes[sessionID]; existing != nil {
		box = existing
	} else {
		d.boxes[sessionID] = box
	}
	d.mu.Unlock()
	slog.Info("sandbox(e2b) 已创建", "session", sessionID, "sandbox", box.sandboxID)
	return box, nil
}

// ---------- 生命周期 REST ----------

type e2bCreateResponse struct {
	SandboxID       string `json:"sandboxID"`
	ClientID        string `json:"clientID"`
	EnvdVersion     string `json:"envdVersion"`
	EnvdAccessToken string `json:"envdAccessToken"`
	Domain          string `json:"domain"`
}

func (d *e2bDriver) createSandbox(ctx context.Context) (*e2bBox, error) {
	// 线上 REST 契约为驼峰字段（templateID / timeout），与官方 SDK 的 openapi 生成模型一致
	body, _ := json.Marshal(map[string]any{
		"templateID": d.cfg.E2BTemplate,
		"timeout":    d.cfg.E2BTimeoutSec,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.apiBase+"/sandboxes", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", d.cfg.E2BKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("create sandbox: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out e2bCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.SandboxID == "" {
		return nil, errors.NewMsg("创建响应缺少 sandbox_id")
	}
	return &e2bBox{sandboxID: out.SandboxID, accessToken: out.EnvdAccessToken, lastUsed: time.Now()}, nil
}

func (d *e2bDriver) killSandbox(ctx context.Context, sandboxID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, d.apiBase+"/sandboxes/"+sandboxID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", d.cfg.E2BKey)
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("kill sandbox: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---------- envd 命令执行（Connect 流式 JSON） ----------

// errE2BStale 沙箱已被服务端回收（401/404/502），应重建
var errE2BStale = errors.NewMsg("e2b sandbox stale")

type e2bCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// startRequest 对应 proto: process.StartRequest{process: ProcessConfig{cmd,args,envs,cwd}}
type startRequest struct {
	Process struct {
		Cmd  string            `json:"cmd"`
		Args []string          `json:"args,omitempty"`
		Envs map[string]string `json:"envs,omitempty"`
		Cwd  string            `json:"cwd,omitempty"`
	} `json:"process"`
	Stdin bool `json:"stdin"`
}

// startEvent proto JSON: {event: oneof{start{pid}, data{stdout|stderr base64}, end{exitCode,exited,status}, keepalive{}}}。
// 注意 oneof 成员在 proto3 JSON 里直挂父级（data.stdout 而非 data.output.stdout）；
// 数值字段为零值时省略（exitCode=0 不出现）。
type startEvent struct {
	Event struct {
		Start *struct {
			PID uint32 `json:"pid"`
		} `json:"start"`
		Data *struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		} `json:"data"`
		End *struct {
			ExitCode int    `json:"exitCode"`
			Exited   bool   `json:"exited"`
			Status   string `json:"status"`
		} `json:"end"`
	} `json:"event"`
}

func (d *e2bDriver) envdDo(ctx context.Context, box *e2bBox, method, path string, query url.Values, contentType string, body io.Reader) (*http.Response, error) {
	u := d.envdBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("E2b-Sandbox-Id", box.sandboxID)
	req.Header.Set("E2b-Sandbox-Port", strconv.Itoa(e2bEnvdPort))
	if box.accessToken != "" {
		req.Header.Set("X-Access-Token", box.accessToken)
	}
	// envd Basic 鉴权：user:<空密码>，与官方 SDK 的 authentication_header 一致
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(e2bUser+":")))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusBadGateway {
		resp.Body.Close()
		return nil, errE2BStale
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("envd %s: HTTP %d %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// startProcess 经 envd Connect RPC 执行一条命令并等待结束
func (d *e2bDriver) startProcess(ctx context.Context, box *e2bBox, argv []string) (e2bCommandResult, error) {
	var reqBody startRequest
	reqBody.Process.Cmd = argv[0]
	reqBody.Process.Args = argv[1:]
	reqBody.Process.Cwd = e2bWorkspace
	reqBody.Stdin = false
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return e2bCommandResult{}, err
	}

	// Connect 流式协议：请求体为单条信封帧，响应为信封帧序列
	framed := connectFrame(nil, payload)
	resp, err := d.envdDo(ctx, box, http.MethodPost, "/process.Process/Start", nil,
		"application/connect+json", bytes.NewReader(framed))
	if err != nil {
		return e2bCommandResult{}, err
	}
	defer resp.Body.Close()

	var out e2bCommandResult
	var stdout, stderr bytes.Buffer
	err = readConnectStream(resp.Body, func(frame []byte) error {
		var ev startEvent
		if err := json.Unmarshal(frame, &ev); err != nil {
			return nil // 忽略无法解析的帧（keepalive 等空载荷）
		}
		if ev.Event.Data != nil {
			if s := ev.Event.Data.Stdout; s != "" {
				if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
					stdout.Write(raw)
				}
			}
			if s := ev.Event.Data.Stderr; s != "" {
				if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
					stderr.Write(raw)
				}
			}
		}
		if ev.Event.End != nil {
			out.ExitCode = ev.Event.End.ExitCode
		}
		return nil
	})
	if err != nil {
		return e2bCommandResult{}, err
	}
	out.Stdout, out.Stderr = stdout.String(), stderr.String()
	return out, nil
}

// connectFrame 构造一条 Connect 协议信封帧（flags 可为 0 或 0x02 end-stream）
func connectFrame(errJSON []byte, payload []byte) []byte {
	var flags byte
	if errJSON != nil {
		flags = 0x02
		payload = errJSON
	}
	buf := make([]byte, 5, 5+len(payload))
	buf[0] = flags
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	return append(buf, payload...)
}

// readConnectStream 读取 Connect 流式响应的信封帧序列；end-stream 帧携带
// {"error":...} 时返回错误。ctx 取消由 http 层直接断流体现。
func readConnectStream(r io.Reader, onMessage func(frame []byte) error) error {
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		size := binary.BigEndian.Uint32(header[1:5])
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		if header[0]&0x02 != 0 { // end of stream
			var trailer struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if len(payload) > 0 && json.Unmarshal(payload, &trailer) == nil && trailer.Error != nil {
				return fmt.Errorf("connect error %s: %s", trailer.Error.Code, trailer.Error.Message)
			}
			return nil
		}
		if len(payload) > 0 {
			if err := onMessage(payload); err != nil {
				return err
			}
		}
	}
}

// ---------- 工作区双向同步 ----------

// fileSnapshot 宿主工作区快照：rel -> "size:mtimeUnixNano"
func fileSnapshot(root string) (map[string]string, error) {
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil // 与快照竞态的文件直接跳过
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isSyncSkipped(rel) {
			return nil
		}
		snap[rel] = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// isSyncSkipped 同步时跳过的文件：marker 与同步用 tar 包。
// 注意 .pyexec_* 临时脚本不能排除——e2b 驱动需要把它上传到云端才能执行
//（本地脚本由 tools 层在执行后自行清理）。
func isSyncSkipped(rel string) bool {
	base := filepath.Base(rel)
	return base == e2bSyncMarker || base == ".sync.tgz" || base == "sbx_out.tgz"
}

// tarWorkspace 把宿主工作区打包为 tar.gz 字节流
func tarWorkspace(root string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || isSyncSkipped(filepath.ToSlash(rel)) {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		_, copyErr := io.Copy(tw, f)
		f.Close()
		return copyErr
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// untarTo 把 tar.gz 解包到目录（覆盖已存在文件，跳过同步排除项）
func untarTo(dir string, data []byte) error {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if isSyncSkipped(filepath.ToSlash(name)) {
			continue
		}
		// 防路径穿越：解析后的目标必须落在 dir 内
		target := filepath.Join(dir, filepath.FromSlash(name))
		rel, err := filepath.Rel(dir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeTarFile(target, tr); err != nil {
				return err
			}
		}
	}
}

func writeTarFile(target string, r io.Reader) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// syncUp 宿主工作区 → 云端沙箱。快照无变化时零开销跳过。
func (d *e2bDriver) syncUp(ctx context.Context, box *e2bBox, hostDir string) error {
	snap, err := fileSnapshot(hostDir)
	if err != nil {
		return err
	}
	if box.synced != nil && sameSnapshot(box.synced, snap) {
		return nil
	}
	data, err := tarWorkspace(hostDir)
	if err != nil {
		return err
	}
	if len(data) > e2bMaxSyncBytes {
		return errors.NewMsg(fmt.Sprintf("工作区同步包超过 %dMB 上限", e2bMaxSyncBytes>>20))
	}
	// 上传 tar 包（gzip 直传）→ 沙箱内解压 → 打同步 marker（下行变更检测的基准）
	upCtx, cancel := context.WithTimeout(ctx, e2bUploadTimeout)
	defer cancel()
	q := url.Values{"path": {e2bWorkspace + "/.sync.tgz"}, "username": {e2bUser}}
	resp, err := d.envdDo(upCtx, box, http.MethodPost, "/files", q, "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp.Body.Close()

	extract := "cd " + e2bWorkspace + " && tar xzf .sync.tgz && rm -f .sync.tgz && touch " + e2bSyncMarker
	if _, err := d.startProcess(ctx, box, []string{"/bin/bash", "-l", "-c", extract}); err != nil {
		return err
	}
	box.synced = snap
	return nil
}

// syncDown 云端沙箱 → 宿主工作区：以 marker 的 mtime 为基准拉回新增/变更文件。
func (d *e2bDriver) syncDown(ctx context.Context, box *e2bBox, hostDir string) error {
	list := "cd " + e2bWorkspace + " && find . -type f ! -name " + e2bSyncMarker +
		" -newer " + e2bSyncMarker + " -print0 | tar --null -czf /tmp/sbx_out.tgz -T - ; ec=$?; " +
		"rm -f " + e2bSyncMarker + " ; exit $ec"
	out, err := d.startProcess(ctx, box, []string{"/bin/bash", "-l", "-c", list})
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("collect changed files: exit %d %s", out.ExitCode, out.Stderr)
	}

	dlCtx, cancel := context.WithTimeout(ctx, e2bUploadTimeout)
	defer cancel()
	q := url.Values{"path": {"/tmp/sbx_out.tgz"}, "username": {e2bUser}}
	resp, err := d.envdDo(dlCtx, box, http.MethodGet, "/files", q, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, e2bMaxSyncBytes+1))
	if err != nil {
		return err
	}
	_, _ = d.startProcess(ctx, box, []string{"/bin/bash", "-l", "-c", "rm -f /tmp/sbx_out.tgz"})
	if len(data) == 0 || len(data) > e2bMaxSyncBytes {
		return nil
	}
	if err := untarTo(hostDir, data); err != nil {
		return err
	}
	// 宿主快照刷新为拉回后的真实状态（下次 syncUp 据此判变更）
	if snap, err := fileSnapshot(hostDir); err == nil {
		box.synced = snap
	}
	return nil
}

// sameSnapshot 两份快照是否一致
func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
