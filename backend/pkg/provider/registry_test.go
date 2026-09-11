package provider

import (
	"context"
	"testing"
)

type mockProvider struct {
	name string
}

func (m *mockProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, <-chan error) {
	return nil, nil
}

func TestModelRouting(t *testing.T) {
	minimaxP := &mockProvider{name: "minimax"}
	glmP := &mockProvider{name: "glm"}

	RegisterProvider("minimax", minimaxP)
	RegisterProvider("glm", glmP)

	RegisterModelRoute("MiniMax-Text-01", "minimax")
	RegisterModelRoute("MiniMax-M3", "minimax")
	RegisterModelRoute("GLM-5.3", "glm")
	RegisterModelRoute("GLM-5.3-Flash", "glm")

	// 1. 精确匹配
	p, ok := GetProviderForModel("MiniMax-Text-01")
	if !ok || p != minimaxP {
		t.Errorf("expected minimax provider for MiniMax-Text-01, got %v", p)
	}

	p, ok = GetProviderForModel("GLM-5.3")
	if !ok || p != glmP {
		t.Errorf("expected glm provider for GLM-5.3, got %v", p)
	}

	// 2. 大小写不敏感匹配
	p, ok = GetProviderForModel("glm-5.3")
	if !ok || p != glmP {
		t.Errorf("expected glm provider for lowercase glm-5.3, got %v", p)
	}

	p, ok = GetProviderForModel("minimax-m3")
	if !ok || p != minimaxP {
		t.Errorf("expected minimax provider for lowercase minimax-m3, got %v", p)
	}

	// 3. 前缀推断
	p, ok = GetProviderForModel("GLM-Future-99")
	if !ok || p != glmP {
		t.Errorf("expected glm provider for GLM prefix, got %v", p)
	}

	p, ok = GetProviderForModel("minimax-chat-ultra")
	if !ok || p != minimaxP {
		t.Errorf("expected minimax provider for minimax prefix, got %v", p)
	}
}
