package api

import (
	"context"
	"testing"

	"backend/constants"
	"backend/dal/model"
	"backend/internal/inputtestdb"
	"backend/pkg/agent"
	"backend/pkg/jwt"
)

func TestAgentEndpointsRequireRootReadAccess(t *testing.T) {
	ctx, _ := inputtestdb.New(t)
	for _, auth := range []context.Context{ctx,
		context.WithValue(ctx, "tokenClaims", jwt.TokenClaims{UserID: 8, CompanyID: 10, UserType: constants.UserType(3)}),
		context.WithValue(ctx, "tokenClaims", jwt.TokenClaims{UserID: 8, CompanyID: 9, UserType: constants.UserType(3)}),
	} {
		if _, err := ListSessionAgents(auth, &ListSessionAgentsReq{ID: "session"}); err == nil {
			t.Fatal("unauthorized list")
		}
		if _, err := GetSessionAgent(auth, &GetSessionAgentReq{ID: "session", AgentID: "child"}); err == nil {
			t.Fatal("unauthorized child history")
		}
	}
}

func TestChildSSEKeepsTerminalEventNested(t *testing.T) {
	for _, kind := range []string{"done", "error", "delta"} {
		payload := ssePayload(agent.Event{Type: "agent_event", AgentID: "child", ChildEvent: &agent.Event{Type: kind, Content: "child text", Error: "child failure"}})
		if payload["type"] != "agent_event" || payload["agent_id"] != "child" || payload["event"] == nil {
			t.Fatalf("lost child envelope: %+v", payload)
		}
		if payload["content"] != nil || payload["message"] != nil {
			t.Fatal("child output leaked into root event")
		}
	}
	row := &model.AgentRun{AgentID: "child", Status: "completed"}
	if ssePayload(agent.Event{Type: "agent_status", Agent: row})["agent"] != row {
		t.Fatal("missing persisted status")
	}
}
