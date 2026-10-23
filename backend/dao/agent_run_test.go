package dao_test

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agentctx"
	"backend/pkg/db"
	"github.com/google/uuid"
)

func newAgentTestSession(t *testing.T, ctx context.Context) *model.Session {
	t.Helper()
	s := &model.Session{SessionID: uuid.NewString(), UserID: 71001, CompanyID: 71001, Model: "test-model", Title: "agent-isolation-test"}
	if err := dao.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dao.DeleteSession(ctx, s.SessionID); err != nil {
			t.Errorf("cleanup session: %v", err)
		}
		u := db.Ctx(ctx).TokenUsage
		if _, err := u.WithContext(ctx).Where(u.SessionID.Eq(s.SessionID)).Delete(); err != nil {
			t.Errorf("cleanup usage: %v", err)
		}
	})
	return s
}

func newAgentTestRun(t *testing.T, ctx context.Context, sessionID, callID string) *model.AgentRun {
	t.Helper()
	r := &model.AgentRun{AgentID: uuid.NewString(), SessionID: sessionID, ToolCallID: callID, Name: "worker", Task: "Inspect assigned files"}
	if err := dao.CreateAgentRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAgentRunOwnershipAndReplay(t *testing.T) {
	ctx := setupTestDB(t)
	s1, s2 := newAgentTestSession(t, ctx), newAgentTestSession(t, ctx)
	r := newAgentTestRun(t, ctx, s1.SessionID, "spawn-call")
	if r.ParentAgentID != s1.SessionID || r.WorkspaceID != s1.SessionID || r.Model != s1.Model || r.Status != "queued" {
		t.Fatalf("incorrect inherited ownership: %+v", r)
	}
	if found, err := dao.GetAgentRun(ctx, s2.SessionID, r.AgentID); err != nil || found != nil {
		t.Fatalf("cross-session lookup leaked agent: %+v, %v", found, err)
	}
	if found, err := dao.FindAgentRunByToolCall(ctx, s1.SessionID, r.ToolCallID); err != nil || found == nil || found.AgentID != r.AgentID {
		t.Fatalf("replay lookup lost original execution: %+v, %v", found, err)
	}
	duplicate := *r
	duplicate.AgentID = uuid.NewString()
	if err := dao.CreateAgentRun(ctx, &duplicate); err == nil {
		t.Fatal("duplicate tool call must not create a second execution")
	}
	newAgentTestRun(t, ctx, s2.SessionID, r.ToolCallID)
	invalid := &model.AgentRun{AgentID: uuid.NewString(), SessionID: s2.SessionID, ParentAgentID: r.AgentID, ToolCallID: "cross-parent", Task: "invalid"}
	if err := dao.CreateAgentRun(ctx, invalid); err == nil {
		t.Fatal("cross-session parent must be rejected")
	}
	invalid.ParentAgentID, invalid.WorkspaceID = s2.SessionID, s1.SessionID
	if err := dao.CreateAgentRun(ctx, invalid); err == nil {
		t.Fatal("cross-session workspace must be rejected")
	}
	rows, err := dao.ListAgentRuns(ctx, s1.SessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one session child: %d, %v", len(rows), err)
	}
	if err := dao.UpdateAgentRun(ctx, r.AgentID, map[string]interface{}{"workspace_id": s2.SessionID}); err == nil {
		t.Fatal("persisted ownership must be immutable")
	}
	started := time.Now()
	if err := dao.UpdateAgentRun(ctx, r.AgentID, map[string]interface{}{"status": "running", "started_at": started}); err != nil {
		t.Fatal(err)
	}
	if err := dao.UpdateAgentRun(ctx, r.AgentID, map[string]interface{}{"status": "completed", "result": "done", "finished_at": time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := dao.UpdateAgentRun(ctx, r.AgentID, map[string]interface{}{"status": "running"}); err == nil {
		t.Fatal("late worker cannot reopen a terminal execution")
	}
	finished, err := dao.GetAgentRun(ctx, s1.SessionID, r.AgentID)
	if err != nil || finished.Result != "done" || finished.StartedAt == nil || finished.FinishedAt == nil {
		t.Fatalf("completed result missing: %+v, %v", finished, err)
	}
}

func TestAgentMessageIsolationTruncationAndSessionDeletion(t *testing.T) {
	ctx := setupTestDB(t)
	s := newAgentTestSession(t, ctx)
	a := newAgentTestRun(t, ctx, s.SessionID, "spawn-a")
	b := newAgentTestRun(t, ctx, s.SessionID, "spawn-b")
	aCtx, bCtx := agentctx.WithAgentID(ctx, a.AgentID), agentctx.WithAgentID(ctx, b.AgentID)
	create := func(c context.Context, body string) *model.Message {
		t.Helper()
		m := &model.Message{MessageID: uuid.NewString(), SessionID: s.SessionID, AgentID: "untrusted-caller-value", Role: "user", Content: body}
		if err := dao.CreateMessage(c, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	rootMessage, aMessage, bMessage := create(ctx, "root"), create(aCtx, "a"), create(bCtx, "b")
	if rootMessage.AgentID != "" || aMessage.AgentID != a.AgentID || bMessage.AgentID != b.AgentID {
		t.Fatal("message writer must use trusted context ownership")
	}
	for _, tc := range []struct {
		ctx  context.Context
		want string
	}{{ctx, "root"}, {aCtx, "a"}, {bCtx, "b"}} {
		rows, err := dao.ListMessagesBySessionID(tc.ctx, s.SessionID)
		if err != nil || len(rows) != 1 || rows[0].Content != tc.want {
			t.Fatalf("message stream mixed: %+v, %v", rows, err)
		}
		count, err := dao.CountMessagesBySessionID(tc.ctx, s.SessionID)
		if err != nil || count != 1 {
			t.Fatalf("count crossed agent boundary: %d, %v", count, err)
		}
	}
	if leaked, err := dao.GetMessageByMessageID(ctx, aMessage.MessageID); err != nil || leaked != nil {
		t.Fatalf("root lookup leaked child message: %+v, %v", leaked, err)
	}
	if leaked, err := dao.GetMessageByMessageID(aCtx, bMessage.MessageID); err != nil || leaked != nil {
		t.Fatalf("sibling lookup leaked message: %+v, %v", leaked, err)
	}
	if err := dao.DeleteMessagesFromRowID(ctx, s.SessionID, rootMessage.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := dao.CountMessagesBySessionID(aCtx, s.SessionID); err != nil || count != 1 {
		t.Fatalf("root truncation removed child message: %d, %v", count, err)
	}
	if err := dao.DeleteMessagesFromRowID(aCtx, s.SessionID, aMessage.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := dao.CountMessagesBySessionID(bCtx, s.SessionID); err != nil || count != 1 {
		t.Fatalf("child truncation removed sibling message: %d, %v", count, err)
	}
	usage := &model.TokenUsage{SessionID: s.SessionID, Model: s.Model, PromptTokens: 12}
	if err := dao.CreateTokenUsage(bCtx, usage); err != nil {
		t.Fatal(err)
	}
	ledger, err := dao.ListTokenUsagesBySession(ctx, s.SessionID, 10)
	if err != nil || len(ledger) != 1 || ledger[0].AgentID != b.AgentID {
		t.Fatalf("root ledger must include attributed child cost: %+v, %v", ledger, err)
	}
	if err := dao.DeleteSession(aCtx, s.SessionID); err != nil {
		t.Fatal(err)
	}
	m, r := db.Ctx(ctx).Message, db.Ctx(ctx).AgentRun
	if count, err := m.WithContext(ctx).Where(m.SessionID.Eq(s.SessionID)).Count(); err != nil || count != 0 {
		t.Fatalf("session delete left sibling messages: %d, %v", count, err)
	}
	if count, err := r.WithContext(ctx).Where(r.SessionID.Eq(s.SessionID)).Count(); err != nil || count != 0 {
		t.Fatalf("session delete left agent records: %d, %v", count, err)
	}
}

func TestAgentRestartReconciliationPreservesTerminalResults(t *testing.T) {
	ctx := setupTestDB(t)
	s := newAgentTestSession(t, ctx)
	queued := newAgentTestRun(t, ctx, s.SessionID, "queued")
	running := newAgentTestRun(t, ctx, s.SessionID, "running")
	completed := newAgentTestRun(t, ctx, s.SessionID, "completed")
	if err := dao.UpdateAgentRun(ctx, running.AgentID, map[string]interface{}{"status": "running"}); err != nil {
		t.Fatal(err)
	}
	if err := dao.UpdateAgentRun(ctx, completed.AgentID, map[string]interface{}{"status": "completed", "result": "keep"}); err != nil {
		t.Fatal(err)
	}
	// Startup reconciliation is global, so always roll it back in the test to
	// preserve any other developer-owned executions in this local database.
	rollback := stderrors.New("rollback restart fixture")
	err := db.Transition(ctx, func(tx context.Context) error {
		if err := dao.InterruptAgentRuns(tx); err != nil {
			return err
		}
		for _, id := range []string{queued.AgentID, running.AgentID} {
			row, err := dao.GetAgentRun(tx, s.SessionID, id)
			if err != nil {
				return err
			}
			if row.Status != "interrupted" || row.FinishedAt == nil {
				t.Errorf("unfinished child was not interrupted: %+v", row)
			}
		}
		row, err := dao.GetAgentRun(tx, s.SessionID, completed.AgentID)
		if err != nil {
			return err
		}
		if row.Status != "completed" || row.Result != "keep" {
			t.Errorf("restart changed terminal child: %+v", row)
		}
		return rollback
	})
	if !stderrors.Is(err, rollback) {
		t.Fatal(err)
	}
}
