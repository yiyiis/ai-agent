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
// 云端沙箱没有宿主绑定挂载，工作区文件经 envd /files API 传入传出：文件工具的
// 读写直达沙箱并双落宿主镜像；沙箱新建时整包推送镜像，命令执行后回拉变更
// （mtime 标记法）。沙箱被服务端 TTL 回收后句柄失效，下次使用自动重建并重推
// 镜像，数据不丢。
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	e2bMaxSyncBytes    = 256 << 20    // 单次工作区同步包上限
	e2bUploadTimeout   = 5 * time.Minute
)

// e2bDriver 每会话一个云端沙箱句柄，懒创建、显式销毁。
type e2bDriver struct {
	cfg      Config
	apiBase  string // 测试可覆写
	envdBase string // 测试可覆写
	http     *http.Client

	mu      sync.Mutex
	boxes   map[string]*e2bBox // sessionID -> 句柄
	blocked map[string]bool    // uncertain command termination quarantines the workspace

	// ensureMu 串行化云端沙箱冷启动：并发工具调用不串行会重复建沙箱（多花钱）
	ensureMu sync.Mutex
}

// registered 会话沙箱已注册时返回句柄并刷新活跃时间
func (d *e2bDriver) registered(sessionID string) *e2bBox {
	d.mu.Lock()
	defer d.mu.Unlock()
	if box := d.boxes[sessionID]; box != nil {
		box.lastUsed = time.Now()
		return box
	}
	return nil
}

// e2bBox 单个云端沙箱句柄。云端侧的变更检测靠沙箱内 marker 文件的 mtime。
type e2bBox struct {
	sandboxID   string
	accessToken string
	lastUsed    time.Time
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
		"- 启动目录 /workspace 就是会话工作区（bash 的 cwd），用户上传与工具产出的文件都在这里；\n" +
		"- 文件工具（read_file/write_file/edit_file/export_artifact）的 path 填工作区相对路径（如 report.md、demo/app.py），不要带 /workspace/ 前缀；\n" +
		"- Python 解释器命令是 `python3`；跑 Python 优先用 python_exec 工具；\n" +
		"- 沙箱内没有系统依赖安装权限（sudo/apt 不可用），缺什么需要管理员预装进模板；\n" +
		"- 命令受硬超时约束，超时会被强制结束。"
}

func (d *e2bDriver) RunBash(ctx context.Context, sessionID, command string, env []string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, timeoutSecs, []string{"/bin/bash", "-l", "-c", command}, env)
}

func (d *e2bDriver) RunPython(ctx context.Context, sessionID, scriptRel string, env []string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, timeoutSecs, []string{"/usr/bin/python3", e2bWorkspace + "/" + scriptRel}, env)
}

// run executes once against the existing sandbox. Uncertain transport failures
// are cleaned up or quarantined, never replayed as possibly duplicated writes.
func (d *e2bDriver) run(ctx context.Context, sessionID string, timeoutSecs int, argv []string, env []string) Result {
	box, err := d.ensure(ctx, sessionID)
	if err != nil {
		return Result{Err: err}
	}
	return d.runOnce(ctx, sessionID, box, timeoutSecs, argv, env)
}

func (d *e2bDriver) runOnce(ctx context.Context, sessionID string, box *e2bBox, timeoutSecs int, argv []string, env []string) Result {
	token, wrapped := supervisedRemoteCommand("/usr/bin/python3", timeoutSecs, argv)
	out, runErr := d.startProcess(ctx, box, wrapped, env)
	// No unverified RPC is required: use the existing Start protocol to execute
	// an independent cancellation helper against this same sandbox handle.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), remoteCleanupTimeout)
	cleanup, cleanupErr := d.startProcess(cleanupCtx, box, cleanupRemoteCommand("/usr/bin/python3", token), nil)
	cleanupCancel()
	if cleanupErr != nil || cleanup.ExitCode != 0 {
		d.mu.Lock()
		if d.blocked == nil {
			d.blocked = map[string]bool{}
		}
		d.blocked[sessionID] = true
		d.mu.Unlock()
		return Result{Killed: true, Err: blockedWorkspaceError()}
	}
	if runErr != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			runErr = ctx.Err()
		}
		return Result{Killed: true, Err: infraErr(runErr, "云端命令已中止且清理")}
	}

	d.mu.Lock()
	box.lastUsed = time.Now()
	d.mu.Unlock()
	killed := out.ExitCode == 124 || out.ExitCode == 137
	return Result{
		Stdout:   strings.ToValidUTF8(out.Stdout, ""),
		Stderr:   strings.ToValidUTF8(out.Stderr, ""),
		ExitCode: out.ExitCode,
		Killed:   killed,
	}
}

