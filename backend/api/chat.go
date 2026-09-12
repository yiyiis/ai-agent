package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"backend/dao"
	"backend/pkg/agent"
	"backend/pkg/jwt"
	"backend/pkg/provider"
	"github.com/gin-gonic/gin"
)

type SendMessageReq struct {
	Content     string             `json:"content" binding:"required"`
	Attachments []agent.Attachment `json:"attachments"`
}

// SendMessageStream 处理 POST /api/sessions/:id/messages 并输出 SSE 实时流。
// 单轮内的多轮工具调用（ReAct）由 pkg/agent 驱动，这里只负责鉴权、
// 上下文装配入口与协议事件到 SSE 的转写。
func SendMessageStream(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id 不能为空"})
		return
	}

	var req SendMessageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": failMsg(fmt.Errorf("请求参数无效: %w", err))})
		return
	}

	ctx := c.Request.Context()
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录或登录态已失效"})
		return
	}

	// 1. 查询目标会话并严格实施用户隔离
	session, err := dao.GetSessionBySessionID(ctx, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": failMsg(err)})
		return
	}
	if session == nil || session.UserID != identity.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	// 2. 历史上下文（用户消息由 agent 落库后自行装配进循环）
	history, err := dao.ListMessagesBySessionID(ctx, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": failMsg(err)})
		return
	}

	p, ok := provider.GetProviderForModel(session.Model)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "未找到匹配的模型 Provider"})
		return
	}

	// 3. 配置 SSE 响应头
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	writeSSEEvent := func(data interface{}) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(c.Writer, "data: %s\n\n", string(b))
		c.Writer.Flush()
	}

	// 4. 协议事件 → SSE（字段名与前端 StreamEvent 契约一致）
	emit := func(ev agent.Event) {
		switch ev.Type {
		case "delta":
			payload := gin.H{"type": "delta"}
			if ev.Content != "" {
				payload["content"] = ev.Content
			}
			if ev.Reasoning != "" {
				payload["reasoning"] = ev.Reasoning
			}
			writeSSEEvent(payload)
		case "tool_call_start":
			writeSSEEvent(gin.H{"type": ev.Type, "id": ev.ID, "name": ev.Name, "arguments": ev.Arguments})
		case "tool_call_result":
			writeSSEEvent(gin.H{"type": ev.Type, "id": ev.ID, "output": ev.Output})
		case "tool_call_error":
			writeSSEEvent(gin.H{"type": ev.Type, "id": ev.ID, "error": ev.Error})
		case "done":
			payload := gin.H{"type": "done", "id": ev.ID}
			if ev.Usage != nil {
				payload["usage"] = ev.Usage
			}
			writeSSEEvent(payload)
		case "error":
			writeSSEEvent(gin.H{"type": "error", "message": ev.Error})
		default:
			writeSSEEvent(gin.H{"type": ev.Type, "id": ev.ID})
		}
	}

	// 5. 驱动 ReAct 循环（阶段二：与请求同生命周期；阶段三将迁移至后台 Turn）
	_ = agent.Run(ctx, agent.Deps{Provider: p}, session, history, req.Content, req.Attachments, emit)
}
