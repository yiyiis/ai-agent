package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agentctx"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
	"backend/pkg/tools"
)

type agentRuntimeStore struct {
	mu     sync.Mutex
	rows   map[string]*model.AgentRun
	events []Event
}

func newRuntimeTest(t *testing.T) (*multiAgentRuntime, *agentRuntimeStore) {
	t.Helper()
	store := &agentRuntimeStore{rows: map[string]*model.AgentRun{}}
	r := newMultiAgentRuntime(Deps{}, &model.Session{SessionID: "root-session", Model: "test-model"}, func(ev Event) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.events = append(store.events, ev)
	})
	r.createRun = func(_ context.Context, row *model.AgentRun) error {
		store.mu.Lock()
		defer store.mu.Unlock()
		copy := *row
		store.rows[row.AgentID] = &copy
		return nil
	}
	r.updateRun = func(ctx context.Context, id string, values map[string]interface{}) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		row := store.rows[id]
		if row == nil {
			return fmt.Errorf("missing run %s", id)
		}
		if value, ok := values["status"].(string); ok {
			row.Status = value
		}
		if value, ok := values["result"].(string); ok {
			row.Result = value
		}
		if value, ok := values["error"].(string); ok {
			row.Error = value
		}
		return nil
	}
	r.findRun = func(_ context.Context, sid, callID string) (*model.AgentRun, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		for _, row := range store.rows {
			if row.SessionID == sid && row.ToolCallID == callID {
				copy := *row
				return &copy, nil
			}
		}
		return nil, nil
	}
	r.runChild = func(_ context.Context, _ Deps, _ *model.Session, _ string, emit func(Event)) error {
		emit(Event{Type: "delta", Content: "completed result"})
		emit(Event{Type: "done"})
		return nil
	}
	r.loadArtifacts = func(context.Context, *model.AgentRun) []Attachment { return nil }
	return r, store
}

func delegateArgs(mode string) map[string]any {
	return map[string]any{"name": "reviewer", "task": "Read the assigned files and report findings.", "mode": mode}
}

func (s *agentRuntimeStore) statusCounts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	for _, row := range s.rows {
		counts[row.Status]++
	}
	return counts
}

func waitAgentTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestMultiAgentIndependentIdentitySharedWorkspaceAndCapabilities(t *testing.T) {
	r, store := newRuntimeTest(t)
	skills := &turnSkills{enabledByName: map[string]string{"skill": "dir"}, envs: map[string]string{"TOKEN": "secret"}}
	var seenAgentID string
	r.runChild = func(ctx context.Context, deps Deps, session *model.Session, _ string, emit func(Event)) error {
		seenAgentID = agentctx.AgentID(ctx)
		if seenAgentID == "" || session.SessionID != r.session.SessionID {
			t.Error("child identity and workspace not separated")
		}
		if !deps.child || deps.multi != r || deps.maxRounds != MaxChildRounds || deps.childMode != "read_only" {
			t.Error("child runtime limits not inherited")
		}
		if !childAllowedTool(deps, "read_file") || childAllowedTool(deps, "write_file") || childAllowedTool(deps, DelegateAgentTool) {
			t.Error("read-only allowlist bypass")
		}
		deps.inheritedSkills.envs["TOKEN"] = "changed"
		if !strings.Contains(session.SystemPrompt, "read_only") {
			t.Error("missing worker constraints")
		}
		emit(Event{Type: "delta", Content: "investigation preamble"})
		emit(Event{Type: "tool_call_start", ID: "read"})
		emit(Event{Type: "delta", Content: "final findings"})
		emit(Event{Type: "tool_artifact", Attachments: []Attachment{{URL: "https://example.test/file", Filename: "report.txt"}}})
		emit(Event{Type: "done"})
		return nil
	}
	out := r.delegate(context.Background(), "identity-call", delegateArgs("read_only"), skills, tools.Schemas())
	if tools.IsErrorResult(out.Text) || !strings.Contains(out.Text, "final findings") || strings.Contains(out.Text, "investigation preamble") {
		t.Fatalf("wrong final result: %s", out.Text)
	}
	if len(out.Attachments) != 1 {
		t.Fatalf("lost artifact: %+v", out.Attachments)
	}
	if skills.envs["TOKEN"] != "secret" {
		t.Error("parent skills mutated")
	}
	if r.session.Title != "" || r.session.SystemPrompt != "" {
		t.Error("root session mutated")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	row := store.rows[seenAgentID]
	if row.Status != "completed" || row.WorkspaceID != "root-session" || row.ParentAgentID != "root-session" {
		t.Fatalf("bad persisted run: %+v", row)
	}
	for _, ev := range store.events {
		if ev.Type != "agent_status" && ev.Type != "agent_event" {
			t.Fatalf("leaked child event: %s", ev.Type)
		}
		if ev.AgentID != seenAgentID {
			t.Error("missing event agent identity")
		}
	}
}

func TestMultiAgentFailuresAndCancellationAreTerminal(t *testing.T) {
	for _, testcase := range []struct {
		name, status string
		run          func(context.Context, func(Event)) error
	}{
		{"error-event-nil-return", "failed", func(_ context.Context, emit func(Event)) error {
			emit(Event{Type: "error", Error: "provider failed"})
			emit(Event{Type: "done"})
			return nil
		}},
		{"returned-error", "failed", func(_ context.Context, _ func(Event)) error { return fmt.Errorf("provider unavailable") }},
		{"panic", "failed", func(_ context.Context, _ func(Event)) error { panic("worker failed") }},
		{"timeout", "cancelled", func(ctx context.Context, _ func(Event)) error { <-ctx.Done(); return ctx.Err() }},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			r, store := newRuntimeTest(t)
			r.timeout = 20 * time.Millisecond
			r.runChild = func(ctx context.Context, _ Deps, _ *model.Session, _ string, emit func(Event)) error {
				return testcase.run(ctx, emit)
			}
			out := r.delegate(context.Background(), "failure-call", delegateArgs("read_only"), nil, tools.Schemas())
			if !tools.IsErrorResult(out.Text) {
				t.Fatalf("failure reported as success: %s", out.Text)
			}
			if counts := store.statusCounts(); counts[testcase.status] != 1 {
				t.Fatalf("wrong terminal state: %v", counts)
			}
		})
	}
}

func TestMultiAgentConcurrentLimitQueueAndRootCancellation(t *testing.T) {
	r, store := newRuntimeTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, maxActive atomic.Int32
	r.runChild = func(ctx context.Context, _ Deps, _ *model.Session, _ string, _ func(Event)) error {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); current > old && !maxActive.CompareAndSwap(old, current); old = maxActive.Load() {
		}
		<-ctx.Done()
		return ctx.Err()
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.delegate(ctx, fmt.Sprintf("queue-%d", i), delegateArgs("read_only"), nil, tools.Schemas())
		}(i)
	}
	waitAgentTest(t, func() bool { counts := store.statusCounts(); return counts["running"] == 3 && counts["queued"] == 3 })
	cancel()
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("root cancellation leaked children")
	}
	if maxActive.Load() != 3 || active.Load() != 0 {
		t.Fatalf("concurrency limit broken: active=%d max=%d", active.Load(), maxActive.Load())
	}
	if counts := store.statusCounts(); counts["cancelled"] != 6 {
		t.Fatalf("queued/running children not all cancelled: %v", counts)
	}
}

func TestMultiAgentCumulativeLimitDoesNotCreateExtraRun(t *testing.T) {
	r, store := newRuntimeTest(t)
	for i := 0; i < MaxAgentsPerTurn; i++ {
		if out := r.delegate(context.Background(), fmt.Sprintf("limit-%d", i), delegateArgs("read_only"), nil, tools.Schemas()); tools.IsErrorResult(out.Text) {
			t.Fatal(out.Text)
		}
	}
	if out := r.delegate(context.Background(), "limit-overflow", delegateArgs("read_only"), nil, tools.Schemas()); !tools.IsErrorResult(out.Text) {
		t.Fatal("missing cumulative limit")
	}
	if counts := store.statusCounts(); counts["completed"] != MaxAgentsPerTurn {
		t.Fatalf("unexpected run count: %v", counts)
	}
}

