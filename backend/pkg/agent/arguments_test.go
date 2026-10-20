package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backend/dal/model"
	"backend/dal/query"
	"backend/pkg/mcp"
	"backend/pkg/provider"
	"backend/pkg/tools"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 走真实 saveMessage/GORM 构造路径，DryRun 捕获待插入实体，不连接任何数据库。
func captureMessages(t *testing.T) (context.Context, *[]model.Message) {
	t.Helper()
	database, err := gorm.Open(mysql.New(mysql.Config{
		DSN: "unused:unused@tcp(127.0.0.1:1)/unused", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	var messages []model.Message
	if err := database.Callback().Create().After("gorm:create").Register("capture_messages", func(tx *gorm.DB) {
		switch value := tx.Statement.Dest.(type) {
		case *model.Message:
			messages = append(messages, *value)
		case []*model.Message:
			for _, msg := range value {
				messages = append(messages, *msg)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	// 与 db.InjectQuery 相同的查询门面注入；仅在本测试上下文生效。
	return context.WithValue(context.Background(), "gormDb", query.Use(database)), &messages
}

type argumentProvider struct {
	rounds   [][]provider.StreamChunk
	requests []provider.ChatRequest
}

func (p *argumentProvider) StreamChat(_ context.Context, req *provider.ChatRequest) (<-chan provider.StreamChunk, <-chan error) {
	snapshot := *req
	snapshot.Messages = append([]provider.ChatMessage(nil), req.Messages...)
	index := len(p.requests)
	p.requests = append(p.requests, snapshot)
	chunks := make(chan provider.StreamChunk, 16)
	errs := make(chan error, 1)
	if index >= len(p.rounds) {
		errs <- fmt.Errorf("unexpected request %d", index)
	} else {
		for _, chunk := range p.rounds[index] {
			chunks <- chunk
		}
	}
	close(chunks)
	return chunks, errs
}

func argumentScript(name, raw, finish string) *argumentProvider {
	return &argumentProvider{rounds: [][]provider.StreamChunk{
		{
			{Content: "执行调用", ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "arg_call", Name: name, Arguments: raw}}},
			{FinishReason: finish},
		},
		{{Content: "完成", FinishReason: "stop"}},
	}}
}

func assertArgumentReplay(t *testing.T, msg provider.ChatMessage, rejected bool) {
	t.Helper()
	calls := asCalls(t, msg)
	if msg.CtxMode != "stub" || len(calls) != 1 || calls[0].ID != "arg_call" {
		t.Fatalf("forced replay must preserve mode and call identity: %+v", msg)
	}
	args, errMsg := tools.ParseArguments(calls[0].Function.Arguments)
	if errMsg != "" || len(calls[0].Function.Arguments) >= 8<<10 {
		t.Fatalf("replay must be valid and bounded: %s", errMsg)
	}
	if rejected {
		if args["_rejected"] != true || strings.Contains(fmt.Sprint(args["note"]), "超限未执行") {
			t.Fatalf("invalid JSON needs an accurate rejection marker: %v", args)
		}
	} else if args["_pruned"] != true || args["_rejected"] != nil {
		t.Fatalf("large valid call must only be pruned: %v", args)
	}
	if !strings.HasPrefix(fmt.Sprint(args["_source"]), ".tool-outputs/arguments-") {
		t.Fatalf("replay should locate the original arguments: %v", args)
	}
	wire, err := json.Marshal(msg)
	if err != nil || strings.Contains(string(wire), "ctx_mode") || strings.Contains(string(wire), "CtxMode") {
		t.Fatal("local context policy must not be sent as a provider field")
	}
}

func TestArgumentRunPreservesAndExecutesLargeWrite(t *testing.T) {
	workspace, _ := setupBlobEnv(t)
	ctx, saved := captureMessages(t)
	content := strings.Repeat("完整内容\n", 10000)
	encoded, _ := json.Marshal(map[string]any{"path": "large.txt", "content": content})
	raw := string(encoded)
	p := argumentScript("write_file", raw, "tool_calls")
	session := &model.Session{SessionID: "large-write", Model: "test-model", Title: "existing"}
	if err := Run(ctx, Deps{Provider: p}, session, nil, "写文件", nil, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, session.SessionID, "large.txt"))
	if err != nil || string(data) != content {
		t.Fatalf("tool must execute full arguments: %v", err)
	}
	assertSavedArguments(t, *saved, raw)
	assertArgumentReplay(t, p.requests[1].Messages[2], false)
	// 下一轮输入很小、没有 usage 水位，仍立即强制 stub。
	if p.requests[1].Messages[3].ToolCallID != "arg_call" || tools.IsErrorResult(p.requests[1].Messages[3].Content) {
		t.Fatal("successful execution result must remain paired with the call")
	}
	replayed := buildContext(context.Background(), session, *saved)
	assertArgumentReplay(t, replayed[2], false)
}

func assertSavedArguments(t *testing.T, saved []model.Message, raw string) {
	t.Helper()
	if len(saved) < 2 || saved[1].ToolCalls == nil || saved[1].StubToolCalls == nil || saved[1].CtxMode != "stub" {
		t.Fatalf("saveMessage must persist raw and stub together: %+v", saved)
	}
	var original []savedCall
	if err := json.Unmarshal([]byte(*saved[1].ToolCalls), &original); err != nil || original[0].Function.Arguments != raw {
		t.Fatalf("stored arguments must exactly match model output: %v", err)
	}
	if saved[1].BlobRef == nil {
		t.Fatal("forced replay should also archive the full original")
	}
	if tools.ArgsNeedStub(raw) && saved[1].TokensFull <= saved[1].StubTokens {
		t.Fatal("token metadata should distinguish complete arguments from replay stubs")
	}
	path, url := decodeBlobRef(*saved[1].BlobRef)
	archive, err := fetchArchive(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	var archived []savedCall
	if err := json.Unmarshal(archive, &archived); err != nil || archived[0].Function.Arguments != raw {
		t.Fatalf("archive must preserve even invalid argument strings: %v", err)
	}
	var stubs []savedCall
	if err := json.Unmarshal([]byte(*saved[1].StubToolCalls), &stubs); err != nil {
		t.Fatal(err)
	}
	args, _ := tools.ParseArguments(stubs[0].Function.Arguments)
	if args["_source"] != path {
		t.Fatal("persisted stub and archive must use the same deterministic path")
	}
}

func TestArgumentRunPreservesInvalidJSONWithoutExecution(t *testing.T) {
	for _, finish := range []string{"tool_calls", "length", "cancel"} {
		t.Run(finish, func(t *testing.T) {
			setupBlobEnv(t)
			ctx, saved := captureMessages(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			raw := `{"path":"a.py","content":"partial`
			p := argumentScript("write_file", raw, finish)
			if finish == "cancel" {
				p.rounds = p.rounds[:1]
				p.rounds[0] = p.rounds[0][:1]
			}
			executed := false
			session := &model.Session{SessionID: "invalid-args", Model: "test-model", Title: "existing"}
			if err := Run(ctx, Deps{Provider: p, ExecTool: func(context.Context, string, string, map[string]any) tools.Output {
				executed = true
				return tools.Output{Text: "unexpected"}
			}}, session, nil, "写文件", nil, func(ev Event) {
				if finish == "cancel" && ev.Type == "tool_call_start" {
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			if executed {
				t.Fatal("invalid JSON must never reach tool execution")
			}
			assertSavedArguments(t, *saved, raw)
			if finish == "tool_calls" {
				assertArgumentReplay(t, p.requests[1].Messages[2], true)
				if !strings.HasPrefix(p.requests[1].Messages[3].Content, "[参数解析失败]") {
					t.Fatal("model should receive the parse error result")
				}
			}
			replayed := buildContext(context.Background(), session, *saved)
			assertArgumentReplay(t, replayed[2], true)
		})
	}
}

func TestArgumentRunMCPReceivesFullPayload(t *testing.T) {
	setupBlobEnv(t)
	ctx, saved := captureMessages(t)
	content := strings.Repeat("mcp-payload", 6000)
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch request.Method {
		case "server/discover":
			result = map[string]any{"protocolVersions": []string{mcp.LatestVersion}, "serverInfo": map[string]string{"name": "args-test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "echo", "inputSchema": map[string]string{"type": "object"}}}}
		case "tools/call":
			received <- request.Params.Arguments
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "ok"}}}
		default:
			t.Errorf("unexpected MCP method: %s", request.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	def := mcp.ServerDef{ID: 938471, Name: "args-test", Transport: "http", URL: server.URL}
	t.Cleanup(func() { mcp.RemoveServer(def.ID) })
	if schemas := mcp.SchemasFor([]mcp.ServerDef{def}); len(schemas) != 1 {
		t.Fatalf("local MCP test server should register: %v", schemas)
	}
	encoded, _ := json.Marshal(map[string]any{
		"payload":   content,
		"record_id": json.Number("9007199254740993"),
		"options":   map[string]any{"enabled": true, "cursor": json.Number("9223372036854775807")},
	})
	raw := string(encoded)
	p := argumentScript("mcp_args-test_echo", raw, "tool_calls")
	if err := Run(ctx, Deps{Provider: p}, &model.Session{SessionID: "mcp-args", Model: "test-model", Title: "existing"}, nil, "调用 MCP", nil, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case actual := <-received:
		actualJSON, err := json.Marshal(actual)
		if err != nil || string(actualJSON) != raw {
			t.Fatal("MCP must receive the complete original payload")
		}
	default:
		t.Fatal("MCP tools/call was not executed")
	}
	assertSavedArguments(t, *saved, raw)
	assertArgumentReplay(t, p.requests[1].Messages[2], false)
}

func TestArgumentGenericStubIsBoundedAndIdempotent(t *testing.T) {
	args := map[string]any{"_pruned": true, "_rejected": true, "nested": map[string]any{"payload": strings.Repeat("x", 50000)}}
	for i := 0; i < 100; i++ {
		args[fmt.Sprintf("%03d-%s", i, strings.Repeat("长", 500))] = []any{"data", 1, true}
	}
	encoded, _ := json.Marshal(args)
	calls := []savedCall{newSavedCall("arg_call", "mcp_test_echo", string(encoded))}
	stubs := stubForToolCalls(calls)
	assertArgumentReplay(t, provider.ChatMessage{Role: "assistant", ToolCalls: stubs, CtxMode: "stub"}, false)
	again := stubForToolCalls(stubs)
	if stubs[0].Function.Arguments != again[0].Function.Arguments || calls[0].Function.Arguments != string(encoded) {
		t.Fatal("generic stub must be idempotent and never mutate original arguments")
	}
}

func TestArgumentContextModeOverridesLatestStep(t *testing.T) {
	raw := `{"path":"small.py","content":"hello"}`
	calls := []savedCall{newSavedCall("arg_call", "write_file", raw)}
	msgs := []provider.ChatMessage{
		{Role: "assistant", Content: "正文保留", ToolCalls: calls, CtxMode: "stub"},
		{Role: "tool", Content: "已写入", ToolCallID: "arg_call"},
	}
	rezoneMsgs(msgs, "test-model")
	if !strings.Contains(asCalls(t, msgs[0])[0].Function.Arguments, "_pruned") ||
		msgs[0].Content != "正文保留" || calls[0].Function.Arguments != raw {
		t.Fatal("ctx_mode must override the latest-step exemption without changing originals")
	}
	session := &model.Session{Model: "test-model"}
	history := []model.Message{{Role: "assistant", Content: "正文保留", ToolCalls: callsJSON(t, calls...), CtxMode: "stub"}}
	fromDB := buildContext(context.Background(), session, history)
	if asCalls(t, fromDB[1])[0].Function.Arguments != asCalls(t, msgs[0])[0].Function.Arguments {
		t.Fatal("history and in-turn forced stubs must match")
	}
	// 旧行缺少模式与 stub 时，大参数仍即时安全回放，不改历史原文。
	large := `{"code":"` + strings.Repeat("x", 50000) + `"}`
	history[0] = model.Message{Role: "assistant", ToolCalls: callsJSON(t, newSavedCall("arg_call", "python_exec", large))}
	legacy := buildContext(context.Background(), session, history)
	assertArgumentReplay(t, legacy[1], false)
}
