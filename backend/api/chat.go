package api

import (
	"context"
	"encoding/json"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agent"
	"backend/pkg/apiwarp"
	"backend/pkg/db"
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

// turnRegistry 进程内会话级 Turn 注册表（阶段三：执行与连接解耦）
var turnRegistry = agent.DefaultRegistry

// startTurn 启动会话的后台 Turn 并返回订阅者视图：
// 互斥拒绝（已有在跑轮次）发生在流开始前，走标准信封错误；
// workload 在后台 Goroutine 中执行——脱离请求生命周期，需显式注入 DB 连接，
// 其内的历史加载与截断也因此天然与其他轮次互斥。
func startTurn(session *model.Session, build func(ctx context.Context) (history []model.Message, content string, attachments []agent.Attachment, err error)) (*apiwarp.SSEData, error) {
	p, ok := provider.GetProviderForModel(session.Model)
	if !ok {
		return nil, errors.NewMsg("未找到匹配的模型 Provider")
	}
	turn, err := turnRegistry.Start(session.SessionID, func(ctx context.Context, emit func(agent.Event)) error {
		ctx = db.WithContext(ctx)
		history, content, attachments, err := build(ctx)
		if err != nil {
			return err
		}
		return agent.Run(ctx, agent.Deps{Provider: p}, session, history, content, attachments, emit)
	})
	if err != nil {
		return nil, errors.NewMsg("该会话已有正在进行的回答，请等待完成或先停止")
	}
	return relaySSE(turn, 0), nil
}

// relaySSE 订阅者模式的 SSE 装配：HTTP 连接只是"旁听"后台 Turn 事件流，
// 断连仅结束本次订阅，Turn 照常执行与落库；帧携带 id: {seq} 游标。
func relaySSE(turn *agent.Turn, fromSeq int) *apiwarp.SSEData {
	return &apiwarp.SSEData{Stream: func(sctx context.Context, emit func(payload any)) error {
		turnRegistry.Follow(sctx, turn, fromSeq,
			func(seq int, ev agent.Event) {
				emit(apiwarp.SSEFrame{ID: int64(seq), Payload: ssePayload(ev)})
			},
			func() { emit(apiwarp.SSEPing{}) })
		return nil
	}}
}

// SendMessageStream 处理 POST /api/sessions/:id/messages 并输出 SSE 实时流。
// 流开始前的失败（登录态/会话隔离/Provider 缺失/并发互斥）走标准信封错误；
// 流开始后由后台 Turn 驱动 ReAct 循环，协议事件经 ssePayload 转写为 SSE 帧。
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

	// 历史在后台 workload 内加载（用户消息由 agent 落库后自行装配进循环）
	return startTurn(session, func(ctx context.Context) ([]model.Message, string, []agent.Attachment, error) {
		history, err := dao.ListMessagesBySessionID(ctx, req.ID)
		return history, req.Content, req.Attachments, err
	})
}

type RegenerateMessageReq struct {
	SessionID string `uri:"id"`
}

// RegenerateLastMessage 重新生成会话中最新一轮的回答 (POST /api/sessions/:id/messages/regenerate)：
// 找到该会话中最后一条 user 提问，将其后的所有消息（含被中断或已完成的 assistant 与 tool 记录）截断，
// 并以该条提问的内容和附件重新开启一轮流式回答。截断发生在后台 workload 内，与在跑轮次天然互斥。
func RegenerateLastMessage(ctx context.Context, req *RegenerateMessageReq) (*apiwarp.SSEData, error) {
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

	msgs, err := dao.ListMessagesBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	// 逆序查找最后一条 user 消息
	var lastUserMsg *model.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUserMsg = &msgs[i]
			break
		}
	}
	if lastUserMsg == nil {
		return nil, errors.NewMsg("未找到可重新生成的提问")
	}

	var attachments []agent.Attachment
	if lastUserMsg.Attachments != nil && *lastUserMsg.Attachments != "" {
		_ = json.Unmarshal([]byte(*lastUserMsg.Attachments), &attachments)
	}

	// 截断在 workload 内执行：被重新生成的提问及其后全部记录删除（Run 内部会重新落库该提问）
	return startTurn(session, func(ctx context.Context) ([]model.Message, string, []agent.Attachment, error) {
		if err := dao.DeleteMessagesFromRowID(ctx, req.SessionID, lastUserMsg.ID); err != nil {
			return nil, "", nil, err
		}
		history, err := dao.ListMessagesBySessionID(ctx, req.SessionID)
		return history, lastUserMsg.Content, attachments, err
	})
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

	// 截断在 workload 内执行：被编辑的提问及其后全部记录删除
	return startTurn(session, func(ctx context.Context) ([]model.Message, string, []agent.Attachment, error) {
		if err := dao.DeleteMessagesFromRowID(ctx, req.SessionID, msg.ID); err != nil {
			return nil, "", nil, err
		}
		history, err := dao.ListMessagesBySessionID(ctx, req.SessionID)
		return history, req.Content, req.Attachments, err
	})
}

type AttachStreamReq struct {
	ID      string `uri:"id"`
	FromSeq int    `form:"from_seq"`
	// LiveOnly 仅接正在执行的轮次：已结束但仍在保留期内的 Turn 返回 204。
	// 页面加载时的探测用它，避免把刚跑完的轮次整轮重放一遍
	LiveOnly bool `form:"live_only"`
}

// AttachSessionStream 重连补播 (GET /api/sessions/:id/stream?from_seq=N)：
// 客户端刷新或断线后携带最后收到的 seq+1 重接当前轮次，从环形缓冲补播丢失事件。
// 无可接轮次（含保留期已过）时返回 204，前端据此恢复为普通空闲态。
func AttachSessionStream(ctx context.Context, req *AttachStreamReq) (any, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	session, err := dao.GetSessionBySessionID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.UserID != identity.UserID {
		return nil, errors.NewMsg("会话不存在")
	}

	turn := turnRegistry.Get(req.ID)
	if turn == nil || (req.LiveOnly && !turn.Running()) {
		return &apiwarp.NoContent{}, nil
	}
	return relaySSE(turn, req.FromSeq), nil
}

type StopTurnReq struct {
	ID string `uri:"id"`
}

// StopSessionTurn 优雅中止 (POST /api/sessions/:id/messages/stop)：
// 取消后台 Turn 的执行上下文，已生成的部分回答照常落库。
// 幂等：无可中止轮次时同样返回成功。
func StopSessionTurn(ctx context.Context, req *StopTurnReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	session, err := dao.GetSessionBySessionID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.UserID != identity.UserID {
		return nil, errors.NewMsg("会话不存在")
	}

	turnRegistry.Abort(req.ID)
	return &StatusOKResp{Status: "ok"}, nil
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
