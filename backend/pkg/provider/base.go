package provider

import (
	"context"
	"encoding/json"
	"strings"
)

// ToolCallDelta 表示单次工具调用的流式增量片段
type ToolCallDelta struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// Usage 归一化的 Token 消耗度量
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	ContextTokens    int `json:"context_tokens"`
}

// StreamChunk 归一化的模型流式响应分块
type StreamChunk struct {
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
	FinishReason     string          `json:"finish_reason,omitempty"`
	Usage            *Usage          `json:"usage,omitempty"`
}

// ContentPartType 内容块类型
type ContentPartType string

const (
	ContentPartTypeText     ContentPartType = "text"
	ContentPartTypeImageURL ContentPartType = "image_url"
)

// ImageURL 图片链接与详情
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto", "low", "high"
}

// ContentPart 多模态内容分块（与 OpenAI 协议兼容）
type ContentPart struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ChatMessage 标准化提示词消息，支持纯文本与多模态 ContentParts
type ChatMessage struct {
	Role         string        `json:"role"`
	Content      string        `json:"content"`
	ContentParts []ContentPart `json:"-"`
	ToolCalls    interface{}   `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	Name         string        `json:"name,omitempty"`
}

func (m ChatMessage) MarshalJSON() ([]byte, error) {
	type Alias ChatMessage
	if len(m.ContentParts) > 0 {
		return json.Marshal(&struct {
			Alias
			Content []ContentPart `json:"content"`
		}{
			Alias:   Alias(m),
			Content: m.ContentParts,
		})
	}
	return json.Marshal(&struct {
		Alias
		Content string `json:"content"`
	}{
		Alias:   Alias(m),
		Content: m.Content,
	})
}

func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	type Alias ChatMessage
	var raw struct {
		Alias
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = ChatMessage(raw.Alias)

	if len(raw.Content) == 0 {
		return nil
	}

	var str string
	if err := json.Unmarshal(raw.Content, &str); err == nil {
		m.Content = str
		return nil
	}

	var parts []ContentPart
	if err := json.Unmarshal(raw.Content, &parts); err == nil {
		m.ContentParts = parts
		var textParts []string
		for _, p := range parts {
			if p.Type == string(ContentPartTypeText) && p.Text != "" {
				textParts = append(textParts, p.Text)
			}
		}
		m.Content = strings.Join(textParts, "\n")
		return nil
	}

	m.Content = string(raw.Content)
	return nil
}

// ChatRequest 统一聊天请求结构
type ChatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	Tools     []interface{} `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

// Provider 模型服务提供商统一接口
type Provider interface {
	StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, <-chan error)
}
