// client.go MCP 协议客户端（双模）。
//
// 连接策略（specs/mcp/engine.md §3.1）：先发 server/discover 探测 2026-07-28
// 无状态服务器（成功即 stateless 模式，每请求 _meta 携带协议版本）；方法不
// 存在或探测失败则回退旧版 initialize 握手（stateless 之前的事，legacy 模式，
// HTTP 的 Mcp-Session-Id 由传输层记录回带）。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

const (
	// ClientName 客户端标识（_meta clientInfo 与 Mcp-Name 头）
	ClientName = "ai-agent"
	// LatestVersion 客户端支持的最新协议版本（2026-07-28 无状态规范）
	LatestVersion = "2026-07-28"
	// LegacyVersion 旧版 initialize 握手申报的协议版本
	LegacyVersion = "2025-06-18"

	discoverTimeout = 3 * time.Second  // server/discover 探测窗口
	dialTimeout     = 10 * time.Second // 整体握手预算
	maxListPages    = 100              // tools/list 分页保护上限
)

// client 协议客户端：一个 client 绑定一条 Transport 与一次协商结果
type client struct {
	tr         Transport
	mode       int
	version    string // 协商出的协议版本
	serverName string // serverInfo.name（展示用，不作消歧依据）
	nextID     atomic.Int64
}

const (
	modeUnknown = iota
	modeStateless
	modeLegacy
)

func newClient(tr Transport) *client {
	return &client{tr: tr}
}

// dial 双模握手：discover 探测 → initialize 回退
func (c *client) dial(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	// 1) server/discover：新版无状态服务器直接可用
	dctx, dcancel := context.WithTimeout(ctx, discoverTimeout)
	defer dcancel()
	res, derr := c.request(dctx, "server/discover", map[string]any{})
	if derr == nil && res.Error == nil {
		var dr discoverResult
		_ = json.Unmarshal(res.Result, &dr)
		c.mode = modeStateless
		c.version = pickVersion(dr.ProtocolVersions)
		c.serverName = dr.ServerInfo.Name
		return nil
	}
	// derr 非 nil（网络/超时）或 res.Error 非 nil（如 -32601 方法不存在）：
	// 前者保守视为连接不可用，但仍给 initialize 一次机会（部分旧服务器对
	// 未知方法的响应路径异常）；后者就是旧版服务器，正常走回退。

	// 2) initialize：存量 2025-06-18 / 2025-11-25 服务器
	res, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": LegacyVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": ClientName, "version": "1.0.0"},
	})
	if err != nil {
		if derr != nil {
			return fmt.Errorf("MCP 握手失败（discover: %v / initialize: %w）", derr, err)
		}
		return fmt.Errorf("MCP initialize 握手失败: %w", err)
	}
	if res.Error != nil {
		return fmt.Errorf("MCP initialize 被拒绝: %s", res.Error.Error())
	}
	var ir initializeResult
	if err := json.Unmarshal(res.Result, &ir); err != nil {
		return fmt.Errorf("MCP initialize 结果解析失败: %w", err)
	}
	c.mode = modeLegacy
	c.version = ir.ProtocolVersion
	if c.version == "" {
		c.version = LegacyVersion
	}
	c.serverName = ir.ServerInfo.Name
	// initialized 通知（尽力而为；个别服务器不等待）
	_ = c.tr.Notify(ctx, &RPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
	return nil
}

// pickVersion 从服务器声明列表挑协议版本：优先客户端最新版，否则取首个
func pickVersion(versions []string) string {
	for _, v := range versions {
		if v == LatestVersion {
			return v
		}
	}
	if len(versions) > 0 {
		return versions[0]
	}
	return LatestVersion
}

// request 发请求。stateless 模式（含探测期）每请求 _meta 携带版本与客户端
// 身份；legacy 模式不带（个别旧服务器对未知 _meta 字段严格）
func (c *client) request(ctx context.Context, method string, params map[string]any) (*RPCResponse, error) {
	if params == nil {
		params = map[string]any{}
	}
	if c.mode != modeLegacy {
		version := c.version
		if version == "" {
			version = LatestVersion
		}
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    version,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": ClientName, "version": "1.0.0"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	req := &RPCRequest{JSONRPC: "2.0", ID: c.nextID.Add(1), Method: method, Params: params}
	return c.tr.Request(ctx, req)
}

// listTools 分页拉全量工具列表，返回（工具, 服务器建议的缓存 ttl）
func (c *client) listTools(ctx context.Context) ([]Tool, time.Duration, error) {
	var all []Tool
	ttl := time.Duration(0)
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, 0, fmt.Errorf("tools/list 失败: %w", err)
		}
		if res.Error != nil {
			// 服务器未实现 tools 能力：空列表不算故障
			if res.Error.Code == errCodeMethodNotFound {
				return nil, 0, nil
			}
			return nil, 0, fmt.Errorf("tools/list 被拒绝: %s", res.Error.Error())
		}
		var lr listToolsResult
		if err := json.Unmarshal(res.Result, &lr); err != nil {
			return nil, 0, fmt.Errorf("tools/list 结果解析失败: %w", err)
		}
		all = append(all, lr.Tools...)
		if lr.TTLms > 0 {
			if d := time.Duration(lr.TTLms) * time.Millisecond; d > ttl {
				ttl = d
			}
		}
		if lr.NextCursor == "" {
			break
		}
		cursor = lr.NextCursor
	}
	return all, ttl, nil
}

// callTool 调用远端工具。返回的 error 为 *RPCError 时是协议层错误（unknown
// tool 等），其余为传输层错误（调用方据此决定是否重连）
func (c *client) callTool(ctx context.Context, name string, args map[string]any) (*CallToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, fmt.Errorf("tools/call 失败: %w", err)
	}
	if res.Error != nil {
		return nil, res.Error
	}
	var out CallToolResult
	if len(res.Result) > 0 {
		if err := json.Unmarshal(res.Result, &out); err != nil {
			return nil, fmt.Errorf("tools/call 结果解析失败: %w", err)
		}
	}
	return &out, nil
}
