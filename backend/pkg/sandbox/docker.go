package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"backend/pkg/errors"
)

// 镜像与容器约定：工作区固定挂载到容器内 /workspace；容器以 sleep infinity 常驻，
// 每次工具调用通过 exec API 进容器执行，避免"每命令一容器"的拉起开销。
const (
	containerPath = "/workspace"
	// labelManaged 打在本系统创建的全部容器上：重启清扫与人工排查都靠它
	labelManaged = "ai-agent-sandbox"
	// reapInterval 生命周期巡检间隔
	reapInterval = 30 * time.Second
)

const (
	defaultDockerImage = "python:3.12-slim" // 需含 bash 与 python3；换镜像须保住这两个契约
	defaultIdleTimeout = time.Hour          // 空闲多久后销毁容器（ephemeral 工作区下适当放宽，减少工作现场丢失窗口）
	defaultMaxLifetime = 12 * time.Hour     // 容器最长存活：常驻容器也需要硬顶，防句柄泄漏越积越多
	defaultNetworkMode = "bridge"
)

// dockerDriver 每会话独立容器的执行沙箱。
//
// 生命周期治理：首次使用时按需拉起（create+start），此后 exec 复用；
// 后台巡检协程对空闲超时或超最长存活的容器强制销毁；会话删除时显式 Close；
// 进程启动时清扫遗留的带标签孤儿容器（单实例部署假设与 TurnRegistry 一致）。
type dockerDriver struct {
	cfg    Config
	client *dockerClient

	// idleTimeout / maxLifetime 由配置的秒数换算（<=0 用默认值）
	idleTimeout time.Duration
	maxLifetime time.Duration

	mu      sync.Mutex
	ctrs    map[string]*ctrState // sessionID -> 容器状态
	blocked map[string]bool      // command termination could not be confirmed
	stop    chan struct{}        // 关闭巡检协程（仅测试用）

	// ensureMu 串行化容器冷启动（inspect→create→start）：一轮对话的多个工具
	// 调用并发执行，不串行会各自 create 同名容器、后到者撞 409（线上真实发生过）
	ensureMu sync.Mutex
}

type ctrState struct {
	id       string
	created  time.Time
	lastUsed time.Time
}

func newDockerDriver(cfg Config) *dockerDriver {
	if cfg.Image == "" {
		cfg.Image = defaultDockerImage
	}
	if cfg.NetworkMode == "" {
		cfg.NetworkMode = defaultNetworkMode
	}
	d := &dockerDriver{
		cfg:         cfg,
		client:      newDockerClient(cfg.DockerURL),
		idleTimeout: secondsOrDefault(cfg.IdleTimeoutSec, defaultIdleTimeout),
		maxLifetime: secondsOrDefault(cfg.MaxLifetimeSec, defaultMaxLifetime),
		ctrs:        map[string]*ctrState{},
		stop:        make(chan struct{}),
	}
	d.sweepOrphans(context.Background())
	go d.reapLoop()
	engine := cfg.DockerURL
	if engine == "" {
		engine = "平台默认"
	}
	fmt.Printf("[INFO] 执行沙箱驱动: docker (Engine: %s, 镜像: %s)\n", engine, cfg.Image)
	return d
}

// secondsOrDefault 配置秒数换算 duration；非正值回退默认
func secondsOrDefault(sec int, def time.Duration) time.Duration {
	if sec <= 0 {
		return def
	}
	return time.Duration(sec) * time.Second
}

func (d *dockerDriver) Name() string { return "docker" }

func (d *dockerDriver) EnvNote() string {
	return "- 运行环境是隔离的 Linux 容器（镜像 " + d.cfg.Image + "），不是宿主机；\n" +
		"- 启动目录 /workspace 就是会话工作区（bash 的 cwd），用户上传与工具产出的文件都在这里；\n" +
		"- 文件工具（read_file/write_file/edit_file/export_artifact）的 path 填工作区相对路径（如 report.md、demo/app.py），不要带 /workspace/ 前缀；\n" +
		"- Python 解释器命令是 `python3`；跑 Python 优先用 python_exec 工具；\n" +
		"- 容器内不可安装系统依赖（apt 等不可用），缺什么需要管理员预装进镜像；\n" +
		"- 命令受硬超时约束，超时会被强制结束。"
}

