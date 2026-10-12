// transport_http.go Streamable HTTP 传输。
//
// 每请求独立 POST（Content-Type: application/json，Accept 兼容单 JSON 与
// SSE 流两种响应形态）；2026-07-28 要求的 Mcp-Method / Mcp-Name 头每请求必带；
// 旧版（2025-06-18 / 2025-11-25）服务器的 Mcp-Session-Id 响应头自动记录并回带。
// v1 不维持 GET 长连接：SSE 流取到目标响应即关闭，流上后续通知经 notify 上抛。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// maxLineBytes SSE 单行上限（工具结果可能很大，放宽到 16MB 防截断）
const maxLineBytes = 16 << 20

// httpTransport Streamable HTTP 传输实现
type httpTransport struct {
	url     string
	headers map[string]string // 静态鉴权头（值不得进日志）
	client  *http.Client
	notify  func(*RPCMessage)

	mu        sync.Mutex
	sessionID string // 旧版服务器会话头，缺省为空（无状态规范下不影响）
}

func newHTTPTransport(url string, headers map[string]string, notify func(*RPCMessage)) Transport {
	return &httpTransport{
		url:     url,
		headers: headers,
		client:  &http.Client{},
		notify:  notify,
	}
}

// applyCommon 每请求必带的标准头与静态鉴权头
func (t *httpTransport) applyCommon(r *http.Request, method string) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	// 2026-07-28 规范要求的标准请求头；旧版服务器对未知头自动忽略
	r.Header.Set("Mcp-Method", method)
	r.Header.Set("Mcp-Name", ClientName)
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		r.Header.Set("Mcp-Session-Id", sid)
	}
}

func (t *httpTransport) Request(ctx context.Context, req *RPCRequest) (*RPCResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化 MCP 请求失败: %w", err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造 MCP 请求失败: %w", err)
	}
	t.applyCommon(r, req.Method)

	resp, err := t.client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("MCP HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("MCP 服务器返回 HTTP %d: %s", resp.StatusCode, readLimited(resp.Body, 512))
	}

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEStream(resp.Body, req.ID, t.notify)
	}
	return readJSONResponse(resp.Body, req.ID, t.notify)
}

func (t *httpTransport) Notify(ctx context.Context, n *RPCNotification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("序列化 MCP 通知失败: %w", err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造 MCP 请求失败: %w", err)
	}
	t.applyCommon(r, n.Method)
	resp, err := t.client.Do(r)
	if err != nil {
		return fmt.Errorf("MCP HTTP 通知失败: %w", err)
	}
	defer resp.Body.Close()
	// 旧版服务器对通知回 202 Accepted；新版无状态直答也视为送达
	if resp.StatusCode >= 400 {
		return fmt.Errorf("MCP 服务器拒绝通知（HTTP %d）: %s", resp.StatusCode, readLimited(resp.Body, 512))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (t *httpTransport) Close() error {
	t.client.CloseIdleConnections()
	return nil
}

// readJSONResponse 单 JSON 响应形态
func readJSONResponse(body io.Reader, wantID int64, notify func(*RPCMessage)) (*RPCResponse, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxLineBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 MCP 响应失败: %w", err)
	}
	var msg RPCMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("MCP 响应不是合法 JSON-RPC 消息: %w", err)
	}
	if msg.ID != nil && *msg.ID == wantID {
		return &RPCResponse{ID: wantID, Result: msg.Result, Error: msg.Error}, nil
	}
	if notify != nil {
		notify(&msg)
	}
	return nil, fmt.Errorf("MCP 响应 id 不匹配（期望 %d）", wantID)
}

// readSSEStream SSE 响应形态：取首个携带目标 id 的 data 帧即返回，
// 其余帧（如 notifications/tools/list_changed）经 notify 上抛
func readSSEStream(body io.Reader, wantID int64, notify func(*RPCMessage)) (*RPCResponse, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var data strings.Builder
	found := (*RPCResponse)(nil)

	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := []byte(data.String())
		data.Reset()
		var msg RPCMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return fmt.Errorf("MCP SSE 帧不是合法 JSON-RPC 消息: %w", err)
		}
		if msg.ID != nil && *msg.ID == wantID {
			found = &RPCResponse{ID: wantID, Result: msg.Result, Error: msg.Error}
			return nil
		}
		if notify != nil {
			notify(&msg)
		}
		return nil
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return nil, err
			}
			if found != nil {
				return found, nil
			}
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		default:
			// event: / id: / 注释行：忽略（v1 不依赖 SSE 事件路由）
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 MCP SSE 流失败: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if found != nil {
		return found, nil
	}
	return nil, fmt.Errorf("MCP SSE 流结束但未收到 id=%d 的响应（2026-07-28 已移除流重放，直接整体重试即可）", wantID)
}

// readLimited 读有限字节作错误消息（防大 body 打爆日志）
func readLimited(r io.Reader, n int64) string {
	b, err := io.ReadAll(io.LimitReader(r, n))
	if err != nil || len(b) == 0 {
		return "(空响应体)"
	}
	s := strings.ToValidUTF8(string(b), "")
	if len(s) > 256 {
		s = s[:256] + "…"
	}
	return s
}
