package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/config"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/provider"
)

// loadTestConfig 与后端启动一致：存在 config.local.yaml 时优先
func loadTestConfig(t *testing.T) config.Config {
	t.Helper()
	confPath := "../../etc/config.yaml"
	if _, err := os.Stat("../../etc/config.local.yaml"); err == nil {
		confPath = "../../etc/config.local.yaml"
	}
	return config.LoadConfig(confPath)
}

// fakeProvider 按剧本回放流式轮次
type fakeProvider struct {
	rounds [][]provider.StreamChunk
	calls  atomic.Int32
}

func (f *fakeProvider) StreamChat(_ context.Context, req *provider.ChatRequest) (<-chan provider.StreamChunk, <-chan error) {
	chunkChan := make(chan provider.StreamChunk, 64)
	errChan := make(chan error, 1)
	idx := int(f.calls.Add(1)) - 1
	go func() {
		defer close(chunkChan)
		if idx >= len(f.rounds) {
			errChan <- fmt.Errorf("no script for round %d", idx)
			return
		}
		for _, c := range f.rounds[idx] {
			chunkChan <- c
		}
	}()
	return chunkChan, errChan
}

// setupDB 与 api 集成测试同款：连本地 MySQL（config.local.yaml 优先）；进程内只初始化一次
var dbOnce sync.Once

func setupDB(t *testing.T) context.Context {
	t.Helper()
	dbOnce.Do(func() {
		conf := loadTestConfig(t)
		db.InitDb(conf.DbConf)
	})
	return db.WithContext(context.Background())
}

func newTestSession(t *testing.T, ctx context.Context) *model.Session {
	t.Helper()
	s := &model.Session{
		SessionID: "sess-test-" + fmt.Sprint(time.Now().UnixNano()),
		UserID:    1,
		CompanyID: 1,
		Title:     "新会话",
		Model:     "fake-model",
	}
	if err := dao.CreateSession(ctx, s); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = dao.DeleteSession(ctx, s.SessionID) })
	return s
}

func TestRunToolLoopHappyPath(t *testing.T) {
	ctx := setupDB(t)
	session := newTestSession(t, ctx)

	tcArgs := `{"path":"a.txt"}`
	fp := &fakeProvider{rounds: [][]provider.StreamChunk{
		{ // 第 1 轮：发起工具调用
			{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "call_1", Name: "read_file", Arguments: tcArgs}}},
			{FinishReason: "tool_calls"},
		},
		{ // 第 2 轮：给出最终回答
			{Content: "文件内容是 hello"},
			{FinishReason: "stop", Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 5}},
		},
	}}

	var events []Event
	emit := func(ev Event) { events = append(events, ev) }

	exec := func(_ context.Context, _, name string, args map[string]any) string {
		if name != "read_file" || args["path"] != "a.txt" {
			return "[执行失败] unexpected call"
		}
		return "hello"
	}

	if err := Run(ctx, Deps{Provider: fp, ExecTool: exec}, session, nil, "读一下 a.txt", nil, emit); err != nil {
		t.Fatalf("run: %v", err)
	}

	// 事件序：user_message_id → tool_call_start → tool_call_result → delta → done
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Type)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"user_message_id", "tool_call_start", "tool_call_result", "delta", "done"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing event %q in %v", want, kinds)
		}
	}
	var startWithArgs *Event
	var resultEv *Event
	for i := range events {
		ev := &events[i]
		if ev.Type == "tool_call_start" && ev.Arguments == tcArgs {
			startWithArgs = ev
		}
		if ev.Type == "tool_call_result" {
			resultEv = ev
		}
	}
	if startWithArgs == nil || startWithArgs.Name != "read_file" {
		t.Fatalf("missing tool_call_start with args %s: %+v", tcArgs, events)
	}
	if resultEv == nil || resultEv.Output != "hello" {
		t.Fatalf("tool_call_result wrong: %+v", resultEv)
	}

	// 落库校验：user + assistant(tool_calls) + tool + assistant(final)，标题自动提取
	msgs, err := dao.ListMessagesBySessionID(ctx, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant" {
		t.Fatalf("roles: %v", roles)
	}
	if msgs[1].ToolCalls == nil || !strings.Contains(*msgs[1].ToolCalls, "read_file") {
		t.Fatalf("assistant tool_calls not saved: %v", msgs[1].ToolCalls)
	}
	if msgs[2].ToolCallID == nil || *msgs[2].ToolCallID != "call_1" {
		t.Fatalf("tool msg tool_call_id wrong: %v", msgs[2].ToolCallID)
	}
	got, err := dao.GetSessionBySessionID(ctx, session.SessionID)
	if err != nil || got.Title != "读一下 a.txt" {
		t.Fatalf("auto title wrong: %v %+v", err, got)
	}
}

func TestRunLoopGuardKills(t *testing.T) {
	ctx := setupDB(t)
	session := newTestSession(t, ctx)

	// 模型每轮都请求同一个工具、同一参数；工具每次返回相同输出
	callChunks := []provider.StreamChunk{
		{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c", Name: "probe", Arguments: `{}`}}},
		{FinishReason: "tool_calls"},
	}
	rounds := make([][]provider.StreamChunk, 0, 8)
	for i := 0; i < 8; i++ {
		rounds = append(rounds, callChunks)
	}
	fp := &fakeProvider{rounds: rounds}

	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	exec := func(context.Context, string, string, map[string]any) string { return "same-output" }

	if err := Run(ctx, Deps{Provider: fp, ExecTool: exec}, session, nil, "test", nil, emit); err != nil {
		t.Fatalf("run: %v", err)
	}

	var hasKill bool
	var hintSeen bool
	for _, ev := range events {
		if ev.Type == "error" && strings.Contains(ev.Error, "死循环") {
			hasKill = true
		}
		if ev.Type == "tool_call_result" && strings.Contains(ev.Output, "Agent Guard") {
			hintSeen = true
		}
	}
	if !hasKill {
		t.Fatal("loop guard should kill the turn")
	}
	if !hintSeen {
		t.Fatal("3rd duplicate call should carry hint")
	}
}

func TestRunFinishLengthAborts(t *testing.T) {
	ctx := setupDB(t)
	session := newTestSession(t, ctx)
	fp := &fakeProvider{rounds: [][]provider.StreamChunk{
		{{Content: "partial"}, {FinishReason: "length"}},
	}}
	var events []Event
	if err := Run(ctx, Deps{Provider: fp, ExecTool: nil}, session, nil, "hi", nil, func(ev Event) { events = append(events, ev) }); err != nil {
		t.Fatal(err)
	}
	var hasTruncNote bool
	for _, ev := range events {
		if ev.Type == "error" && strings.Contains(ev.Error, "截断") {
			hasTruncNote = true
		}
	}
	if !hasTruncNote {
		t.Fatal("finish=length should abort with note")
	}
}