// RunBash / RunPython 统一走 run：确保容器在跑 → 建 exec → 读流拿输出 → 查退出码
func (d *dockerDriver) RunBash(ctx context.Context, sessionID, command string, env []string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, timeoutSecs, []string{"bash", "-c", command}, env)
}

func (d *dockerDriver) RunPython(ctx context.Context, sessionID, scriptRel string, env []string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, timeoutSecs, []string{"python3", containerPath + "/" + scriptRel}, env)
}

// run supervises one process group and confirms cleanup before returning. Engine
// stream cancellation alone does not cancel the corresponding remote exec.
func (d *dockerDriver) run(ctx context.Context, sessionID string, timeoutSecs int, argv []string, env []string) Result {
	id, err := d.ensure(ctx, sessionID)
	if err != nil {
		return Result{Err: err}
	}
	token, wrapped := supervisedRemoteCommand("python3", timeoutSecs, argv)

	eid, err := d.client.createExec(ctx, id, wrapped, env)
	if err != nil {
		// 容器可能刚被巡检销毁（空闲/超期），按需拉起一次后重试
		d.forget(sessionID)
		if id, err = d.ensure(ctx, sessionID); err != nil {
			return Result{Err: err}
		}
		if eid, err = d.client.createExec(ctx, id, wrapped, env); err != nil {
			return Result{Err: infraErr(err, "创建执行会话失败")}
		}
	}

	var stdout, stderr bytes.Buffer
	streamErr := d.client.startExecStream(ctx, eid, &stdout, &stderr)
	// Losing the HTTP stream does not stop an Engine exec. Cancellation and even
	// successful commands must acknowledge group cleanup before the caller can
	// release its shared-workspace lock.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), remoteCleanupTimeout)
	cleanupCode, cleanupErr := d.execQuiet(cleanupCtx, id, cleanupRemoteCommand("python3", token))
	cleanupCancel()
	if cleanupErr != nil || cleanupCode != 0 {
		d.mu.Lock()
		if d.blocked == nil {
			d.blocked = map[string]bool{}
		}
		d.blocked[sessionID] = true
		d.mu.Unlock()
		return Result{Killed: true, Err: blockedWorkspaceError()}
	}
	if streamErr != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			streamErr = ctx.Err()
		}
		return Result{Killed: true, Err: infraErr(streamErr, "执行已中止且远端命令已清理")}
	}
	exitCode, err := d.client.execExitCode(ctx, eid)
	if err != nil {
		return Result{Err: infraErr(err, "获取退出码失败（远端命令已清理）")}
	}
	d.touch(sessionID)
	// 124=timeout 超时退出；137=SIGKILL（-k 宽限后强杀）
	killed := exitCode == 124 || exitCode == 137
	return Result{
		Stdout:   strings.ToValidUTF8(stdout.String(), ""),
		Stderr:   strings.ToValidUTF8(stderr.String(), ""),
		ExitCode: exitCode,
		Killed:   killed,
	}
}

