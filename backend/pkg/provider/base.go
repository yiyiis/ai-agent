package provider

import (
	"context"
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

// ChatMessage 标准化提示词消息
type ChatMessage struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	ToolCalls  interface{} `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Name       string      `json:"name,omitempty"`
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
