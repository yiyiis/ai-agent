package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAICompatProvider struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func NewOpenAICompatProvider(baseURL, apiKey string) *OpenAICompatProvider {
	return &OpenAICompatProvider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

type openAIRequest struct {
	Model         string        `json:"model"`
	Messages      []ChatMessage `json:"messages"`
	Stream        bool          `json:"stream"`
	MaxTokens     int           `json:"max_tokens,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Tools      []interface{} `json:"tools,omitempty"`
	ToolChoice interface{}   `json:"tool_choice,omitempty"`
}

type openAIChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (p *OpenAICompatProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkChan := make(chan StreamChunk, 64)
	errChan := make(chan error, 1)

	go func() {
		defer close(chunkChan)
		defer close(errChan)

		maxTokens := req.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 8192
		}

		payload := openAIRequest{
			Model:     req.Model,
			Messages:  req.Messages,
			Stream:    true,
			MaxTokens: maxTokens,
			StreamOptions: &struct {
				IncludeUsage bool `json:"include_usage"`
			}{
				IncludeUsage: true,
			},
		}
		if len(req.Tools) > 0 {
			payload.Tools = req.Tools
			payload.ToolChoice = "auto"
		}

		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			errChan <- fmt.Errorf("序列化请求体失败: %w", err)
			return
		}

		endpoint := fmt.Sprintf("%s/chat/completions", p.BaseURL)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			errChan <- fmt.Errorf("创建 HTTP 请求失败: %w", err)
			return
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if p.APIKey != "" {
			httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.APIKey))
		}

		resp, err := p.HTTPClient.Do(httpReq)
		if err != nil {
			errChan <- fmt.Errorf("上游模型接口调用异常: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBytes, _ := io.ReadAll(resp.Body)
			errChan <- fmt.Errorf("上游接口响应异常 HTTP %d: %s", resp.StatusCode, string(respBytes))
			return
		}

		reader := bufio.NewReader(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					errChan <- fmt.Errorf("读取 SSE 响应流异常: %w", err)
				}
				return
			}

			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, ":") {
				continue // 忽略心跳或空行
			}

			if !strings.HasPrefix(line, "data:") {
				continue
			}

			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataStr == "[DONE]" {
				return
			}

			var chunk openAIChunk
			if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
				continue // 容错非 JSON 格式行
			}

			var normalizedChunk StreamChunk

			// 处理 Usage 统计
			if chunk.Usage != nil {
				normalizedChunk.Usage = &Usage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					CachedTokens:     chunk.Usage.PromptTokensDetails.CachedTokens,
					ReasoningTokens:  chunk.Usage.CompletionTokensDetails.ReasoningTokens,
					ContextTokens:    chunk.Usage.PromptTokens,
				}
			}

			if len(chunk.Choices) > 0 {
				choice := chunk.Choices[0]
				normalizedChunk.Content = choice.Delta.Content
				normalizedChunk.ReasoningContent = choice.Delta.ReasoningContent
				normalizedChunk.FinishReason = choice.FinishReason

				if len(choice.Delta.ToolCalls) > 0 {
					for _, tc := range choice.Delta.ToolCalls {
						normalizedChunk.ToolCalls = append(normalizedChunk.ToolCalls, ToolCallDelta{
							Index:     tc.Index,
							ID:        tc.ID,
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						})
					}
				}
			}

			if normalizedChunk.Content != "" || normalizedChunk.ReasoningContent != "" || len(normalizedChunk.ToolCalls) > 0 || normalizedChunk.FinishReason != "" || normalizedChunk.Usage != nil {
				chunkChan <- normalizedChunk
			}
		}
	}()

	return chunkChan, errChan
}
