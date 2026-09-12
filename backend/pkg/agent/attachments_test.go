package agent

import (
	"os"
	"path/filepath"
	"testing"

	"backend/pkg/tools"
)

// 重定向上传目录与工作区根到临时目录，避免污染运行目录
func redirectDirs(t *testing.T) (uploadsRoot, wsRoot string) {
	t.Helper()
	root := t.TempDir()
	uploadsRoot = filepath.Join(root, "uploads")
	wsRoot = filepath.Join(root, "workspace")
	if err := os.MkdirAll(uploadsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldUploads, oldWS := UploadsDir, tools.WorkspaceRoot
	UploadsDir, tools.WorkspaceRoot = uploadsRoot, wsRoot
	t.Cleanup(func() { UploadsDir, tools.WorkspaceRoot = oldUploads, oldWS })
	return uploadsRoot, wsRoot
}

func TestMaterializeAttachments(t *testing.T) {
	uploadsRoot, wsRoot := redirectDirs(t)

	if err := os.WriteFile(filepath.Join(uploadsRoot, "ab12cd34_README.md"), []byte("hello readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	placed := materializeAttachments("sess-1", []Attachment{
		{URL: "/api/uploads/ab12cd34_README.md", Filename: "README.md", ContentType: "text/markdown"},
		{URL: "https://cdn.example.com/x.png", Filename: "x.png"},  // 非本地上传 url，跳过
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
