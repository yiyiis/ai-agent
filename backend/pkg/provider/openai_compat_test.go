package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAICompatProvider_StreamChat(t *testing.T) {
	// 构造模拟的 OpenAI 兼容 SSE 服务
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []string{
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"思考中..."}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"你好！"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"有什么可以帮您？"}}]}`,
			`data: {"usage":{"prompt_tokens":15,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":4}}}`,
			`data: [DONE]`,
		}

		for _, evt := range events {
			fmt.Fprintf(w, "%s\n\n", evt)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer server.Close()

	p := NewOpenAICompatProvider(server.URL, "mock-api-key")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	chunkChan, errChan := p.StreamChat(ctx, &ChatRequest{
		Model: "test-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "hello"},
		},
	})

	var receivedContent string
	var receivedReasoning string
	var receivedUsage *Usage

	for {
		select {
		case err, ok := <-errChan:
			if ok && err != nil {
				t.Fatalf("StreamChat unexpected error: %v", err)
			}
		case chunk, ok := <-chunkChan:
			if !ok {
				goto DONE
			}
			if chunk.Content != "" {
				receivedContent += chunk.Content
			}
			if chunk.ReasoningContent != "" {
				receivedReasoning += chunk.ReasoningContent
			}
			if chunk.Usage != nil {
				receivedUsage = chunk.Usage
			}
		case <-ctx.Done():
			t.Fatal("test timed out waiting for chunks")
		}
	}

DONE:
	if receivedContent != "你好！有什么可以帮您？" {
		t.Errorf("expected content '你好！有什么可以帮您？', got '%s'", receivedContent)
	}
	if receivedReasoning != "思考中..." {
		t.Errorf("expected reasoning '思考中...', got '%s'", receivedReasoning)
	}
	if receivedUsage == nil || receivedUsage.PromptTokens != 15 || receivedUsage.CompletionTokens != 8 {
		t.Errorf("unexpected usage: %+v", receivedUsage)
	}
}