func TestMultiAgentDuplicateCallRunsOnce(t *testing.T) {
	r, _ := newRuntimeTest(t)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	r.runChild = func(_ context.Context, _ Deps, _ *model.Session, _ string, emit func(Event)) error {
		calls.Add(1)
		close(entered)
		<-release
		emit(Event{Type: "delta", Content: "one result"})
		emit(Event{Type: "done"})
		return nil
	}
	outputs := make(chan tools.Output, 2)
	go func() {
		outputs <- r.delegate(context.Background(), "duplicate", delegateArgs("read_only"), nil, tools.Schemas())
	}()
	<-entered
	go func() {
		outputs <- r.delegate(context.Background(), "duplicate", delegateArgs("read_only"), nil, tools.Schemas())
	}()
	close(release)
	first, second := <-outputs, <-outputs
	if calls.Load() != 1 || first.Text != second.Text {
		t.Fatalf("duplicate execution: %d %q %q", calls.Load(), first.Text, second.Text)
	}
}

func TestMultiAgentDurableCallsNeverReexecute(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled", "interrupted", "running", "queued"} {
		t.Run(status, func(t *testing.T) {
			r, store := newRuntimeTest(t)
			store.rows["previous"] = &model.AgentRun{AgentID: "previous", SessionID: "root-session", ToolCallID: "persisted", Status: status, Result: "durable result"}
			r.runChild = func(context.Context, Deps, *model.Session, string, func(Event)) error {
				t.Error("durable task executed twice")
				return nil
			}
			out := r.delegate(context.Background(), "persisted", delegateArgs("read_only"), nil, tools.Schemas())
			if !strings.Contains(out.Text, "previous") {
				t.Fatalf("lost durable identity: %s", out.Text)
			}
			if r.total != 0 {
				t.Fatal("replaying a durable result consumed new-task budget")
			}
		})
	}
}

func TestMultiAgentWorkspaceMutexCancelsWaiter(t *testing.T) {
	r, _ := newRuntimeTest(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		r.withWorkspace(context.Background(), func() tools.Output { close(entered); <-release; return tools.Output{Text: "done"} })
		close(finished)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out := r.withWorkspace(ctx, func() tools.Output { t.Error("cancelled waiter executed tool"); return tools.Output{} })
	if !tools.IsErrorResult(out.Text) {
		t.Fatal("waiting did not cancel")
	}
	close(release)
	<-finished
	if out := r.withWorkspace(context.Background(), func() tools.Output { return tools.Output{Text: "next"} }); out.Text != "next" {
		t.Fatal("workspace lock leaked")
	}
}

func TestMultiAgentRejectsNestedAndInvalidDelegation(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		args map[string]any
	}{
		{"nested", agentctx.WithAgentID(context.Background(), "child"), delegateArgs("write")},
		{"invalid-mode", context.Background(), delegateArgs("administrator")},
		{"invalid-mode-type", context.Background(), map[string]any{"name": "x", "task": "y", "mode": true}},
		{"empty-task", context.Background(), map[string]any{"name": "x", "task": " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newRuntimeTest(t)
			out := r.delegate(tc.ctx, "invalid", tc.args, nil, tools.Schemas())
			if !tools.IsErrorResult(out.Text) || len(s.statusCounts()) != 0 {
				t.Fatalf("invalid task accepted: %s", out.Text)
			}
		})
	}
}

func TestMultiAgentWriteModeCannotInventTools(t *testing.T) {
	schemas := append(tools.Schemas(), DelegateAgentSchema(), map[string]any{"function": map[string]any{"name": "mcp_enabled_tool"}}, map[string]any{"function": map[string]any{"name": "request_input"}}, tools.LoadSkillSchema())
	deps := Deps{child: true, childMode: "write", inheritedSchemas: filterChildSchemas(schemas, "write")}
	for _, name := range []string{"read_file", "write_file", "bash", "mcp_enabled_tool"} {
		if !childAllowedTool(deps, name) {
			t.Errorf("missing inherited tool %s", name)
		}
	}
	for _, name := range []string{DelegateAgentTool, "request_input", tools.LoadSkillTool, "mcp_other_session_tool"} {
		if childAllowedTool(deps, name) {
			t.Errorf("capability escalation %s", name)
		}
	}
}

