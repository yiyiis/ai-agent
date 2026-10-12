// transport.go MCP 传输层抽象：承载 JSON-RPC 消息的双向通道。
//
// 实现负责按 id 配对请求与响应；途中到达的通知（如 notifications/tools/
// list_changed）经构造时注入的 notify 回调上抛。两个实现：
//   - Streamable HTTP（transport_http.go）：远端服务器，每请求独立 POST
//   - stdio（transport_stdio.go）：本地子进程，换行分隔 JSON-RPC
package mcp

import "context"

// Transport MCP 传输层接口
type Transport interface {
	// Request 发送请求并阻塞取回按 id 配对的响应。
	// 返回的 RPCResponse.Error 非 nil 表示 JSON-RPC 协议层错误（非传输失败）。
	Request(ctx context.Context, req *RPCRequest) (*RPCResponse, error)
	// Notify 发送通知（initialized 握手等），不期待响应
	Notify(ctx context.Context, n *RPCNotification) error
	// Close 关闭传输（杀子进程 / 释放连接）
	Close() error
}
