// mcp_test.go 引擎单测：httptest 双模式假服务器（新版无状态 / 旧版 initialize）
// 覆盖握手探测回退、Session-Id 回带、SSE 响应、分页、桥接文案与超时；
// stdio 假进程（测试二进制自执行）覆盖行分隔编解码与 legacy 握手。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	"testing"
	"time"
)

// ---------- HTTP 假服务器 ----------

// fakeMCPServer 可配置的 Streamable HTTP 假服务器。
// mode: "stateless"（2026-07-28）| "legacy"（initialize 握手 + Session-Id）
type fakeMCPServer struct {
	mode       string
	mu         sync.Mutex
	sessionID  string
	listCalls  int
	callLog    []string
	sseList    bool // tools/list 用 SSE 响应形态
	callsSleep time.Duration
	// callBehavior 工具名 → 结果定制（nil 走默认 echo）
	callBehavior map[string]func(args map[string]any) (*CallToolResult, *RPCError)
}

func newFake(t *testing.T, mode string) *fakeMCPServer {
	t.Helper()
	return &fakeMCPServer{mode: mode, callBehavior: map[string]func(map[string]any) (*CallToolResult, *RPCError){}}
}

type fakeRPC struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (f *fakeMCPServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fakeRPC
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Mcp-Method") == "" || r.Header.Get("Mcp-Name") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// legacy：握手后的请求必须带 Session-Id
		if f.mode == "legacy" && req.Method != "server/discover" && req.Method != "initialize" {
			if r.Header.Get("Mcp-Session-Id") != f.sessionID {
				writeJSONRPC(w, req.ID, nil, &RPCError{Code: -32001, Message: "missing session"})
				return
			}
		}

		switch req.Method {
		case "server/discover":
			if f.mode == "legacy" {
				writeJSONRPC(w, req.ID, nil, &RPCError{Code: -32601, Message: "method not found"})
				return
			}
			writeJSONRPC(w, req.ID, map[string]any{
				"protocolVersions": []string{LatestVersion},
				"serverInfo":       map[string]any{"name": "fake-server", "version": "1.0"},
			}, nil)
		case "initialize":
			f.mu.Lock()
			f.sessionID = "sess-123"
			f.mu.Unlock()
			w.Header().Set("Mcp-Session-Id", "sess-123")
			writeJSONRPC(w, req.ID, map[string]any{
				"protocolVersion": LegacyVersion,
				"serverInfo":      map[string]any{"name": "legacy-fake", "version": "0.9"},
			}, nil)
		case "tools/list":
			f.mu.Lock()
			f.listCalls++
			calls := f.listCalls
			f.mu.Unlock()
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(req.Params, &params)
			result := map[string]any{
				"tools": []map[string]any{{
					"name":        "echo",
					"description": "回显消息",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
						"msg": map[string]any{"type": "string"},
					}},
				}},
			}
			if calls == 1 && params.Cursor == "" {
				result["nextCursor"] = "page2"
			}
			if params.Cursor == "page2" {
				result["tools"] = []map[string]any{{
					"name":        "boom.bad",
					"description": "含非法字符的工具名",
					"inputSchema": map[string]any{"type": "object"},
				}}
			}
			// 自定义行为的工具也要广播，否则客户端索引不到
			for name := range f.callBehavior {
				result["tools"] = append(result["tools"].([]map[string]any), map[string]any{
					"name": name, "description": name, "inputSchema": map[string]any{"type": "object"},
				})
			}
			if f.sseList {
				writeSSE(w, req.ID, result)
				return
			}
			writeJSONRPC(w, req.ID, result, nil)
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			f.mu.Lock()
			f.callLog = append(f.callLog, params.Name)
			f.mu.Unlock()
			if f.callsSleep > 0 {
				time.Sleep(f.callsSleep)
			}
			if custom, ok := f.callBehavior[params.Name]; ok {
				res, rpcErr := custom(params.Arguments)
				writeJSONRPC(w, req.ID, res, rpcErr)
				return
			}
			msg, _ := params.Arguments["msg"].(string)
			writeJSONRPC(w, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echo: " + msg}},
			}, nil)
		default:
			writeJSONRPC(w, req.ID, nil, &RPCError{Code: -32601, Message: "method not found: " + req.Method})
		}
	}
}

func writeJSONRPC(w http.ResponseWriter, id *int64, result any, rpcErr *RPCError) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// writeSSE 以 SSE 流形态回响应（前面先塞一条无关通知，验证解析器能跳过）
func writeSSE(w http.ResponseWriter, id *int64, result any) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
	flusher.Flush()
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
	flusher.Flush()
}

func httpDef(id int64, name, url string) ServerDef {
	return ServerDef{ID: id, Name: name, Transport: "http", URL: url, CallTimeoutSec: 5}
}

// ---------- stateless（2026-07-28）全链路 ----------

