// manager.go MCP 连接管理器：广场注册表的引擎侧投影。
//
// 每个 server 一条 serverConn（惰性拨号 → 双模握手 → 首次 tools/list → ready），
// 工具列表按 TTL 懒刷新（尊重服务器 ttlMs；list_changed 通知即时标脏）。
// turn 侧（SchemasFor）与执行侧（ExecuteTool）都以 DB 组装的 ServerDef 为准
// 按需 EnsureRegistered——定义变化自动重连，无需依赖 API 层记账。
//
// 锁序约定：Manager.mu → serverConn.mu → Manager.qmu（反向不可重入）。
package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ensureWait turn 内同步等待连接就绪的窗口：超时本轮跳过该 server（下轮重试）
const ensureWait = 3 * time.Second

// ServerDef 广场注册行的引擎投影（api / agent 层从 DB 组装）
type ServerDef struct {
	ID             int64
	Name           string
	Transport      string // "http" | "stdio"
	URL            string
	Headers        map[string]string
	Command        []string
	Env            map[string]string
	CallTimeoutSec int
	ToolAllow      []string
}

func (d ServerDef) callTimeout() time.Duration {
	if d.CallTimeoutSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(d.CallTimeoutSec) * time.Second
}

func (d ServerDef) equal(o ServerDef) bool {
	if d.ID != o.ID || d.Name != o.Name || d.Transport != o.Transport ||
		d.URL != o.URL || d.CallTimeoutSec != o.CallTimeoutSec {
		return false
	}
	if !strMapEqual(d.Headers, o.Headers) || !strMapEqual(d.Env, o.Env) {
		return false
	}
	return strSliceEqual(d.Command, o.Command) && strSliceEqual(d.ToolAllow, o.ToolAllow)
}

