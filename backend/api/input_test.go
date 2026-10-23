package api

import (
	"context"
	"testing"

	"backend/constants"
	"backend/dal/model"
	"backend/internal/inputtestdb"
	"backend/pkg/agent"
	"backend/pkg/human"
	"backend/pkg/jwt"
)

func TestInputEndpointRequiresWritePermission(t *testing.T) {
	calls := `[{"id":"q","type":"function","function":{"name":"request_input","arguments":"{\"question\":\"格式？\"}"}}]`
	ctx, store := inputtestdb.New(t, model.Message{Role: "user"}, model.Message{Role: "assistant", MessageID: "a", ToolCalls: &calls})
	req := &AnswerInputReq{ID: "session", MessageID: "a", CallID: "q", Action: "answer", Answer: "PDF"}
	if _, err := AnswerSessionInput(ctx, req); err == nil {
		t.Fatal("unauthenticated answer accepted")
	}
	for _, claims := range []jwt.TokenClaims{
		{UserID: 8, CompanyID: 10, UserType: constants.UserType(3)},
		{UserID: 8, CompanyID: 9, UserType: constants.Manager},
	} {
		auth := context.WithValue(ctx, "tokenClaims", claims)
		if _, err := AnswerSessionInput(auth, req); err == nil {
			t.Fatal("read-only/non-owner answer accepted")
		}
		if _, err := StopSessionTurn(auth, &StopTurnReq{ID: "session"}); err == nil {
			t.Fatal("read-only/non-owner stop accepted")
		}
	}
	if len(store.Snapshot()) != 2 {
		t.Fatal("unauthorized requests changed messages")
	}
}

func TestInputEndpointRetriesReuseLiveRun(t *testing.T) {
	old := turnRegistry
	turnRegistry = agent.NewTurnRegistry()
	defer func() { turnRegistry.Abort("session"); turnRegistry = old }()
	turn, err := turnRegistry.Start("session", func(ctx context.Context, emit func(agent.Event)) error {
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := `[{"id":"q","type":"function","function":{"name":"request_input","arguments":"{\"question\":\"格式？\"}"}}]`
	ctx, store := inputtestdb.New(t, model.Message{Role: "user"}, model.Message{Role: "assistant", MessageID: "a", ToolCalls: &calls})
	ctx = context.WithValue(ctx, "tokenClaims", jwt.TokenClaims{UserID: 7, CompanyID: 9, UserType: constants.UserType(3)})
	attach := &AttachStreamReq{ID: "session", FromSeq: 999, StreamID: "previous-process"}
	if _, err := AttachSessionStream(ctx, attach); err != nil || attach.FromSeq != 0 || turn.StreamID() == "" {
		t.Fatalf("restarted stream must reset stale cursor: %v %+v", err, attach)
	}
	req := &AnswerInputReq{ID: "session", MessageID: "a", CallID: "q", Action: "answer", Answer: "PDF"}
	for i := 0; i < 2; i++ {
		if _, err := AnswerSessionInput(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if turnRegistry.Get("session") != turn || len(store.Snapshot()) != 3 {
		t.Fatal("retry must not create a new worker or duplicate the answer")
	}
	req.Answer = "Word"
	if _, err := AnswerSessionInput(ctx, req); err == nil {
		t.Fatal("conflicting answer accepted")
	}
	if _, err := StopSessionTurn(ctx, &StopTurnReq{ID: "session"}); err != nil {
		t.Fatal(err)
	}
	if human.Active(store.Snapshot()) != nil {
		t.Fatal("stop must persist terminal state")
	}
}

func TestInputSSEPayload(t *testing.T) {
	request := &human.Request{MessageID: "a", CallID: "q", Question: human.Question{Question: "格式？", Options: []string{"PDF"}}}
	payload := ssePayload(agent.Event{Type: "input_required", Input: request})
	if payload["request"] != request || payload["type"] != "input_required" {
		t.Fatalf("missing durable identity: %+v", payload)
	}
}