func TestHTTPStatelessFlow(t *testing.T) {
	fake := newFake(t, "stateless")
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := NewManager()
	def := httpDef(1, "fake", srv.URL)
	schemas := m.SchemasFor([]ServerDef{def})
	if len(schemas) != 2 {
		t.Fatalf("期望分页聚合 2 个工具，实际 %d", len(schemas))
	}
	// 消歧名：boom.bad 中的 . 应被替换
	schema := schemas[0].(map[string]any)["function"].(map[string]any)
	if schema["name"] != "mcp_fake_echo" {
		t.Fatalf("消歧名不符: %v", schema["name"])
	}
	if !strings.HasPrefix(schema["description"].(string), "[MCP server:fake]") {
		t.Fatalf("description 缺来源标注: %v", schema["description"])
	}
	if !m.IsTool("mcp_fake_echo") || !m.IsTool("mcp_fake_boom_bad") {
		t.Fatal("消歧索引未建立")
	}

	res := m.ExecuteTool(context.Background(), "mcp_fake_echo", map[string]any{"msg": "你好"})
	if res.Text != "echo: 你好" {
		t.Fatalf("执行结果不符: %q", res.Text)
	}
}

// SSE 响应形态 + list_changed 通知解析
func TestHTTPSSEList(t *testing.T) {
	fake := newFake(t, "stateless")
	fake.sseList = true
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := NewManager()
	def := httpDef(2, "fake", srv.URL)
	schemas := m.SchemasFor([]ServerDef{def})
	if len(schemas) != 2 {
		t.Fatalf("SSE 形态下分页聚合失败: %d", len(schemas))
	}
}

// ---------- legacy（2025-06-18）握手回退 + Session-Id 回带 ----------

func TestHTTPLegacyFlow(t *testing.T) {
	fake := newFake(t, "legacy")
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := NewManager()
	def := httpDef(3, "legacyfake", srv.URL)
	schemas := m.SchemasFor([]ServerDef{def})
	if len(schemas) != 2 {
		t.Fatalf("legacy 握手回退失败，工具数 %d", len(schemas))
	}
	res := m.ExecuteTool(context.Background(), "mcp_legacyfake_echo", map[string]any{"msg": "hi"})
	if res.Text != "echo: hi" {
		t.Fatalf("Session-Id 回带异常: %q", res.Text)
	}
}

// ---------- 错误双轨 / MRTR / 超时 ----------