func strMapEqual(a, b map[string]string) bool {
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

func strSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// qTarget 消歧名反查目标
type qTarget struct {
	serverID int64
	tool     string
}

// Manager 连接注册表
type Manager struct {
	mu    sync.RWMutex
	conns map[int64]*serverConn

	qmu   sync.RWMutex
	index map[string]qTarget // 消歧函数名 → (server, 原始工具名)
}

// NewManager 构造管理器
func NewManager() *Manager {
	return &Manager{
		conns: map[int64]*serverConn{},
		index: map[string]qTarget{},
	}
}

// 连接状态机
const (
	stateIdle       = iota // 已登记未拨号
	stateConnecting        // 拨号中（单飞）
	stateReady             // 握手完成且工具列表就绪
	stateFailed            // 拨号/刷新失败，退避后重试
	stateClosed            // 已回收
)

type serverConn struct {
	m   *Manager
	def ServerDef

	mu         sync.Mutex
	state      int
	connecting chan struct{} // 关闭即本次拨号尝试结束（成功或失败）
	tr         Transport
	cli        *client
	err        error
	failures   int
	nextTry    time.Time

	tools      []Tool            // 白名单过滤后的列表
	qualified  map[string]string // 消歧名 → 原始工具名
	synced     time.Time
	ttl        time.Duration
	refreshing atomic.Bool
	dirty      atomic.Bool // list_changed 通知置位，强制下次刷新
}

// ---------- 注册与回收 ----------

// Upsert 登记/刷新 server 定义：定义未变直接复用现连接；变化则回收重连（惰性）
func (m *Manager) Upsert(def ServerDef) {
	m.mu.Lock()
	old := m.conns[def.ID]
	if old != nil {
		if old.def.equal(def) {
			m.mu.Unlock()
			return
		}
		old.close()
	}
	m.conns[def.ID] = &serverConn{m: m, def: def, ttl: DefaultTTL}
	m.mu.Unlock()
	m.deindexServer(def.ID)
}

// Remove 回收 server 连接（广场删除时调用，stdio 进程即时清理）
func (m *Manager) Remove(id int64) {
	m.mu.Lock()
	c := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	if c != nil {
		c.close()
	}
	m.deindexServer(id)
}

// EnsureRegistered 批量登记（turn 开始时以 DB 为准喂入会话启用的 server）
func (m *Manager) EnsureRegistered(defs []ServerDef) {
	for _, d := range defs {
		m.Upsert(d)
	}
}

func (c *serverConn) close() {
	c.mu.Lock()
	if c.state == stateClosed {
		c.mu.Unlock()
		return
	}
	c.state = stateClosed
	tr := c.tr
	c.tr = nil
	c.cli = nil
	c.mu.Unlock()
	if tr != nil {
		_ = tr.Close()
	}
}

func (m *Manager) connByID(id int64) *serverConn {
	m.mu.RLock()
	c := m.conns[id]
	m.mu.RUnlock()
	return c
}

// ---------- 拨号与刷新 ----------

// dial 建立传输 + 双模握手
func (c *serverConn) dial(ctx context.Context) (Transport, *client, error) {
	var tr Transport
	switch c.def.Transport {
	case "stdio":
		t, err := newStdioTransport(c.def.Command, c.def.Env, c.handleNotify)
		if err != nil {
			return nil, nil, err
		}
		tr = t
	default:
		if c.def.URL == "" {
			return nil, nil, errors.New("http 传输缺少 URL 配置")
		}
		tr = newHTTPTransport(c.def.URL, c.def.Headers, c.handleNotify)
	}
	cli := newClient(tr)
	if err := cli.dial(ctx); err != nil {
		_ = tr.Close()
		return nil, nil, err
	}
	return tr, cli, nil
}

// startConnectLocked 发起单飞拨号（调用方持有 c.mu；失败退避 1s→30s）
func (c *serverConn) startConnectLocked(ctx context.Context) {
	c.state = stateConnecting
	c.connecting = make(chan struct{})
	done := c.connecting
	cdef := c.def
	go func() {
		tr, cli, err := c.dial(ctx)
		var tools []Tool
		var ttl time.Duration
		if err == nil {
			// 握手完成即拉首次工具列表（"就绪"的定义包含工具可用）
			tools, ttl, err = cli.listTools(ctx)
			if err != nil {
				_ = tr.Close()
				tr = nil
			}
		}

		c.mu.Lock()
		close(done)
		if err != nil {
			c.state = stateFailed
			c.err = err
			c.failures++
			c.nextTry = time.Now().Add(backoff(c.failures))
			c.mu.Unlock()
			slog.Warn("MCP server 连接失败", "server", cdef.Name, "err", err.Error(),
				"retry_after", backoff(c.failures))
			return
		}
		c.tr, c.cli = tr, cli
		c.err = nil
		c.state = stateReady
		c.applyToolsLocked(tools, ttl)
		version := cli.version
		c.mu.Unlock()
		c.m.reindex(c)

		slog.Info("MCP server 就绪", "server", cdef.Name, "protocol", version, "tools", len(tools))
	}()
}

// backoff 拨号失败退避：1s 起、指数翻倍、封顶 30s
func backoff(failures int) time.Duration {
	d := time.Second
	for i := 1; i < failures && d < 30*time.Second; i++ {
		d *= 2
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// ensureReady 等待连接就绪（wait 为同步等待窗口；超时返回错误，拨号不中断）
func (c *serverConn) ensureReady(wait time.Duration) error {
	deadline := time.Now().Add(wait)
	c.mu.Lock()
	for {
		switch c.state {
		case stateReady:
			c.mu.Unlock()
			c.maybeRefresh()
			return nil
		case stateConnecting:
			ch := c.connecting
			c.mu.Unlock()
			select {
			case <-ch:
				c.mu.Lock() // 重评状态
			case <-time.After(time.Until(deadline)):
				return fmt.Errorf("连接进行中，超过 %v 未就绪", wait)
			}
		case stateFailed:
			if time.Now().Before(c.nextTry) {
				err := c.err
				c.mu.Unlock()
				if err == nil {
					err = errors.New("连接失败")
				}
				return fmt.Errorf("连接失败（退避重试中）: %w", err)
			}
			c.startConnectLocked(context.Background())
		case stateClosed:
			c.mu.Unlock()
			return errors.New("连接已回收")
		default: // idle
			c.startConnectLocked(context.Background())
		}
	}
}

// maybeRefresh TTL 过期或 list_changed 置脏时后台刷新（单飞，失败保留旧列表）
func (c *serverConn) maybeRefresh() {
	c.mu.Lock()
	stale := c.state == stateReady && (c.dirty.Load() || time.Since(c.synced) > c.ttl)
	c.mu.Unlock()
	if !stale || !c.refreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.refreshing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.refresh(ctx); err != nil {
			slog.Warn("MCP 工具列表刷新失败，沿用旧列表", "server", c.def.Name, "err", err.Error())
		}
	}()
}

// refresh 同步拉取并应用工具列表
func (c *serverConn) refresh(ctx context.Context) error {
	c.mu.Lock()
	cli := c.cli
	c.mu.Unlock()
	if cli == nil {
		return errors.New("连接未就绪")
	}
	tools, ttl, err := cli.listTools(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.applyToolsLocked(tools, ttl)
	c.mu.Unlock()
	c.m.reindex(c)
	return nil
}

// applyToolsLocked 应用工具列表（白名单过滤 + 消歧映射 + ttl 折算），调用方持锁
func (c *serverConn) applyToolsLocked(tools []Tool, ttl time.Duration) {
	tools = filterTools(tools, c.def.ToolAllow)
	c.tools = tools
	c.qualified = make(map[string]string, len(tools))
	for _, t := range tools {
		c.qualified[QualifiedName(c.def.Name, t.Name)] = t.Name
	}
	c.synced = time.Now()
	c.dirty.Store(false)
	// 服务器 ttlMs 更短则尊重服务器；缺省/超长都收敛到 DefaultTTL
	c.ttl = DefaultTTL
	if ttl > 0 && ttl < DefaultTTL {
		c.ttl = ttl
	}
}

// handleNotify 传输层上抛的通知：list_changed 标脏触发刷新
func (c *serverConn) handleNotify(msg *RPCMessage) {
	if msg.Method == "notifications/tools/list_changed" {
		c.dirty.Store(true)
		c.maybeRefresh()
		return
	}
	slog.Debug("MCP 服务器通知", "server", c.def.Name, "method", msg.Method)
}

// ---------- 消歧索引 ----------

// reindex 重建该 server 的消歧索引项。先确认自己仍是注册表中的现役连接
// （Test/更新定义会换新 conn，旧 conn 的异步完成不得写回索引）
func (m *Manager) reindex(c *serverConn) {
	m.mu.RLock()
	registered := m.conns[c.def.ID] == c
	m.mu.RUnlock()
	if !registered {
		return
	}
	m.deindexServer(c.def.ID)
	m.qmu.Lock()
	defer m.qmu.Unlock()
	for q, tool := range c.qualified {
		if _, exists := m.index[q]; exists {
			slog.Warn("MCP 工具消歧名冲突，后注册者被跳过", "qualified", q, "server", c.def.Name)
			continue
		}
		m.index[q] = qTarget{serverID: c.def.ID, tool: tool}
	}
}

// deindexServer 清除某 server 的全部索引项
func (m *Manager) deindexServer(serverID int64) {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	for q, t := range m.index {
		if t.serverID == serverID {
			delete(m.index, q)
		}
	}
}

// ---------- 对外查询与执行 ----------

// SchemasFor 返回会话启用 server 的 function schema 集合：
// 并发等连接（每 server 最多 ensureWait，墙钟合计 ≈ ensureWait），
// 未就绪的跳过本轮并告警。调用方负责先以 DB 行组装 ServerDef 喂入。
func (m *Manager) SchemasFor(defs []ServerDef) []any {
	m.EnsureRegistered(defs)
	schemas := make([][]any, len(defs))
	var wg sync.WaitGroup
	for i := range defs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			def := defs[i]
			c := m.connByID(def.ID)
			if c == nil {
				return
			}
			if err := c.ensureReady(ensureWait); err != nil {
				slog.Warn("MCP server 未就绪，本轮跳过其工具", "server", def.Name, "err", err.Error())
				return
			}
			schemas[i] = c.schemas()
		}(i)
	}
	wg.Wait()

	var out []any
	for _, s := range schemas {
		out = append(out, s...)
	}
	return out
}

func (c *serverConn) schemas() []any {
	c.mu.Lock()
	tools := make([]Tool, len(c.tools))
	copy(tools, c.tools)
	c.mu.Unlock()
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, ToolToSchema(c.def.Name, t))
	}
	return out
}

