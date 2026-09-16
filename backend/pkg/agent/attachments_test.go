package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"backend/dal/model"
	"backend/pkg/sandbox"
	"backend/pkg/storage"
)

// 重定向上传目录与沙箱工作区根到临时目录，避免污染运行目录
func redirectDirs(t *testing.T) (uploadsRoot, wsRoot string) {
	t.Helper()
	root := t.TempDir()
	uploadsRoot = filepath.Join(root, "uploads")
	wsRoot = filepath.Join(root, "workspace")
	if err := os.MkdirAll(uploadsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldUploads := storage.LocalRoot
	storage.LocalRoot = uploadsRoot
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: wsRoot})
	t.Cleanup(func() {
		storage.LocalRoot = oldUploads
		sandbox.Init(sandbox.Config{Driver: "local"}) // 恢复默认根
	})
	return uploadsRoot, wsRoot
}

func TestMaterializeAttachments(t *testing.T) {
	uploadsRoot, wsRoot := redirectDirs(t)

	if err := os.WriteFile(filepath.Join(uploadsRoot, "ab12cd34_README.md"), []byte("hello readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	placed := materializeAttachments(context.Background(), "sess-1", []Attachment{
		{URL: "/api/uploads/ab12cd34_README.md", Filename: "README.md", ContentType: "text/markdown"},
		{URL: "https://cdn.example.com/x.png", Filename: "x.png"},  // 无法解析来源，跳过
		{URL: "/api/uploads/missing.bin", Filename: "missing.bin"}, // 源文件不存在，跳过
	})
	if len(placed) != 1 || placed[0].Filename != "README.md" {
		t.Fatalf("placed = %+v, 期望只有 1 个 README.md", placed)
	}
	data, err := os.ReadFile(filepath.Join(wsRoot, "sess-1", "README.md"))
	if err != nil || string(data) != "hello readme" {
		t.Fatalf("工作区文件内容不符: %q, err=%v", string(data), err)
	}
}

// TestMaterializeAttachmentsFromCOS 对象存储直传的附件（公网 URL）应下载回填工作区
func TestMaterializeAttachmentsFromCOS(t *testing.T) {
	_, wsRoot := redirectDirs(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ab12cd34_report.md" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("# cloud doc"))
	}))
	t.Cleanup(srv.Close)

	placed := materializeAttachments(context.Background(), "sess-2", []Attachment{
		{URL: srv.URL + "/ab12cd34_report.md", Filename: "报告 v1.md"},
	})
	if len(placed) != 1 || placed[0].Filename != "报告 v1.md" {
		t.Fatalf("placed = %+v", placed)
	}
	data, err := os.ReadFile(filepath.Join(wsRoot, "sess-2", "报告 v1.md"))
	if err != nil || string(data) != "# cloud doc" {
		t.Fatalf("COS 附件未落工作区: %q, err=%v", string(data), err)
	}
}

// TestCollectAttachments 历史 + 当轮附件汇总按 URL 去重（ephemeral 工作区每轮重物化的输入）
func TestCollectAttachments(t *testing.T) {
	history := []model.Message{
		{Attachments: strPtrOrNil(`[{"url":"https://cos/a.md","filename":"a.md"}]`)},
		{Attachments: strPtrOrNil(`[{"url":"/api/uploads/x.bin","filename":"x.bin"},{"url":"https://cos/a.md","filename":"dup.md"}]`)},
		{}, // 无附件消息跳过
	}
	current := []Attachment{{URL: "https://cos/b.md", Filename: "b.md"}}

	got := collectAttachments(history, current)
	if len(got) != 3 {
		t.Fatalf("dedupe wrong: %+v", got)
	}
	if got[0].URL != "https://cos/a.md" || got[1].URL != "/api/uploads/x.bin" || got[2].URL != "https://cos/b.md" {
		t.Fatalf("order/content wrong: %+v", got)
	}
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func TestWorkspaceName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"README.md", "README.md"},
		{"../etc/passwd", "passwd"},
		{"报告:v1.docx", "报告_v1.docx"}, // Windows 非法字符替换
		{"a\nb.txt", "a_b.txt"},         // 控制符替换
		{"..", ""},
		{".", ""},
		{"", ""},
		{"  ", ""}, // TrimSpace 后为空
	}
	for _, c := range cases {
		if got := workspaceName(c.in); got != c.want {
			t.Errorf("workspaceName(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}