func TestExecuteErrorTracks(t *testing.T) {
	fake := newFake(t, "stateless")
	fake.callBehavior["bad"] = func(args map[string]any) (*CallToolResult, *RPCError) {
		return &CallToolResult{Content: []ContentBlock{{Type: "text", Text: "参数 date 必须是未来时间"}},
			IsError: true}, nil
	}
	fake.callBehavior["gone"] = func(args map[string]any) (*CallToolResult, *RPCError) {
		return nil, &RPCError{Code: -32602, Message: "Unknown tool: gone"}
	}
	fake.callBehavior["ask"] = func(args map[string]any) (*CallToolResult, *RPCError) {
		return &CallToolResult{ResultType: "input_required"}, nil
	}
	fake.callBehavior["pic"] = func(args map[string]any) (*CallToolResult, *RPCError) {
		return &CallToolResult{Content: []ContentBlock{
			{Type: "text", Text: "已生成图片"},
			{Type: "image", MimeType: "image/png"},
		}, StructuredContent: json.RawMessage(`{"url":"https://x/1.png"}`)}, nil
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := NewManager()
	def := httpDef(4, "fake", srv.URL)
	m.SchemasFor([]ServerDef{def})

	if res := m.ExecuteTool(context.Background(), "mcp_fake_bad", nil); !strings.Contains(res.Text, "[MCP 工具执行错误]") {
		t.Fatalf("isError 应带执行错误前缀: %q", res.Text)
	}
	before := fake.listCalls
	if res := m.ExecuteTool(context.Background(), "mcp_fake_gone", nil); !strings.Contains(res.Text, "[MCP 调用失败]") {
		t.Fatalf("协议错误应带调用失败前缀: %q", res.Text)
	}
	// Unknown tool 应触发一次工具列表刷新
	time.Sleep(300 * time.Millisecond)
	if fake.listCalls <= before {
		t.Fatal("Unknown tool 未触发列表刷新")
	}
	if res := m.ExecuteTool(context.Background(), "mcp_fake_ask", nil); !strings.Contains(res.Text, "[MCP 工具等待输入]") {
		t.Fatalf("input_required 应返回占位文案: %q", res.Text)
	}
	res := m.ExecuteTool(context.Background(), "mcp_fake_pic", nil)
	if !strings.Contains(res.Text, "已生成图片") || !strings.Contains(res.Text, "非文本内容块") || !strings.Contains(res.Text, `"url"`) {
		t.Fatalf("多内容块/结构化结果桥接异常: %q", res.Text)
	}
}

func TestExecuteTimeout(t *testing.T) {
	fake := newFake(t, "stateless")
	fake.callsSleep = 3 * time.Second
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	def := httpDef(5, "slow", srv.URL)
	def.CallTimeoutSec = 1
	m := NewManager()
	m.SchemasFor([]ServerDef{def})
	res := m.ExecuteTool(context.Background(), "mcp_slow_echo", map[string]any{"msg": "x"})
	if !strings.Contains(res.Text, "[MCP 超时]") {
		t.Fatalf("应识别为超时: %q", res.Text)
	}
}

// ---------- 桥接纯函数 ----------

func TestQualifiedName(t *testing.T) {
	if q := QualifiedName("weather", "get_weather"); q != "mcp_weather_get_weather" {
		t.Fatalf("常规消歧名不符: %s", q)
	}
	if q := QualifiedName("db", "admin.tools.list"); q != "mcp_db_admin_tools_list" {
		t.Fatalf("非法字符应替换: %s", q)
	}
	long := QualifiedName("s", strings.Repeat("t", 100))
	if len(long) != 64 {
		t.Fatalf("超长名应截断到 64: %d", len(long))
	}
	if QualifiedName("s", strings.Repeat("t", 100)) != long {
		t.Fatal("同名应稳定")
	}
	if QualifiedName("s", strings.Repeat("u", 100)) == long {
		t.Fatal("不同超长名经哈希后缀应可区分")
	}
}

func TestBridgeCallResultTruncate(t *testing.T) {
	big := strings.Repeat("a", maxResultBytes+1024)
	out := BridgeCallResult(&CallToolResult{Content: []ContentBlock{{Type: "text", Text: big}}})
	if len(out) > maxResultBytes+512 {
		t.Fatalf("截断失效: %d", len(out))
	}
	if !strings.Contains(out, "已截断") {
		t.Fatal("截断应带说明")
	}
}

func TestFilterTools(t *testing.T) {
	tools := []Tool{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if got := filterTools(tools, nil); len(got) != 3 {
		t.Fatal("空白名单应全放行")
	}
	if got := filterTools(tools, []string{"a", "c"}); len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("白名单过滤异常: %+v", got)
	}
}

// ---------- stdio 假进程 ----------

// TestMain 测试二进制自执行：子进程模式跑 stdio 假服务器后退出
func TestMain(m *testing.M) {
	if os.Getenv("BE_MCP_STDIO_HELPER") == "1" {
		stdioHelperMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// stdioHelperMain 换行分隔 JSON-RPC 的假 MCP 服务器（legacy 模式）：
// discover 回 -32601 强制 initialize 回退，tools/list 一个 hello 工具
func stdioHelperMain() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	w := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil || req.ID == nil {
			continue // 通知忽略
		}
		var result any
		var rpcErr *RPCError
		switch req.Method {
		case "server/discover":
			rpcErr = &RPCError{Code: -32601, Message: "not found"}
		case "initialize":
			result = map[string]any{"protocolVersion": LegacyVersion, "serverInfo": map[string]any{"name": "stdio-fake"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name": "hello", "description": "打招呼", "inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			var p struct {
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			name, _ := p.Arguments["name"].(string)
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "hello " + name}}}
		default:
			rpcErr = &RPCError{Code: -32601, Message: "not found"}
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result, "error": rpcErr})
		_ = w.Flush()
	}
}

func TestStdioFlow(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("无法定位测试二进制")
	}
	tr, err := newStdioTransport([]string{exe}, map[string]string{"BE_MCP_STDIO_HELPER": "1"}, func(*RPCMessage) {})
	if err != nil {
		t.Fatalf("启动 stdio 假服务器失败: %v", err)
	}
	defer tr.Close()

	cli := newClient(tr)
	if err := cli.dial(context.Background()); err != nil {
		t.Fatalf("stdio legacy 握手失败: %v", err)
	}
	if cli.mode != modeLegacy || cli.serverName != "stdio-fake" {
		t.Fatalf("握手结果异常: mode=%d server=%s", cli.mode, cli.serverName)
	}
	tools, _, err := cli.listTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "hello" {
		t.Fatalf("tools/list 异常: %v %+v", err, tools)
	}
	res, err := cli.callTool(context.Background(), "hello", map[string]any{"name": "世界"})
	if err != nil || res.Content[0].Text != "hello 世界" {
		t.Fatalf("tools/call 异常: %v %+v", err, res)
	}
}

func TestStdioClose(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("无法定位测试二进制")
	}
	tr, err := newStdioTransport([]string{exe}, map[string]string{"BE_MCP_STDIO_HELPER": "1"}, func(*RPCMessage) {})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	_ = tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := tr.Request(ctx, &RPCRequest{JSONRPC: "2.0", ID: 99, Method: "ping"}); err == nil {
		t.Fatal("关闭后请求应失败")
	}
}

// 并发调用安全（同一连接多请求按 id 配对）
func TestConcurrentCalls(t *testing.T) {
	fake := newFake(t, "stateless")
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := NewManager()
	def := httpDef(9, "fake", srv.URL)
	m.SchemasFor([]ServerDef{def})

	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := fmt.Sprintf("m%d", i)
			res := m.ExecuteTool(context.Background(), "mcp_fake_echo", map[string]any{"msg": msg})
			if res.Text != "echo: "+msg {
				errs <- res.Text
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("并发响应串扰: %s", e)
	}
}
