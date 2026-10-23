package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"backend/dao"
	"backend/pkg/agentctx"
	"backend/pkg/provider"
)

type delegationFlowProvider struct{}

func (delegationFlowProvider) StreamChat(_ context.Context, req *provider.ChatRequest) (<-chan provider.StreamChunk, <-chan error) {
	chunks := make(chan provider.StreamChunk, 3)
	errs := make(chan error, 1)
	child := strings.Contains(req.Messages[0].Content, "## 子 Agent 执行约定")
	if child {
		chunks <- provider.StreamChunk{Content: "完成独立子任务"}
	} else if req.Messages[len(req.Messages)-1].Role == "user" {
		chunks <- provider.StreamChunk{ToolCalls: []provider.ToolCallDelta{
			{Index: 0, ID: "delegate-a", Name: DelegateAgentTool, Arguments: `{"name":"reader-a","task":"分析任务A"}`},
			{Index: 1, ID: "delegate-b", Name: DelegateAgentTool, Arguments: `{"name":"reader-b","task":"分析任务B"}`},
		}}
		chunks <- provider.StreamChunk{FinishReason: "tool_calls", Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 2}}
		close(chunks)
		return chunks, errs
	} else {
		chunks <- provider.StreamChunk{Content: "两项已完成"}
	}
	chunks <- provider.StreamChunk{FinishReason: "stop", Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 2}}
	close(chunks)
	return chunks, errs
}

func TestMultiAgentFullRootReActFlow(t *testing.T) {
	ctx := setupDB(t)
	session := newTestSession(t, ctx)
	var mu sync.Mutex
	var events []Event
	err := Run(ctx, Deps{Provider: delegationFlowProvider{}}, session, nil, "并行分析两个任务", nil, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := dao.ListAgentRuns(ctx, session.SessionID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("missing child runs: %v %v", runs, err)
	}
	for _, run := range runs {
		if run.Status != "completed" || run.WorkspaceID != session.SessionID {
			t.Fatalf("invalid run: %+v", run)
		}
		history, err := dao.ListMessagesBySessionID(agentctx.WithAgentID(ctx, run.AgentID), session.SessionID)
		if err != nil || len(history) != 2 {
			t.Fatalf("child history: %d %v", len(history), err)
		}
	}
	history, err := dao.ListMessagesBySessionID(ctx, session.SessionID)
	if err != nil || len(history) != 5 || history[4].Content != "两项已完成" {
		t.Fatalf("root history: %d %v", len(history), err)
	}
	var rootDone, childDone int
	for _, ev := range events {
		if ev.Type == "done" {
			rootDone++
		}
		if ev.Type == "agent_event" && ev.ChildEvent != nil && ev.ChildEvent.Type == "done" {
			childDone++
		}
		if ev.Type == "delta" && ev.Content == "完成独立子任务" {
			t.Fatal("child text leaked into root")
		}
	}
	if rootDone != 1 || childDone != 2 {
		t.Fatalf("root=%d child=%d terminal events", rootDone, childDone)
	}
	usage, err := dao.ListTokenUsagesBySession(ctx, session.SessionID, 100)
	if err != nil || len(usage) != 4 {
		t.Fatalf("root+child billing: %d %v", len(usage), err)
	}
}
