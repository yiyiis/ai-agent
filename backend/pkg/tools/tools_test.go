package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := WorkspaceRoot
	WorkspaceRoot = root
	t.Cleanup(func() { WorkspaceRoot = old })
	return root
}

func writeWsFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, "sess1", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveConfinesPath(t *testing.T) {
	setupWorkspace(t)
	// 正常相对路径
	if _, err := resolve("sess1", "src/main.py"); err != nil {
		t.Fatalf("normal path rejected: %v", err)
	}
	// 越界：../ 逃逸
	for _, escape := range []string{"../secret", "a/../../etc/passwd", "..\\..\\x"} {
		if _, err := resolve("sess1", escape); err == nil {
			t.Fatalf("escape path %q should be rejected", escape)
		}
	}
	// 绝对路径拒绝
	if _, err := resolve("sess1", "/etc/passwd"); err == nil {
		t.Fatal("absolute path should be rejected")
	}
}

func TestReadFilePagingAndMissing(t *testing.T) {
	root := setupWorkspace(t)
	writeWsFile(t, root, "data.txt", "l1\nl2\nl3\nl4\nl5\n")

	out := Execute(context.Background(), "sess1", "read_file", map[string]any{"path": "data.txt", "offset": 2, "limit": 2})
	if !strings.Contains(out, "l2") || !strings.Contains(out, "l3") || strings.Contains(out, "l1\n") {
		t.Fatalf("paging wrong: %q", out)
	}

	out = Execute(context.Background(), "sess1", "read_file", map[string]any{"path": "nope.txt"})
	if !IsErrorResult(out) {
		t.Fatalf("missing file should be error result: %q", out)
	}
}

func TestWriteAndEditFile(t *testing.T) {
	root := setupWorkspace(t)

	out := Execute(context.Background(), "sess1", "write_file", map[string]any{"path": "nested/a.txt", "content": "hello"})
	if IsErrorResult(out) {
		t.Fatalf("write failed: %s", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "sess1", "nested", "a.txt"))
	if string(data) != "hello" {
		t.Fatalf("write content mismatch: %q", data)
	}

	// 精确替换
	out = Execute(context.Background(), "sess1", "edit_file", map[string]any{"path": "nested/a.txt", "old_string": "hello", "new_string": "world"})
	if IsErrorResult(out) || !strings.Contains(out, "1 处") {
		t.Fatalf("edit failed: %s", out)
	}
	data, _ = os.ReadFile(filepath.Join(root, "sess1", "nested", "a.txt"))
	if string(data) != "world" {
		t.Fatalf("edit result mismatch: %q", data)
	}

	// 未命中提示
	out = Execute(context.Background(), "sess1", "edit_file", map[string]any{"path": "nested/a.txt", "old_string": "nope", "new_string": "x"})
	if !strings.Contains(out, "[未改动]") {
		t.Fatalf("no-hit should tell model: %s", out)
	}
}

func TestEditFileAmbiguous(t *testing.T) {
	root := setupWorkspace(t)
	writeWsFile(t, root, "dup.txt", "ab\nab\nab\n")

	out := Execute(context.Background(), "sess1", "edit_file", map[string]any{"path": "dup.txt", "old_string": "ab", "new_string": "z"})
	if !strings.Contains(out, "3 处") {
		t.Fatalf("multi-hit should refuse: %s", out)
	}

	out = Execute(context.Background(), "sess1", "edit_file", map[string]any{"path": "dup.txt", "old_string": "ab", "new_string": "z", "replace_all": true})
	if IsErrorResult(out) || !strings.Contains(out, "3 处") {
		t.Fatalf("replace_all should replace all: %s", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "sess1", "dup.txt"))
	if string(data) != "z\nz\nz\n" {
		t.Fatalf("replace_all result: %q", data)
	}
}

func TestCheckArgsMessages(t *testing.T) {
	out := Execute(context.Background(), "sess1", "write_file", map[string]any{"path": "a.txt"})
	if !strings.Contains(out, "[参数缺失]") || !strings.Contains(out, "content") {
		t.Fatalf("missing arg hint wrong: %s", out)
	}
	// edit_file 的 new_string 允许空串
	root := setupWorkspace(t)
	writeWsFile(t, root, "e.txt", "keep drop")
	out = Execute(context.Background(), "sess1", "edit_file", map[string]any{"path": "e.txt", "old_string": " drop", "new_string": ""})
	if IsErrorResult(out) {
		t.Fatalf("empty new_string means delete: %s", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "sess1", "e.txt"))
	if string(data) != "keep" {
		t.Fatalf("delete result: %q", data)
	}
}

func TestParseArguments(t *testing.T) {
	args, errMsg := ParseArguments(`{"path":"a"}`)
	if errMsg != "" || args["path"] != "a" {
		t.Fatalf("parse ok case failed: %v %s", args, errMsg)
	}
	if _, errMsg := ParseArguments(`{broken`); !strings.Contains(errMsg, "[参数解析失败]") {
		t.Fatalf("broken json should self-correct: %s", errMsg)
	}
}

func TestSanitizeTruncate(t *testing.T) {
	in := "a\x00b\x07c\n"
	if got := sanitize(in); got != "abc\n" {
		t.Fatalf("sanitize: %q", got)
	}
	long := strings.Repeat("x", maxOutput+100)
	got := sanitize(long)
	if !strings.Contains(got, "已截断") || len(got) > maxOutput+50 {
		t.Fatalf("truncate: len=%d", len(got))
	}
}

func TestBashToolIfAvailable(t *testing.T) {
	setupWorkspace(t)
	out := Execute(context.Background(), "sess1", "bash", map[string]any{"command": "echo hi"})
	if IsErrorResult(out) {
		t.Skipf("bash not available in this environment: %s", out)
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "[exit_code] 0") {
		t.Fatalf("bash output wrong: %s", out)
	}
}

