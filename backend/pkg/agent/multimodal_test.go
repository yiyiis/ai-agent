package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backend/dal/model"
	"backend/pkg/provider"
	"backend/pkg/storage"
)

func TestIsImageAttachment(t *testing.T) {
	tests := []struct {
		filename    string
		contentType string
		want        bool
	}{
		{"photo.png", "image/png", true},
		{"photo.jpg", "", true},
		{"photo.JPEG", "application/octet-stream", true},
		{"photo.webp", "image/webp", true},
		{"diagram.svg", "", true},
		{"data.csv", "text/csv", false},
		{"doc.pdf", "application/pdf", false},
		{"code.py", "text/x-python", false},
		{"unknown.bin", "image/png", true},
		{"unknown.bin", "application/octet-stream", false},
	}

	for _, tt := range tests {
		att := Attachment{Filename: tt.filename, ContentType: tt.contentType}
		if got := isImageAttachment(att); got != tt.want {
			t.Errorf("isImageAttachment(%+v) = %v, want %v", att, got, tt.want)
		}
	}
}

func TestResolveAttachmentImageURL(t *testing.T) {
	ctx := context.Background()

	// 1. Data URL 原样返回
	rawURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	got, err := resolveAttachmentImageURL(ctx, Attachment{URL: rawURL, Filename: "dot.png"})
	if err != nil || got != rawURL {
		t.Fatalf("resolve data URL failed: got %s, err %v", got, err)
	}

	// 2. 本地驱动 /api/uploads/ 转换为 base64 data URL
	tmpDir := t.TempDir()
	origLocalRoot := storage.LocalRoot
	storage.LocalRoot = tmpDir
	defer func() { storage.LocalRoot = origLocalRoot }()

	testContent := []byte("fake-image-bytes-12345")
	testFilename := "test_upload_pic.png"
	if err := os.WriteFile(filepath.Join(tmpDir, testFilename), testContent, 0o644); err != nil {
		t.Fatal(err)
	}

	att := Attachment{
		URL:         "/api/uploads/" + testFilename,
		Filename:    "my_chart.png",
		ContentType: "image/png",
	}
	gotLocal, err := resolveAttachmentImageURL(ctx, att)
	if err != nil {
		t.Fatalf("resolve local upload failed: %v", err)
	}
	expectedPrefix := "data:image/png;base64,"
	if !strings.HasPrefix(gotLocal, expectedPrefix) {
		t.Fatalf("expected prefix %s, got %s", expectedPrefix, gotLocal)
	}
	b64Data := strings.TrimPrefix(gotLocal, expectedPrefix)
	decoded, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil || string(decoded) != string(testContent) {
		t.Fatalf("decoded data mismatch: got %s, want %s", string(decoded), string(testContent))
	}

	// 3. 外部公网 URL 原样返回
	publicURL := "https://cdn.example.com/photos/cat.jpg"
	gotPublic, err := resolveAttachmentImageURL(ctx, Attachment{URL: publicURL, Filename: "cat.jpg"})
	if err != nil || gotPublic != publicURL {
		t.Fatalf("resolve public URL failed: got %s, err %v", gotPublic, err)
	}
}

func TestBuildMultimodalUserMessage(t *testing.T) {
	ctx := context.Background()

	// 1. 仅文本，无附件
	msg1 := buildMultimodalUserMessage(ctx, "你好", nil, nil)
	if msg1.Content != "你好" || len(msg1.ContentParts) != 0 {
		t.Fatalf("expected pure text message, got %+v", msg1)
	}

	// 2. 文本 + 图片附件
	dataURL := "data:image/png;base64,AAAA"
	atts := []Attachment{
		{URL: dataURL, Filename: "sample.png", ContentType: "image/png"},
	}
	msg2 := buildMultimodalUserMessage(ctx, "分析这张图", atts, nil)
	if len(msg2.ContentParts) != 2 {
		t.Fatalf("expected 2 parts (text + image), got %d parts", len(msg2.ContentParts))
	}
	if msg2.ContentParts[0].Type != string(provider.ContentPartTypeText) || !strings.Contains(msg2.ContentParts[0].Text, "分析这张图") {
		t.Errorf("part 0 mismatch: %+v", msg2.ContentParts[0])
	}
	if msg2.ContentParts[1].Type != string(provider.ContentPartTypeImageURL) || msg2.ContentParts[1].ImageURL.URL != dataURL {
		t.Errorf("part 1 mismatch: %+v", msg2.ContentParts[1])
	}
	if msg2.ContentParts[1].ImageURL.Detail != "" {
		t.Errorf("expected empty detail to avoid upstream 400 rejection, got %s", msg2.ContentParts[1].ImageURL.Detail)
	}
	if b, err := json.Marshal(msg2); err != nil || strings.Contains(string(b), `"detail"`) {
		t.Errorf("expected serialized json not to contain 'detail', got %s", string(b))
	}

	// 3. 仅图片，无文本
	msg3 := buildMultimodalUserMessage(ctx, "", atts, nil)
	if len(msg3.ContentParts) == 0 {
		t.Fatalf("expected content parts for image-only message")
	}
	hasImg := false
	for _, p := range msg3.ContentParts {
		if p.Type == string(provider.ContentPartTypeImageURL) {
			hasImg = true
		}
	}
	if !hasImg {
		t.Fatalf("image part not found in image-only message")
	}
	if msg3.Content == "" {
		t.Fatalf("expected fallback text in msg3.Content")
	}

	// 4. 纯非图片附件（如 csv）不应产生 image_url part
	csvAtts := []Attachment{
		{URL: "https://example.com/data.csv", Filename: "data.csv", ContentType: "text/csv"},
	}
	msg4 := buildMultimodalUserMessage(ctx, "请分析表格", csvAtts, nil)
	if len(msg4.ContentParts) != 0 {
		t.Fatalf("expected 0 content parts for non-image attachments, got %d", len(msg4.ContentParts))
	}
	if !strings.Contains(msg4.Content, "data.csv") {
		t.Fatalf("expected prompt to contain attachment note, got %s", msg4.Content)
	}
}

func TestBuildContext_MultimodalHistory(t *testing.T) {
	ctx := context.Background()
	session := &model.Session{SessionID: "sess-mm", Model: "gpt-4o"}

	dataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	attsJSON, _ := json.Marshal([]Attachment{
		{URL: dataURL, Filename: "cat.png", ContentType: "image/png"},
	})
	attsStr := string(attsJSON)

	history := []model.Message{
		{Role: "user", Content: "看下这只猫", Attachments: &attsStr},
		{Role: "assistant", Content: "这是一只可爱的橘猫。"},
	}

	msgs := buildContext(ctx, session, history)

	// msgs[0] = system, msgs[1] = user, msgs[2] = assistant
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	userMsg := msgs[1]
	if userMsg.Role != "user" {
		t.Fatalf("expected user role, got %s", userMsg.Role)
	}
	if len(userMsg.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts in history user message, got %d", len(userMsg.ContentParts))
	}
	if userMsg.ContentParts[0].Type != string(provider.ContentPartTypeText) || userMsg.ContentParts[0].Text != "看下这只猫" {
		t.Errorf("history text part mismatch: %+v", userMsg.ContentParts[0])
	}
	if userMsg.ContentParts[1].Type != string(provider.ContentPartTypeImageURL) || userMsg.ContentParts[1].ImageURL.URL != dataURL {
		t.Errorf("history image part mismatch: %+v", userMsg.ContentParts[1])
	}
}
