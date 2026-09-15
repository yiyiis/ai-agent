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
	"strconv"
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
	// execKillGrace 内层 timeout 命令的 KILL 宽限（秒）：TERM 后仍不退则强杀
	execKillGrace = 5
	// reapInterval 生命周期巡检间隔
	reapInterval = 30 * time.Second
)

const (
	defaultDockerImage = "python:3.12-slim" // 需含 bash 与 python3；换镜像须保住这两个契约
	defaultIdleTimeout = 15 * time.Minute   // 空闲多久后销毁容器
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

	mu   sync.Mutex
	ctrs map[string]*ctrState // sessionID -> 容器状态
	stop chan struct{}        // 关闭巡检协程（仅测试用）
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
		"- 启动目录 /workspace 就是会话工作区，与文件工具看到的是同一份目录，用户上传与工具产出的文件都在这里；\n" +
		"- Python 解释器命令是 `python3`；跑 Python 优先用 python_exec 工具；\n" +
		"- 容器内不可安装系统依赖（apt 等不可用），缺什么需要管理员预装进镜像；\n" +
		"- 命令受硬超时约束，超时会被强制结束。"
}

// RunBash / RunPython 统一走 run：确保容器在跑 → 建 exec → 读流拿输出 → 查退出码
func (d *dockerDriver) RunBash(ctx context.Context, sessionID, hostDir, command string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, hostDir, timeoutSecs, []string{"bash", "-c", command})
}

func (d *dockerDriver) RunPython(ctx context.Context, sessionID, hostDir, scriptRel string, timeoutSecs int) Result {
	return d.run(ctx, sessionID, hostDir, timeoutSecs, []string{"python3", containerPath + "/" + scriptRel})
}