// Exercise the actual ReAct loop and DAO boundary, rather than only its runtime
// wrapper: even a provider emitting a hidden tool cannot upgrade a read-only child.
func TestMultiAgentRunIsolatesHistoryAndRejectsForgedWrites(t *testing.T) {
	ctx := setupDB(t)
	session := newTestSession(t, ctx)
	root := t.TempDir()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: root})
	t.Cleanup(func() { sandbox.Init(sandbox.Config{Driver: "local"}) })
	if err := sandbox.WriteFile(ctx, session.SessionID, "shared.txt", []byte("shared root workspace")); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{rounds: [][]provider.StreamChunk{
		{
			{ToolCalls: []provider.ToolCallDelta{
				{Index: 0, ID: "forged-write", Name: "write_file", Arguments: `{"path":"forbidden.txt","content":"bad"}`},
				{Index: 1, ID: "allowed-read", Name: "read_file", Arguments: `{"path":"shared.txt"}`},
			}},
			{FinishReason: "tool_calls"},
		},
		{{Content: "Verified shared root workspace; write was denied."}, {FinishReason: "stop", Usage: &provider.Usage{PromptTokens: 12, CompletionTokens: 7}}},
	}}
	var toolCalls atomic.Int32
	r := newMultiAgentRuntime(Deps{Provider: provider, ExecTool: func(ctx context.Context, sid, name string, args map[string]any) tools.Output {
		toolCalls.Add(1)
		if sid != session.SessionID || agentctx.AgentID(ctx) == "" || name != "read_file" {
			t.Errorf("child tool bypassed boundary: session=%s agent=%s name=%s", sid, agentctx.AgentID(ctx), name)
		}
		return tools.ExecuteWithOutput(ctx, sid, name, args)
	}}, session, func(Event) {})
	out := r.delegate(ctx, "integration-delegate", delegateArgs("read_only"), nil, tools.Schemas())
	if tools.IsErrorResult(out.Text) || toolCalls.Load() != 1 {
		t.Fatalf("execution failed or forged tool ran: %s calls=%d", out.Text, toolCalls.Load())
	}
	rows, err := dao.ListAgentRuns(ctx, session.SessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("agent runs: %v %v", rows, err)
	}
	row := rows[0]
	if row.Status != "completed" || row.WorkspaceID != session.SessionID {
		t.Fatalf("wrong persisted identity: %+v", row)
	}
	rootMessages, err := dao.ListMessagesBySessionID(ctx, session.SessionID)
	if err != nil || len(rootMessages) != 0 {
		t.Fatalf("child polluted main history: %v %v", rootMessages, err)
	}
	childMessages, err := dao.ListMessagesBySessionID(agentctx.WithAgentID(ctx, row.AgentID), session.SessionID)
	if err != nil || len(childMessages) != 5 {
		t.Fatalf("missing isolated child transcript: count=%d err=%v", len(childMessages), err)
	}
	var rejected, read bool
	for _, message := range childMessages {
		if message.AgentID != row.AgentID {
			t.Fatal("message missing agent ownership")
		}
		if message.ToolCallID == nil {
			continue
		}
		if *message.ToolCallID == "forged-write" {
			rejected = strings.Contains(message.Content, "未获授权")
		}
		if *message.ToolCallID == "allowed-read" {
			read = strings.Contains(message.Content, "shared root workspace")
		}
	}
	if !rejected || !read {
		t.Fatalf("capability/shared-file check failed: rejected=%v read=%v", rejected, read)
	}
	if _, err := os.Stat(filepath.Join(root, session.SessionID, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("forbidden write reached filesystem: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, row.AgentID)); !os.IsNotExist(err) {
		t.Fatalf("child created a separate workspace: %v", err)
	}
	usage, err := dao.ListTokenUsagesBySession(ctx, session.SessionID, 100)
	if err != nil || len(usage) != 1 || usage[0].AgentID != row.AgentID {
		t.Fatalf("child costs not attributed to root and agent: %v %v", usage, err)
	}
}