func (d *dockerDriver) Close(sessionID string) {
	d.mu.Lock()
	st := d.ctrs[sessionID]
	delete(d.ctrs, sessionID)
	d.mu.Unlock()
	if st == nil {
		d.mu.Lock()
		delete(d.blocked, sessionID)
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.client.removeContainer(ctx, st.id); err != nil {
		slog.Warn("sandbox 销毁容器失败", "session", sessionID, "err", err)
		// Keep the handle and quarantine for a later explicit retry. Forgetting
		// it would let a second Close clear the block without removing anything.
		d.mu.Lock()
		d.ctrs[sessionID] = st
		d.mu.Unlock()
	} else {
		d.mu.Lock()
		delete(d.blocked, sessionID)
		d.mu.Unlock()
	}
}

// forget 让会话的容器记录失效（下次使用重新拉起）
func (d *dockerDriver) forget(sessionID string) {
	d.mu.Lock()
	delete(d.ctrs, sessionID)
	d.mu.Unlock()
}

// touch 刷新会话容器的最后使用时间（空闲销毁的计时依据）
func (d *dockerDriver) touch(sessionID string) {
	d.mu.Lock()
	if st := d.ctrs[sessionID]; st != nil && !d.blocked[sessionID] {
		st.lastUsed = time.Now()
	}
	d.mu.Unlock()
}

// registered 会话容器已注册时返回其 id 并刷新活跃时间
func (d *dockerDriver) registered(sessionID string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st := d.ctrs[sessionID]; st != nil {
		st.lastUsed = time.Now()
		return st.id, true
	}
	return "", false
}

// ensure 按需拉起会话容器：已注册直接复用；否则（互斥地）查名 → 不在则建、
// 没跑则启。冷启动全程持 ensureMu，防止并发工具调用重复 create 同名容器。
// ephemeral 工作区：全新容器从空工作区开始，附件由 agent 层按轮重物化。
func (d *dockerDriver) ensure(ctx context.Context, sessionID string) (string, error) {
	d.mu.Lock()
	blocked := d.blocked[sessionID]
	d.mu.Unlock()
	if blocked {
		return "", blockedWorkspaceError()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id, ok := d.registered(sessionID); ok {
		return id, nil
	}
	d.ensureMu.Lock()
	defer d.ensureMu.Unlock()
	if id, ok := d.registered(sessionID); ok {
		return id, nil // 等锁间隙已被并发协程拉起
	}

	name := containerName(sessionID)
	id, running, err := d.client.inspectContainer(ctx, name)
	if err != nil {
		return "", infraErr(err, "连接执行容器失败")
	}
	if id != "" && !running {
		if err := d.client.startContainer(ctx, id); err != nil {
			return "", infraErr(err, "启动执行容器失败")
		}
		slog.Info("sandbox 容器已重启", "session", sessionID, "container", name)
	}
	if id == "" {
		id, err = d.client.createContainer(ctx, name, d.containerSpec(sessionID))
		if errors.Is(err, errDockerImageMissing) {
			// 镜像不在本地：自动拉取后重试一次（首次部署无需手动 docker pull）
			slog.Info("sandbox 镜像不存在，开始自动拉取", "image", d.cfg.Image)
			pullCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			if pullErr := d.client.pullImage(pullCtx, d.cfg.Image); pullErr != nil {
				cancel()
				return "", infraErr(pullErr, "拉取沙箱镜像失败")
			}
			cancel()
			id, err = d.client.createContainer(ctx, name, d.containerSpec(sessionID))
		}
		switch {
		case errors.Is(err, errDockerNameConflict):
			// 同名容器已存在（上次进程建完未注册即退出等）：收养现成容器
			id, running, err = d.client.inspectContainer(ctx, name)
			if err == nil && id != "" && !running {
				err = d.client.startContainer(ctx, id)
			}
			if err != nil || id == "" {
				return "", infraErr(errors.Join(err, errDockerNameConflict), "拉起执行容器失败")
			}
			slog.Info("sandbox 同名容器已存在，收养复用", "session", sessionID, "container", name)
		case err != nil:
			return "", infraErr(err, "拉起执行容器失败")
		default:
			if err := d.client.startContainer(ctx, id); err != nil {
				return "", infraErr(err, "启动执行容器失败")
			}
			slog.Info("sandbox 容器已创建", "session", sessionID, "container", name, "image", d.cfg.Image)
		}
	}

	now := time.Now()
	d.mu.Lock()
	d.ctrs[sessionID] = &ctrState{id: id, created: now, lastUsed: now}
	d.mu.Unlock()
	return id, nil
}

// containerSpec 组装容器创建参数。工作区不做 bind 挂载——文件经 /archive API
// 传入传出（与 e2b 的传输模型统一），容器因此完全自包含，也支持远程 Docker daemon。
func (d *dockerDriver) containerSpec(sessionID string) containerCreateReq {
	env := append([]string{"AI_AGENT_SESSION=" + sessionID}, d.cfg.Env...)
	return containerCreateReq{
		Image:      d.cfg.Image,
		Entrypoint: []string{},
		Cmd:        []string{"sleep", "infinity"},
		Env:        env,
		WorkingDir: containerPath,
		Labels:     map[string]string{labelManaged: "1", "ai-agent-session": sessionID},
		HostConfig: hostConfig{
			Binds:       d.cfg.Mounts, // 额外只读挂载（资产目录等），工作区不在此列
			NetworkMode: d.cfg.NetworkMode,
			Memory:      d.cfg.MemoryMB << 20,            // 0 = 不限制
			NanoCPUs:    int64(d.cfg.CPUS * 1e9),         // 0 = 不限制
			PidsLimit:   pidsLimitOrNil(d.cfg.PidsLimit), // nil = 不限制
		},
	}
}

// ---------- 工作区文件操作（Engine /archive API，与 e2b 的传输模型统一） ----------

// ErrFileNotFound 工作区文件不存在（工具层据此生成可自纠正提示）
var ErrFileNotFound = errors.NewMsg("工作区文件不存在")

func (d *dockerDriver) ReadFile(ctx context.Context, sessionID, rel string) ([]byte, error) {
	relC, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	id, err := d.ensure(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	tarBytes, err := d.client.getArchive(ctx, id, containerPath+"/"+relC)
	if err != nil {
		if errors.Is(err, errDockerArchiveNotFound) {
			return nil, ErrFileNotFound
		}
		return nil, infraErr(err, "读取沙箱文件失败")
	}
	data, err := readTarSingle(tarBytes)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (d *dockerDriver) WriteFile(ctx context.Context, sessionID, rel string, data []byte) error {
	relC, err := cleanRel(rel)
	if err != nil {
		return err
	}
	id, err := d.ensure(ctx, sessionID)
	if err != nil {
		return err
	}
	tarball, err := tarSingleFile(relC, data)
	if err != nil {
		return err
	}
	// 解包到 /workspace：PUT archive 的 path 是目标目录，tar 内相对路径即工作区相对路径
	if err := d.client.putArchive(ctx, id, containerPath, tarball); err != nil {
		return infraErr(err, "写入沙箱文件失败")
	}
	return nil
}

func (d *dockerDriver) RemoveFile(sessionID, rel string) {
	relC, err := cleanRel(rel)
	if err != nil {
		return
	}
	d.mu.Lock()
	st := d.ctrs[sessionID]
	blocked := d.blocked[sessionID]
	d.mu.Unlock()
	if st == nil || blocked {
		return // 容器未拉起：ephemeral 工作区里也没有该文件
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = d.execQuiet(ctx, st.id, []string{"/bin/rm", "-f", "--", containerPath + "/" + relC})
}

// execQuiet 静默执行（只要退出码，输出丢弃）——清理类内部命令用
func (d *dockerDriver) execQuiet(ctx context.Context, id string, argv []string) (int, error) {
	eid, err := d.client.createExec(ctx, id, argv, nil)
	if err != nil {
		return 0, err
	}
	if err := d.client.startExecStream(ctx, eid, io.Discard, io.Discard); err != nil {
		return 0, err
	}
	return d.client.execExitCode(ctx, eid)
}

func pidsLimitOrNil(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

// reapLoop 生命周期巡检：空闲超过 IdleTimeout 或存活超过 MaxLifetime 的容器强制销毁。
// 根执行树持有租约时延后回收，避免多 Agent 执行途中共享文件突然消失。
func (d *dockerDriver) reapLoop() {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.reapOnce()
		}
	}
}

func (d *dockerDriver) reapOnce() {
	// Do not let a new lease/ensure adopt a container between retirement and
	// removal. Existing users hold workspace leases and are skipped below.
	d.ensureMu.Lock()
	defer d.ensureMu.Unlock()
	type victim struct {
		sessionID string
		state     *ctrState
	}
	d.mu.Lock()
	now := time.Now()
	var victims []victim
	for sid, st := range d.ctrs {
		if workspaceLeased(sid) || d.blocked[sid] {
			continue
		}
		idle := now.Sub(st.lastUsed) > d.idleTimeout
		expired := now.Sub(st.created) > d.maxLifetime
		if idle || expired {
			victims = append(victims, victim{sid, st})
			delete(d.ctrs, sid)
		}
	}
	d.mu.Unlock()

	for _, v := range victims {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := d.client.removeContainer(ctx, v.state.id); err != nil {
			slog.Warn("sandbox 巡检销毁容器失败", "session", v.sessionID, "err", err)
			d.mu.Lock()
			d.ctrs[v.sessionID] = v.state
			d.mu.Unlock()
		} else {
			slog.Info("sandbox 容器已销毁（空闲/超期）", "session", v.sessionID)
		}
		cancel()
	}
}

// sweepOrphans 启动清扫：删除遗留的带本系统标签的容器（上次进程退出没来得及销毁的）
func (d *dockerDriver) sweepOrphans(ctx context.Context) {
	ids, err := d.client.listLabeled(ctx)
	if err != nil {
		slog.Warn("sandbox 孤儿容器清扫失败（Docker 未就绪？稍后首次使用时会再暴露）", "err", err)
		return
	}
	for _, id := range ids {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := d.client.removeContainer(cctx, id); err != nil {
			slog.Warn("sandbox 清扫孤儿容器失败", "id", id, "err", err)
		}
		cancel()
	}
	if len(ids) > 0 {
		slog.Info("sandbox 已清扫遗留容器", "count", len(ids))
	}
}

// containerName 容器名与 sessionID 一一映射（sessionID 为 UUID，天然安全）
func containerName(sessionID string) string { return "ai-agent-sbx-" + sessionID }

// ---------- Docker Engine API 客户端（手写，不引官方 SDK） ----------

type containerCreateReq struct {
	Image      string            `json:"Image"`
	Entrypoint []string          `json:"Entrypoint"`
	Cmd        []string          `json:"Cmd"`
	Env        []string          `json:"Env,omitempty"`
	WorkingDir string            `json:"WorkingDir"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	Binds       []string `json:"Binds,omitempty"`
	NetworkMode string   `json:"NetworkMode,omitempty"`
	AutoRemove  bool     `json:"AutoRemove"`
	Memory      int64    `json:"Memory,omitempty"`
	NanoCPUs    int64    `json:"NanoCpus,omitempty"`
	PidsLimit   *int64   `json:"PidsLimit,omitempty"`
}

// dockerClient Docker Engine API 的极薄 HTTP 封装，只覆盖沙箱用到的接口面：
// containers inspect/create/start/list/remove 与 exec create/start/inspect。
type dockerClient struct {
	base       string // 形如 http://docker（socket 场景的虚拟主机名）
	http       *http.Client
	dialPipe   func(ctx context.Context, path string) (net.Conn, error) // npipe 拨号（测试注入用）
	pipePath   string                                                   // 非空时经命名管道拨号
	socketPath string                                                   // 非空时经 unix socket 拨号
}

func newDockerClient(host string) *dockerClient {
	if host == "" {
		if runtime.GOOS == "windows" {
			host = "npipe:////./pipe/docker_engine"
		} else {
			host = "unix:///var/run/docker.sock"
		}
	}
	c := &dockerClient{dialPipe: dialPipe}
	u, err := url.Parse(host)
	if err == nil {
		switch u.Scheme {
		case "unix":
			c.socketPath = u.Path
			c.base = "http://docker"
		case "npipe":
			c.pipePath = strings.TrimPrefix(host, "npipe://")
			c.base = "http://docker"
			return c
		case "tcp", "http", "https":
			c.base = strings.TrimRight(host, "/")
			return c
		}
	}
	// 兜底当 TCP 地址处理
	if c.base == "" {
		c.base = strings.TrimRight(host, "/")
	}
	return c
}

func (c *dockerClient) transport() *http.Transport {
	t := &http.Transport{}
	if c.socketPath != "" {
		sp := c.socketPath
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sp)
		}
	}
	if c.pipePath != "" {
		pp, dial := c.pipePath, c.dialPipe
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx, pp)
		}
	}
	return t
}

func (c *dockerClient) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	var rdr io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case []byte: // 原始字节流（tar 上传），不做 JSON 包装
		rdr = bytes.NewReader(b)
		contentType = "application/x-tar"
	default:
		j, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(j)
		contentType = "application/json"
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Host", "docker") // socket 虚拟主机名场景下保持 Host 一致
	return (&http.Client{Transport: c.transport()}).Do(req)
}

// inspectContainer 返回 (id, running, err)；不存在时 id 为空、err 为 nil
func (c *dockerClient) inspectContainer(ctx context.Context, name string) (string, bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+name+"/json", nil, nil)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode >= 400 {
		return "", false, fmt.Errorf("inspect %s: HTTP %d", name, resp.StatusCode)
	}
	var out struct {
		ID    string `json:"Id"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, err
	}
	return out.ID, out.State.Running, nil
}

// errDockerImageMissing create 404（No such image）的判别哨兵
var errDockerImageMissing = errors.NewMsg("docker image missing")

// errDockerNameConflict create 409（同名容器已存在）的判别哨兵
var errDockerNameConflict = errors.NewMsg("docker container name conflict")

func (c *dockerClient) createContainer(ctx context.Context, name string, spec containerCreateReq) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": []string{name}}, spec)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		switch {
		case resp.StatusCode == http.StatusNotFound && strings.Contains(string(body), "No such image"):
			return "", errors.Join(errDockerImageMissing,
				errors.New(fmt.Sprintf("create %s: HTTP %d %s", name, resp.StatusCode, strings.TrimSpace(string(body)))))
		case resp.StatusCode == http.StatusConflict:
			return "", errors.Join(errDockerNameConflict,
				errors.New(fmt.Sprintf("create %s: HTTP %d %s", name, resp.StatusCode, strings.TrimSpace(string(body)))))
		}
		return "", fmt.Errorf("create %s: HTTP %d %s", name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// errDockerArchiveNotFound getArchive 404 的判别哨兵（文件不存在）
var errDockerArchiveNotFound = errors.NewMsg("docker archive not found")

// putArchive 把 tar 流解压到容器内指定目录（docker cp 的上传半边）
func (c *dockerClient) putArchive(ctx context.Context, containerID, dir string, tarball []byte) error {
	resp, err := c.do(ctx, http.MethodPut, "/containers/"+containerID+"/archive",
		url.Values{"path": []string{dir}}, tarball)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("put archive %s: HTTP %d %s", dir, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// getArchive 读取容器内路径的 tar 流（docker cp 的下载半边）
func (c *dockerClient) getArchive(ctx context.Context, containerID, path string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+containerID+"/archive",
		url.Values{"path": []string{path}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errDockerArchiveNotFound
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("get archive %s: HTTP %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, e2bMaxSyncBytes+1))
}

// pullImage 拉取镜像（POST /images/create 为流式进度接口，读到 EOF 即完成）
func (c *dockerClient) pullImage(ctx context.Context, ref string) error {
	resp, err := c.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": []string{ref}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pull %s: HTTP %d %s", ref, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *dockerClient) startContainer(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 304 = 已在运行，视为成功
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("start %s: HTTP %d", id, resp.StatusCode)
	}
	return nil
}

func (c *dockerClient) removeContainer(ctx context.Context, idOrName string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+idOrName,
		url.Values{"force": []string{"1"}, "v": []string{"1"}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("remove %s: HTTP %d", idOrName, resp.StatusCode)
	}
	return nil
}

// listLabeled 列出本系统管理的全部容器（含已停止的）
func (c *dockerClient) listLabeled(ctx context.Context) ([]string, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {labelManaged + "=1"}})
	resp, err := c.do(ctx, http.MethodGet, "/containers/json",
		url.Values{"all": []string{"1"}, "filters": []string{string(filters)}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("list: HTTP %d", resp.StatusCode)
	}
	var out []struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out))
	for _, ctr := range out {
		ids = append(ids, ctr.ID)
	}
	return ids, nil
}

func (c *dockerClient) createExec(ctx context.Context, containerID string, cmd []string, env []string) (string, error) {
	body := map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
		"WorkingDir":   containerPath,
	}
	if len(env) > 0 {
		body["Env"] = env
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+containerID+"/exec", nil, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("exec create: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// startExecStream 启动 exec 并读取复用流输出（非 TTY 模式为 8 字节帧头 + 载荷的
// docker raw stream：帧头首字节 1=stdout 2=stderr，后 4 字节为大端长度）
func (c *dockerClient) startExecStream(ctx context.Context, execID string, stdout, stderr io.Writer) error {
	resp, err := c.do(ctx, http.MethodPost, "/exec/"+execID+"/start", nil, map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("exec start: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return demuxStream(resp.Body, stdout, stderr)
}

func (c *dockerClient) execExitCode(ctx context.Context, execID string) (int, error) {
	resp, err := c.do(ctx, http.MethodGet, "/exec/"+execID+"/json", nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("exec inspect: HTTP %d", resp.StatusCode)
	}
	var out struct {
		ExitCode int  `json:"ExitCode"`
		Running  bool `json:"Running"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	if out.Running {
		return 0, errors.NewMsg("exec 仍在运行，无法获取退出码")
	}
	return out.ExitCode, nil
}

// demuxStream 解析 docker raw stream 复用帧
func demuxStream(r io.Reader, stdout, stderr io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		size := binary.BigEndian.Uint32(header[4:])
		if size == 0 {
			continue
		}
		if _, err := io.CopyN(pickWriter(header[0], stdout, stderr), r, int64(size)); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
	}
}

func pickWriter(streamByte byte, stdout, stderr io.Writer) io.Writer {
	if streamByte == 2 {
		return stderr
	}
	return stdout
}