// run 在会话容器内执行 argv。外层包一层 `timeout -k` 做命令级硬超时：
// Engine API 没有"取消 exec"的接口，靠镜像内的 timeout 命令兜底最可靠
//（GNU coreutils 与 busybox 均支持该参数序）。
func (d *dockerDriver) run(ctx context.Context, sessionID, hostDir string, timeoutSecs int, argv []string) Result {
	id, err := d.ensure(ctx, sessionID, hostDir)
	if err != nil {
		return Result{Err: err}
	}
	wrapped := append([]string{"timeout", "-k", strconv.Itoa(execKillGrace), strconv.Itoa(timeoutSecs)}, argv...)

	eid, err := d.client.createExec(ctx, id, wrapped)
	if err != nil {
		// 容器可能刚被巡检销毁（空闲/超期），按需拉起一次后重试
		d.forget(sessionID)
		if id, err = d.ensure(ctx, sessionID, hostDir); err != nil {
			return Result{Err: err}
		}
		if eid, err = d.client.createExec(ctx, id, wrapped); err != nil {
			return Result{Err: infraErr(err, "创建执行会话失败")}
		}
	}

	var stdout, stderr bytes.Buffer
	if err := d.client.startExecStream(ctx, eid, &stdout, &stderr); err != nil {
		return Result{Err: infraErr(err, "读取执行输出失败")}
	}
	exitCode, err := d.client.execExitCode(ctx, eid)
	if err != nil {
		return Result{Err: infraErr(err, "获取退出码失败")}
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
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.client.removeContainer(ctx, st.id); err != nil {
		slog.Warn("sandbox 销毁容器失败", "session", sessionID, "err", err)
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
	if st := d.ctrs[sessionID]; st != nil {
		st.lastUsed = time.Now()
	}
	d.mu.Unlock()
}

// ensure 按需拉起会话容器：查名 → 不在则建、没跑则启。hostDir 为工作区宿主绝对路径，
// 以绑定挂载进 /workspace；额外的只读挂载来自配置 Mounts。环境注入隔离标记 + 配置项。
func (d *dockerDriver) ensure(ctx context.Context, sessionID, hostDir string) (string, error) {
	d.mu.Lock()
	if st := d.ctrs[sessionID]; st != nil {
		st.lastUsed = time.Now()
		d.mu.Unlock()
		return st.id, nil
	}
	d.mu.Unlock()

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
		id, err = d.client.createContainer(ctx, name, d.containerSpec(sessionID, hostDir))
		if errors.Is(err, errDockerImageMissing) {
			// 镜像不在本地：自动拉取后重试一次（首次部署无需手动 docker pull）
			slog.Info("sandbox 镜像不存在，开始自动拉取", "image", d.cfg.Image)
			pullCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			if pullErr := d.client.pullImage(pullCtx, d.cfg.Image); pullErr != nil {
				cancel()
				return "", infraErr(pullErr, "拉取沙箱镜像失败")
			}
			cancel()
			id, err = d.client.createContainer(ctx, name, d.containerSpec(sessionID, hostDir))
		}
		if err != nil {
			return "", infraErr(err, "拉起执行容器失败")
		}
		if err := d.client.startContainer(ctx, id); err != nil {
			return "", infraErr(err, "启动执行容器失败")
		}
		slog.Info("sandbox 容器已创建", "session", sessionID, "container", name, "image", d.cfg.Image)
	}

	now := time.Now()
	d.mu.Lock()
	// 双检：拉起的间隙可能有并发协程已注册同会话容器，以先注册者为准
	if existing := d.ctrs[sessionID]; existing != nil {
		id = existing.id
	} else {
		d.ctrs[sessionID] = &ctrState{id: id, created: now, lastUsed: now}
	}
	d.mu.Unlock()
	return id, nil
}

// containerSpec 组装容器创建参数
func (d *dockerDriver) containerSpec(sessionID, hostDir string) containerCreateReq {
	binds := []string{d.bindHostPath(hostDir) + ":" + containerPath}
	binds = append(binds, d.cfg.Mounts...) // 形如 host:container[:ro]，只读挂载由调用方声明

	env := append([]string{"AI_AGENT_SESSION=" + sessionID}, d.cfg.Env...)
	return containerCreateReq{
		Image:      d.cfg.Image,
		Entrypoint: []string{},
		Cmd:        []string{"sleep", "infinity"},
		Env:        env,
		WorkingDir: containerPath,
		Labels:     map[string]string{labelManaged: "1", "ai-agent-session": sessionID},
		HostConfig: hostConfig{
			Binds:       binds,
			NetworkMode: d.cfg.NetworkMode,
			Memory:      d.cfg.MemoryMB << 20,           // 0 = 不限制
			NanoCPUs:    int64(d.cfg.CPUS * 1e9),        // 0 = 不限制
			PidsLimit:   pidsLimitOrNil(d.cfg.PidsLimit), // nil = 不限制
		},
	}
}

// bindHostPath 工作区的 bind 源路径：后端直跑宿主时 hostDir 即宿主路径；
// 后端自己跑在容器里时，hostDir 是容器内视角（如 /app/workspace/<sid>），
// bind 挂载必须翻译成宿主真实路径——由 HostWorkspaceMap（容器前缀=宿主前缀）提供映射。
func (d *dockerDriver) bindHostPath(hostDir string) string {
	m := d.cfg.HostWorkspaceMap
	if m != "" {
		if parts := strings.SplitN(m, "=", 2); len(parts) == 2 && strings.HasPrefix(hostDir, parts[0]) {
			return toBindPath(parts[1] + strings.TrimPrefix(hostDir, parts[0]))
		}
	}
	return toBindPath(hostDir)
}

func pidsLimitOrNil(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

// reapLoop 生命周期巡检：空闲超过 IdleTimeout 或存活超过 MaxLifetime 的容器强制销毁。
// MaxLifetime 对"活跃"容器同样生效——常驻容器到期销毁，下次使用自动重拉，杜绝泄漏累积。
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
	type victim struct{ sessionID, id string }
	d.mu.Lock()
	now := time.Now()
	var victims []victim
	for sid, st := range d.ctrs {
		idle := now.Sub(st.lastUsed) > d.idleTimeout
		expired := now.Sub(st.created) > d.maxLifetime
		if idle || expired {
			victims = append(victims, victim{sid, st.id})
			delete(d.ctrs, sid)
		}
	}
	d.mu.Unlock()

	for _, v := range victims {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := d.client.removeContainer(ctx, v.id); err != nil {
			slog.Warn("sandbox 巡检销毁容器失败", "session", v.sessionID, "err", err)
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

// toBindPath 宿主路径转 Docker 绑定挂载格式：统一正斜杠（Windows 盘符路径 D:\a\b → D:/a/b）
func toBindPath(hostDir string) string { return strings.ReplaceAll(hostDir, "\\", "/") }

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
	base      string // 形如 http://docker（socket 场景的虚拟主机名）
	http      *http.Client
	dialPipe  func(ctx context.Context, path string) (net.Conn, error) // npipe 拨号（测试注入用）
	pipePath  string   // 非空时经命名管道拨号
	socketPath string // 非空时经 unix socket 拨号
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
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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

func (c *dockerClient) createContainer(ctx context.Context, name string, spec containerCreateReq) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": []string{name}}, spec)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusNotFound && strings.Contains(string(body), "No such image") {
			return "", errors.Join(errDockerImageMissing,
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

func (c *dockerClient) createExec(ctx context.Context, containerID string, cmd []string) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+containerID+"/exec", nil, map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
		"WorkingDir":   containerPath,
	})
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