// IsTool 是否为 MCP 消歧工具名（agent 执行路由用）
func (m *Manager) IsTool(name string) bool {
	m.qmu.RLock()
	_, ok := m.index[name]
	m.qmu.RUnlock()
	return ok
}

// ExecuteTool 执行一次 MCP 工具调用并桥接为模型可读文本
func (m *Manager) ExecuteTool(ctx context.Context, qualified string, args map[string]any) Result {
	m.qmu.RLock()
	tgt, ok := m.index[qualified]
	m.qmu.RUnlock()
	if !ok {
		return Result{Text: "[MCP 调用失败] 未知 MCP 工具 `" + qualified +
			"`：服务器工具清单可能已更新，请换用其他工具或调整方案。"}
	}
	c := m.connByID(tgt.serverID)
	if c == nil {
		return Result{Text: "[MCP 调用失败] 工具所属的 MCP server 已被移除，请换用其他工具。"}
	}
	return c.execute(ctx, tgt.tool, args)
}

func (c *serverConn) execute(ctx context.Context, tool string, args map[string]any) Result {
	if err := c.ensureReady(ensureWait); err != nil {
		return Result{Text: "[MCP 调用失败] MCP server `" + c.def.Name + "` 连接未就绪: " + err.Error() +
			"。请稍后重试或换用其他工具。"}
	}
	c.mu.Lock()
	cli := c.cli
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, c.def.callTimeout())
	defer cancel()
	res, err := cli.callTool(ctx, tool, args)
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			// Unknown tool 等：清单可能已变化，触发一次刷新让下轮对齐
			if rpcErr.Code == errCodeInvalidParams || rpcErr.Code == errCodeMethodNotFound {
				c.dirty.Store(true)
				c.maybeRefresh()
			}
			return Result{Text: BridgeProtocolError(rpcErr)}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return Result{Text: fmt.Sprintf("[MCP 超时] 工具 %s 在 MCP server `%s` 上执行超过 %.0fs 未返回。"+
				"长耗时操作请换思路（或告知用户该服务当前响应慢）。", tool, c.def.Name, c.def.callTimeout().Seconds())}
		}
		// 传输层故障：连接大概率已失效，标记失败触发退避重连
		c.markFailed(err)
		return Result{Text: "[MCP 调用失败] 与 MCP server `" + c.def.Name + "` 的通信中断: " + err.Error() +
			"（已触发重连，请稍后重试）"}
	}
	return Result{Text: BridgeCallResult(res)}
}

