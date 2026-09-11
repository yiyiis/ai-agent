package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/jwt"
	"backend/pkg/provider"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type SendMessageReq struct {
	Content string `json:"content" binding:"required"`
}

// SendMessageStream 处理 POST /api/sessions/:id/messages 并输出 SSE 实时流
func SendMessageStream(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id 不能为空"})
		return
	}

	var req SendMessageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	ctx := c.Request.Context()
	identity, err := jwt.GetIdentityFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录或登录态已失效"})
		return
	}

	// 1. 查询目标会话并严格实施用户隔离
	session, err := dao.GetSessionBySessionID(ctx, sessionID)
	if err != nil || session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	if session.UserID != identity.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	// 2. 持久化当前用户消息
	userMsgID := uuid.New().String()
	userMsg := &model.Message{
		MessageID: userMsgID,
		SessionID: sessionID,
		Role:      "user",
		Content:   req.Content,
		CreatedAt: time.Now(),
	}
	if err := dao.CreateMessage(ctx, userMsg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存用户消息失败: " + err.Error()})
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

	// 4. 推送用户消息确认事件
	writeSSEEvent(gin.H{
		"type": "user_message_id",
		"id":   userMsgID,
	})

	// 5. 组装历史上下文消息
	historyMsgs, err := dao.ListMessagesBySessionID(ctx, sessionID)
	if err != nil {
		writeSSEEvent(gin.H{"type": "error", "message": "读取历史上下文失败"})
		return
	}

	chatMessages := make([]provider.ChatMessage, 0, len(historyMsgs)+1)
	if session.SystemPrompt != "" {
		chatMessages = append(chatMessages, provider.ChatMessage{
			Role:    "system",
			Content: session.SystemPrompt,
		})
	}
	for _, m := range historyMsgs {
		chatMessages = append(chatMessages, provider.ChatMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	// 6. 获取模型 Provider
	p, ok := provider.GetProviderForModel(session.Model)
	if !ok {
		writeSSEEvent(gin.H{"type": "error", "message": "未找到匹配的模型 Provider"})
		return
	}

	chatReq := &provider.ChatRequest{
		Model:     session.Model,
		Messages:  chatMessages,
		MaxTokens: 8192,
	}

	chunkChan, errChan := p.StreamChat(ctx, chatReq)

	var fullContent strings.Builder
	var lastUsage *provider.Usage

	// 7. 实时消费并转发 SSE 流
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errChan:
			if ok && err != nil {
				writeSSEEvent(gin.H{"type": "error", "message": err.Error()})
				return
			}
		case chunk, ok := <-chunkChan:
			if !ok {
				// 流式结束，持久化助手完整消息
				asstMsgID := uuid.New().String()
				asstMsg := &model.Message{
					MessageID: asstMsgID,
					SessionID: sessionID,
					Role:      "assistant",
					Content:   fullContent.String(),
					CreatedAt: time.Now(),
				}
				_ = dao.CreateMessage(ctx, asstMsg)

				// 首次对话自动提取并更新会话标题
				if session.Title == "新会话" || session.Title == "" {
					title := strings.TrimSpace(req.Content)
					r := []rune(title)
					if len(r) > 20 {
						title = string(r[:20]) + "..."
					}
					_ = dao.UpdateSessionTitle(ctx, sessionID, title)
				}

				doneEvent := gin.H{
					"type": "done",
					"id":   asstMsgID,
				}
				if lastUsage != nil {
					doneEvent["usage"] = lastUsage
				}
				writeSSEEvent(doneEvent)
				return
			}

			if chunk.Usage != nil {
				lastUsage = chunk.Usage
			}

			if chunk.Content != "" || chunk.ReasoningContent != "" {
				deltaEvent := gin.H{"type": "delta"}
				if chunk.Content != "" {
					fullContent.WriteString(chunk.Content)
					deltaEvent["content"] = chunk.Content
				}
				if chunk.ReasoningContent != "" {
					deltaEvent["reasoning"] = chunk.ReasoningContent
				}
				writeSSEEvent(deltaEvent)
			}
		}
	}
}
