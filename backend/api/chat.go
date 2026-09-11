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
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
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
	// 注入当前模型说明：切换模型后历史消息里可能残留旧模型的自我描述，
	// 不显式声明的话，模型会顺着历史人设错误自报身份
	modelNote := fmt.Sprintf("当前对话由模型 %s 提供支持。如果用户询问你是什么模型，如实回答自己是 %s。", session.Model, session.Model)
	systemPrompt := session.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = modelNote
	} else {
		systemPrompt += "\n\n" + modelNote
	}
	chatMessages = append(chatMessages, provider.ChatMessage{
		Role:    "system",
		Content: systemPrompt,
	})
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
	var reasoningContent strings.Builder
	var lastUsage *provider.Usage
	tf := &thinkFilter{}

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
				// 流式结束，持久化助手完整消息（含思考过程）
				asstMsgID := uuid.New().String()
				asstMsg := &model.Message{
					MessageID: asstMsgID,
					SessionID: sessionID,
					Role:      "assistant",
					Content:   fullContent.String(),
					CreatedAt: time.Now(),
				}
				if reasoningContent.Len() > 0 {
					r := reasoningContent.String()
					asstMsg.Reasoning = &r
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
						// 分离开头的 <think> 推理块：正文转发并入库，推理走 reasoning 事件
						visible, reasoning := tf.Filter(chunk.Content)
						if visible != "" {
							fullContent.WriteString(visible)
							deltaEvent["content"] = visible
						}
						if reasoning != "" {
							reasoningContent.WriteString(reasoning)
							deltaEvent["reasoning"] = reasoning
						}
					}
					if chunk.ReasoningContent != "" {
						reasoningContent.WriteString(chunk.ReasoningContent)
						if prev, ok := deltaEvent["reasoning"].(string); ok {
							deltaEvent["reasoning"] = prev + chunk.ReasoningContent
						} else {
							deltaEvent["reasoning"] = chunk.ReasoningContent
						}
					}
					if len(deltaEvent) > 1 {
						writeSSEEvent(deltaEvent)
					}
				}
		}
	}
}

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// thinkFilter 分离模型输出开头的 <think>...</think> 推理块（如 MiniMax-M3）：
// 正文作为 content 放行，推理块作为 reasoning 返回；标签拆分到多个 chunk 也能正确识别。
type thinkFilter struct {
	phase   int    // 0=判定开头是否为 think 块, 1=think 块内, 2=正文透传
	buf     string // phase 0: 待判定缓冲; phase 1: 已累积的 think 文本
	emitted int    // phase 1 中已作为 reasoning 发出的字节数
}

// Filter 输入一个增量 chunk，返回 (应展示的正文, 应展示的推理)
func (f *thinkFilter) Filter(chunk string) (content, reasoning string) {
	switch f.phase {
	case 2:
		return chunk, ""
	case 0:
		f.buf += chunk
		trimmed := strings.TrimLeft(f.buf, " \t\r\n")
		if trimmed == "" {
			f.buf = trimmed
			return "", ""
		}
		if strings.HasPrefix(trimmed, thinkOpen) { // 完整标签，进入 think 块
			f.phase, f.buf, f.emitted = 1, trimmed[len(thinkOpen):], 0
			return f.Filter("")
		}
		if strings.HasPrefix(thinkOpen, trimmed) { // 仍是不完整前缀，继续缓冲
			f.buf = trimmed
			return "", ""
		}
		f.phase = 2 // 开头不是 think 块，后续全部透传
		f.buf = ""
		return trimmed, ""
	case 1:
		f.buf += chunk
		if idx := strings.Index(f.buf, thinkClose); idx >= 0 {
			out := ""
			if idx > f.emitted {
				out = f.buf[f.emitted:idx]
			}
			rest := strings.TrimLeft(f.buf[idx+len(thinkClose):], " \t\r\n")
			f.phase, f.buf, f.emitted = 2, "", 0
			return rest, out
		}
		out := ""
		if len(f.buf) > f.emitted {
			out = f.buf[f.emitted:]
			f.emitted = len(f.buf)
		}
		return "", out
	}
	return chunk, ""
}
