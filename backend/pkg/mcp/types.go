// types.go MCP wire 层类型（JSON-RPC 2.0 + Tools 原语子集）。
//
// 只覆盖客户端消费的最小集合：tools/list 与 tools/call 的请求/响应结构、
// 双模握手（server/discover 与 initialize）结果。规范细节见 specs/mcp/engine.md。
package mcp

import (
	"encoding/json"
	"fmt"
)

// ---------- JSON-RPC 2.0 ----------

// RPCRequest 客户端发出的请求（id 由 client 统一分配）
type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCNotification 客户端发出的通知（无 id，不期待响应）
type RPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCMessage 传输层收到的任意消息：响应携带 id 且无 method，
// 通知无 id 有 method（服务器反向请求两者皆有，v1 不支持、按通知记日志）
type RPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCResponse 与已发请求配对的响应（传输层按 id 匹配后上抛）
type RPCResponse struct {
	ID     int64
	Result json.RawMessage
	Error  *RPCError
}

// RPCError JSON-RPC 错误对象
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("JSON-RPC 错误 %d: %s", e.Code, e.Message)
}

// JSON-RPC / MCP 关键错误码
const (
	errCodeMethodNotFound   = -32601 // 方法不存在（server/discover 探测旧版服务器的信号）
	errCodeInvalidParams    = -32602 // 参数非法（含 Unknown tool）
	errCodeUnsupportedProto = -32022 // 2026-07-28: UnsupportedProtocolVersion
)

// ---------- Tools 原语 ----------

// Tool 服务器侧工具定义（tools/list 返回项，只保留 v1 消费的字段）
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ContentBlock 工具结果内容块（v1 只消费 text，其余类型按占位说明）
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallToolResult tools/call 结果
type CallToolResult struct {
	// ResultType complete | input_required（MRTR；旧版服务器缺省视为 complete）
	ResultType        string          `json:"resultType,omitempty"`
	Content           []ContentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// listToolsResult tools/list 单页结果（分页 cursor + 缓存 ttlMs 提示）
type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
	TTLms      int64  `json:"ttlMs,omitempty"`
}

// ---------- 双模握手结果 ----------

type serverIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// discoverResult server/discover 结果（2026-07-28 无状态服务器）
type discoverResult struct {
	ProtocolVersions []string        `json:"protocolVersions"`
	ServerInfo       serverIdentity  `json:"serverInfo"`
	Capabilities     json.RawMessage `json:"capabilities,omitempty"`
}

// initializeResult initialize 握手结果（2025-06-18 / 2025-11-25 存量服务器）
type initializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	ServerInfo      serverIdentity  `json:"serverInfo"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
}