// markFailed 传输故障回收连接并进入退避（下次 ensureReady 重拨）
func (c *serverConn) markFailed(err error) {
	c.mu.Lock()
	if c.state == stateClosed {
		c.mu.Unlock()
		return
	}
	c.state = stateFailed
	c.err = err
	c.failures++
	c.nextTry = time.Now().Add(backoff(c.failures))
	tr := c.tr
	c.tr = nil
	c.cli = nil
	c.mu.Unlock()
	if tr != nil {
		_ = tr.Close()
	}
}

// ---------- 广场支撑 ----------

// ToolInfo 广场展示用的工具概况
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Qualified   string `json:"qualified"`
}

// TestInfo 测试连接结果（api 层回写广场缓存）
type TestInfo struct {
	OK              bool       `json:"ok"`
	ProtocolVersion string     `json:"protocol_version,omitempty"`
	ServerName      string     `json:"server_name,omitempty"`
	ToolCount       int        `json:"tool_count"`
	Tools           []ToolInfo `json:"tools"`
	Err             string     `json:"error,omitempty"`
}

// Test 同步测试连接：以最新定义强制重连 + 全量握手 + 工具发现
func (m *Manager) Test(ctx context.Context, def ServerDef) TestInfo {
	m.mu.Lock()
	if old := m.conns[def.ID]; old != nil {
		old.close()
	}
	c := &serverConn{m: m, def: def, ttl: DefaultTTL}
	m.conns[def.ID] = c
	m.mu.Unlock()
	m.deindexServer(def.ID)

	if err := c.ensureReady(dialTimeout + 30*time.Second); err != nil {
		return TestInfo{OK: false, Err: err.Error()}
	}
	c.mu.Lock()
	version, serverName := c.cli.version, c.cli.serverName
	tools := make([]Tool, len(c.tools))
	copy(tools, c.tools)
	c.mu.Unlock()

	info := TestInfo{
		OK:              true,
		ProtocolVersion: version,
		ServerName:      serverName,
		ToolCount:       len(tools),
		Tools:           make([]ToolInfo, 0, len(tools)),
	}
	for _, t := range tools {
		info.Tools = append(info.Tools, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Qualified:   QualifiedName(def.Name, t.Name),
		})
	}
	return info
}

// ToolsOf 读取 server 的工具清单（未连接则现场触发连接）
func (m *Manager) ToolsOf(ctx context.Context, def ServerDef) ([]ToolInfo, error) {
	m.Upsert(def)
	c := m.connByID(def.ID)
	if c == nil {
		return nil, errors.New("server 未注册")
	}
	if err := c.ensureReady(ensureWait * 3); err != nil {
		return nil, err
	}
	c.mu.Lock()
	tools := make([]Tool, len(c.tools))
	copy(tools, c.tools)
	c.mu.Unlock()
	out := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Qualified:   QualifiedName(def.Name, t.Name),
		})
	}
	return out, nil
}

// Status 广场列表页的引擎连接状态
type Status struct {
	ServerID int64
	Ready    bool
	Err      string
}

// Statuses 批量查询连接状态
func (m *Manager) Statuses(ids []int64) map[int64]Status {
	out := make(map[int64]Status, len(ids))
	for _, id := range ids {
		c := m.connByID(id)
		if c == nil {
			out[id] = Status{ServerID: id}
			continue
		}
		c.mu.Lock()
		st := Status{ServerID: id, Ready: c.state == stateReady}
		if c.err != nil {
			st.Err = c.err.Error()
		}
		c.mu.Unlock()
		out[id] = st
	}
	return out
}