func (d *e2bDriver) Close(sessionID string) {
	d.mu.Lock()
	box := d.boxes[sessionID]
	delete(d.boxes, sessionID)
	d.mu.Unlock()
	if box == nil {
		d.mu.Lock()
		delete(d.blocked, sessionID)
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.killSandbox(ctx, box.sandboxID); err != nil {
		slog.Warn("sandbox(e2b) 销毁失败", "session", sessionID, "err", err)
		d.mu.Lock()
		d.boxes[sessionID] = box
		d.mu.Unlock()
	} else {
		d.mu.Lock()
		delete(d.blocked, sessionID)
		d.mu.Unlock()
	}
}

func (d *e2bDriver) forget(sessionID string) {
	d.mu.Lock()
	delete(d.boxes, sessionID)
	d.mu.Unlock()
}

// ensure 取（必要时创建）会话的云端沙箱。冷启动互斥——一轮对话的多个工具
// 调用并发执行，不串行会重复创建云沙箱。ephemeral 工作区：新沙箱从空工作区
// 开始，附件由 agent 层按轮重物化。
func (d *e2bDriver) ensure(ctx context.Context, sessionID string) (*e2bBox, error) {
	d.mu.Lock()
	blocked := d.blocked[sessionID]
	d.mu.Unlock()
	if blocked {
		return nil, blockedWorkspaceError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if box := d.registered(sessionID); box != nil {
		return box, nil
	}
	d.ensureMu.Lock()
	defer d.ensureMu.Unlock()
	if box := d.registered(sessionID); box != nil {
		return box, nil // 等锁间隙已被并发协程创建
	}

	box, err := d.createSandbox(ctx)
	if err != nil {
		return nil, infraErr(err, "创建 E2B 云端沙箱失败")
	}
	d.mu.Lock()
	d.boxes[sessionID] = box
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

// startProcess 经 envd Connect RPC 执行一条命令并等待结束；env 为 KEY=VALUE
// 形式的额外环境变量（nil 表示不注入）
func (d *e2bDriver) startProcess(ctx context.Context, box *e2bBox, argv []string, env []string) (e2bCommandResult, error) {
	var reqBody startRequest
	reqBody.Process.Cmd = argv[0]
	reqBody.Process.Args = argv[1:]
	reqBody.Process.Cwd = e2bWorkspace
	reqBody.Process.Envs = map[string]string{}
	reqBody.Stdin = false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			reqBody.Process.Envs[k] = v
		}
	}
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
	sawExit := false
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
			sawExit = ev.Event.End.Exited
		}
		return nil
	})
	if err != nil {
		return e2bCommandResult{}, err
	}
	if !sawExit {
		return e2bCommandResult{}, io.ErrUnexpectedEOF
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

// ---------- 工作区文件操作（envd /files REST） ----------

// ReadFile 从云端沙箱读取工作区文件
func (d *e2bDriver) ReadFile(ctx context.Context, sessionID, rel string) ([]byte, error) {
	relC, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	box, err := d.ensure(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	q := url.Values{"path": {e2bWorkspace + "/" + relC}, "username": {e2bUser}}
	resp, err := d.envdDo(ctx, box, http.MethodGet, "/files", q, "", nil)
	if err != nil {
		if errors.Is(err, errE2BStale) {
			return nil, ErrFileNotFound // 沙箱已被回收：ephemeral 工作区随之消失
		}
		return nil, infraErr(err, "读取沙箱文件失败")
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, e2bMaxSyncBytes+1))
}

// WriteFile 写入云端沙箱工作区
func (d *e2bDriver) WriteFile(ctx context.Context, sessionID, rel string, data []byte) error {
	relC, err := cleanRel(rel)
	if err != nil {
		return err
	}
	box, err := d.ensure(ctx, sessionID)
	if err != nil {
		return err
	}
	q := url.Values{"path": {e2bWorkspace + "/" + relC}, "username": {e2bUser}}
	resp, err := d.envdDo(ctx, box, http.MethodPost, "/files", q, "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return infraErr(err, "写入沙箱文件失败")
	}
	resp.Body.Close()
	return nil
}

// RemoveFile 删除云端沙箱工作区文件（临时脚本清理）
func (d *e2bDriver) RemoveFile(sessionID, rel string) {
	relC, err := cleanRel(rel)
	if err != nil {
		return
	}
	d.mu.Lock()
	box := d.boxes[sessionID]
	blocked := d.blocked[sessionID]
	d.mu.Unlock()
	if box == nil || blocked {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = d.startProcess(ctx, box, []string{"/bin/rm", "-f", "--", e2bWorkspace + "/" + relC}, nil)
}
