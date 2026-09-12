package api

import (
	"context"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agent"
	"backend/pkg/apiwarp"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"backend/pkg/provider"
	"github.com/gin-gonic/gin"
)

type SendMessageReq struct {
	ID          string             `uri:"id"`
	Content     string             `json:"content" binding:"required"`
	Attachments []agent.Attachment `json:"attachments"`
}

// runTurnStream 装配 SSEData：以 history 为前置上下文跑一轮完整对话
func runTurnStream(ctx context.Context, session *model.Session, history []model.Message, content string, attachments []agent.Attachment) (*apiwarp.SSEData, error) {
	p, ok := provider.GetProviderForModel(session.Model)
	if !ok {
		return nil, errors.NewMsg("未找到匹配的模型 Provider")
	}
	return &apiwarp.SSEData{Stream: func(sctx context.Context, emit func(payload any)) error {
		return agent.Run(sctx, agent.Deps{Provider: p}, session, history, content, attachments,
			func(ev agent.Event) { emit(ssePayload(ev)) })
	}}, nil
}

// SendMessageStream 处理 POST /api/sessions/:id/messages 并输出 SSE 实时流。
// 流开始前的失败（登录态/会话隔离/Provider 缺失）走标准信封错误；
// 流开始后由 pkg/agent 驱动 ReAct 循环，协议事件经 ssePayload 转写为 SSE 帧。
func SendMessageStream(ctx context.Context, req *SendMessageReq) (*apiwarp.SSEData, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	// 查询目标会话并严格实施用户隔离（不区分不存在与无权，防存在性探测）
	session, err := dao.GetSessionBySessionID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.UserID != identity.UserID {
		return nil, errors.NewMsg("会话不存在")
	}

	// 历史上下文（用户消息由 agent 落库后自行装配进循环）
	history, err := dao.ListMessagesBySessionID(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	return runTurnStream(ctx, session, history, req.Content, req.Attachments)
}

type EditMessageReq struct {
	SessionID   string             `uri:"id"`
	MessageID   string             `uri:"mid"`
	Content     string             `json:"content" binding:"required"`
	Attachments []agent.Attachment `json:"attachments"`
}

// EditUserMessage 编辑任意一条历史提问并重新生成 (PUT /api/sessions/:id/messages/:mid)：
// 从该条 user 消息起整段截断（含其后的全部回答与工具记录），再以编辑后的内容重开一轮。
func EditUserMessage(ctx context.Context, req *EditMessageReq) (*apiwarp.SSEData, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	session, err := dao.GetSessionBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.UserID != identity.UserID {
		return nil, errors.NewMsg("会话不存在")
	}

	msg, err := dao.GetMessageByMessageID(ctx, req.MessageID)
	if err != nil {
		return nil, err
	}
	if msg == nil || msg.SessionID != session.SessionID || msg.Role != "user" {
		return nil, errors.NewMsg("消息不存在")
	}

	// 截断：被编辑的提问及其后全部记录删除
	if err := dao.DeleteMessagesFromRowID(ctx, req.SessionID, msg.ID); err != nil {
		return nil, err
	}

	history, err := dao.ListMessagesBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	return runTurnStream(ctx, session, history, req.Content, req.Attachments)
}

// ssePayload 协议事件 → 前端 StreamEvent 契约字段
func ssePayload(ev agent.Event) gin.H {
	switch ev.Type {
	case "delta":
		payload := gin.H{"type": "delta"}
		if ev.Content != "" {
			payload["content"] = ev.Content
		}
		if ev.Reasoning != "" {
			payload["reasoning"] = ev.Reasoning
		}
		return payload
	case "tool_call_start":
		return gin.H{"type": ev.Type, "id": ev.ID, "name": ev.Name, "arguments": ev.Arguments}
	case "tool_call_result":
		return gin.H{"type": ev.Type, "id": ev.ID, "output": ev.Output}
	case "tool_call_error":
		return gin.H{"type": ev.Type, "id": ev.ID, "error": ev.Error}
	case "done":
		payload := gin.H{"type": "done", "id": ev.ID}
		if ev.Usage != nil {
			payload["usage"] = ev.Usage
		}
		return payload
	case "error":
		return gin.H{"type": "error", "message": ev.Error}
	default:
		return gin.H{"type": ev.Type, "id": ev.ID}
	}
}
