package api

import (
	"context"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agent"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/human"
	"backend/pkg/jwt"
	"backend/pkg/provider"
)

type AnswerInputReq struct {
	ID        string `uri:"id"`
	MessageID string `uri:"mid"`
	CallID    string `uri:"cid"`
	Action    string `json:"action"` // answer | skip; stop uses the existing stop endpoint
	Answer    string `json:"answer"`
}

type AnswerInputOut struct {
	Content string `json:"content"`
}

func AnswerSessionInput(ctx context.Context, req *AnswerInputReq) (*AnswerInputOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	session, err := loadOwnedSession(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := dao.CheckSessionWriteAccess(identity, session); err != nil {
		return nil, err
	}
	result, err := dao.AnswerInput(ctx, req.ID, req.MessageID, req.CallID, req.Action, req.Answer)
	if err != nil {
		return nil, err
	}
	agent.NotifyInput(req.ID)
	if _, _, err := resumeInputTurn(ctx, session); err != nil {
		// The answer is already durable; returning an error allows an identical
		// retry to safely restart the worker when the provider becomes available.
		return nil, err
	}
	return &AnswerInputOut{Content: result.Content}, nil
}

// Only the registry owner may create a recovery worker. Concurrent HTTP
// submissions/reconnections reuse the same in-process Turn.
func resumeInputTurn(ctx context.Context, session *model.Session) (*agent.Turn, bool, error) {
	if t := turnRegistry.Get(session.SessionID); t != nil && t.Running() {
		return t, false, nil
	}
	messages, err := dao.ListMessagesBySessionID(ctx, session.SessionID)
	if err != nil {
		return nil, false, err
	}
	if human.Active(messages) == nil {
		return turnRegistry.Get(session.SessionID), false, nil
	}
	p, ok := provider.GetProviderForModel(session.Model)
	if !ok {
		return nil, false, errors.NewMsg("未找到匹配的模型 Provider；回答已保存，可稍后重试")
	}
	t, err := turnRegistry.Start(session.SessionID, func(ctx context.Context, emit func(agent.Event)) error {
		return agent.ResumeInput(db.WithContext(ctx), agent.Deps{Provider: p}, session, emit)
	})
	if err != nil {
		if current := turnRegistry.Get(session.SessionID); current != nil && current.Running() {
			return current, false, nil
		}
		return nil, false, err
	}
	return t, true, nil
}
