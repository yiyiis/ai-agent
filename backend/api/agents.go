package api

import (
	"context"
	"encoding/json"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agentctx"
	"backend/pkg/errors"
	"backend/pkg/jwt"
)

type ListSessionAgentsReq struct {
	ID string `uri:"id"`
}
type GetSessionAgentReq struct {
	ID      string `uri:"id"`
	AgentID string `uri:"agent_id"`
}
type SessionAgentOut struct {
	Agent    *model.AgentRun `json:"agent"`
	Messages []MessageOut    `json:"messages"`
}

func authorizeAgentRead(ctx context.Context, sessionID string) error {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return errors.NewMsg("登录态已失效")
	}
	_, err = loadOwnedSession(ctx, identity, sessionID)
	return err
}

func ListSessionAgents(ctx context.Context, req *ListSessionAgentsReq) ([]*model.AgentRun, error) {
	if err := authorizeAgentRead(ctx, req.ID); err != nil {
		return nil, err
	}
	return dao.ListAgentRuns(ctx, req.ID)
}

func GetSessionAgent(ctx context.Context, req *GetSessionAgentReq) (*SessionAgentOut, error) {
	if err := authorizeAgentRead(ctx, req.ID); err != nil {
		return nil, err
	}
	run, err := dao.GetAgentRun(ctx, req.ID, req.AgentID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.NewMsg("Agent 不存在")
	}
	messages, err := dao.ListMessagesBySessionID(agentctx.WithAgentID(ctx, run.AgentID), req.ID)
	if err != nil {
		return nil, err
	}
	out := &SessionAgentOut{Agent: run, Messages: make([]MessageOut, 0, len(messages))}
	for _, m := range messages {
		item := MessageOut{ID: m.MessageID, Role: m.Role, Content: m.Content, Reasoning: m.Reasoning,
			ToolCallID: m.ToolCallID, Name: m.Name, CreatedAt: m.CreatedAt.Format(time.RFC3339)}
		if m.ToolCalls != nil {
			item.ToolCalls = json.RawMessage(*m.ToolCalls)
		}
		if m.Attachments != nil {
			item.Attachments = json.RawMessage(*m.Attachments)
		}
		out.Messages = append(out.Messages, item)
	}
	return out, nil
}
