package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestChatMessage_MultimodalJSON(t *testing.T) {
	// 1. 纯文本消息序列化
	textMsg := ChatMessage{
		Role:    "user",
		Content: "Hello world",
	}
	b, err := json.Marshal(textMsg)
	if err != nil {
		t.Fatalf("marshal text message failed: %v", err)
	}
	if string(b) != `{"role":"user","content":"Hello world"}` {
		t.Errorf("unexpected text json: %s", string(b))
	}

	// 2. 多模态消息序列化（含图片）
	mmMsg := ChatMessage{
		Role:    "user",
		Content: "Describe this image",
		ContentParts: []ContentPart{
			{
				Type: string(ContentPartTypeText),
				Text: "Describe this image",
			},
			{
				Type: string(ContentPartTypeImageURL),
				ImageURL: &ImageURL{
					URL:    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
					Detail: "auto",
				},
			},
		},
	}
	b2, err := json.Marshal(mmMsg)
	if err != nil {
		t.Fatalf("marshal multimodal message failed: %v", err)
	}
	expectedSub := `"content":[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"data:image/png;base64,`
	if !strings.Contains(string(b2), expectedSub) {
		t.Errorf("unexpected multimodal json: %s", string(b2))
	}

	// 3. 多模态消息反序列化
	var unmarshaled ChatMessage
	if err := json.Unmarshal(b2, &unmarshaled); err != nil {
		t.Fatalf("unmarshal multimodal message failed: %v", err)
	}
	if unmarshaled.Role != "user" {
		t.Errorf("expected role 'user', got '%s'", unmarshaled.Role)
	}
	if unmarshaled.Content != "Describe this image" {
		t.Errorf("expected content 'Describe this image', got '%s'", unmarshaled.Content)
	}
	if len(unmarshaled.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(unmarshaled.ContentParts))
	}
	if unmarshaled.ContentParts[1].Type != string(ContentPartTypeImageURL) {
		t.Errorf("expected second part to be image_url, got '%s'", unmarshaled.ContentParts[1].Type)
	}
}

