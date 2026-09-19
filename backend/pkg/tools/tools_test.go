package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backend/pkg/sandbox"
	"backend/pkg/storage"
)

// setupWorkspace 把 local 沙箱驱动的工作区根指到临时目录（默认驱动即 local）
func setupWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: root})
	t.Cleanup(func() { sandbox.Init(sandbox.Config{Driver: "local"}) }) // 恢复默认根
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

func TestWorkspacePathConfinement(t *testing.T) {
	root := setupWorkspace(t)
	// 越界路径：经驱动（local）读取一律拒绝
	for _, escape := range []string{"../secret", "a/../../etc/passwd", "/etc/passwd", "..\\..\\x"} {
		out := Execute(context.Background(), "sess1", "read_file", map[string]any{"path": escape})
		if !IsErrorResult(out) || !strings.Contains(out, "超出工作区范围") {
			t.Fatalf("escape path %q should be rejected: %s", escape, out)
		}
	}
	// 不存在的文件给可自纠正提示（非失败前缀变体由 [未读取] 承担）
	out := Execute(context.Background(), "sess1", "read_file", map[string]any{"path": "nope.txt"})
	if !strings.Contains(out, "[未读取]") {
		t.Fatalf("missing file hint wrong: %s", out)
	}

	// 模型常把工作区写成沙箱内绝对路径（EnvNote 告知 cwd 是 /workspace）：
	// 精确前缀剥离后照常工作，越界变体仍拒绝
	out = Execute(context.Background(), "sess1", "write_file", map[string]any{"path": "/workspace/abs-prefix.txt", "content": "ok"})
	if IsErrorResult(out) {
		t.Fatalf("workspace prefix should be stripped: %s", out)
	}
	if data, err := os.ReadFile(filepath.Join(root, "sess1", "abs-prefix.txt")); err != nil || string(data) != "ok" {
		t.Fatalf("stripped-prefix file wrong: %q %v", data, err)
	}
	for _, escape := range []string{"/workspace/../etc/passwd", "/workspace"} {
		out := Execute(context.Background(), "sess1", "write_file", map[string]any{"path": escape, "content": "x"})
		if !IsErrorResult(out) || !strings.Contains(out, "超出工作区范围") {
			t.Fatalf("escape %q should stay rejected: %s", escape, out)
		}
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

func TestExportArtifact(t *testing.T) {
	root := setupWorkspace(t)
	uploads := t.TempDir()
	oldRoot := storage.LocalRoot
	storage.LocalRoot = uploads
	t.Cleanup(func() { storage.LocalRoot = oldRoot })

	writeWsFile(t, root, "report.md", "# hello")

	out := ExecuteWithOutput(context.Background(), "sess1", "export_artifact",
		map[string]any{"path": "report.md", "filename": "最终报告.md"})
	if IsErrorResult(out.Text) {
		t.Fatalf("export failed: %s", out.Text)
	}
	if len(out.Attachments) != 1 {
		t.Fatalf("attachment missing: %+v", out)
	}
	att := out.Attachments[0]
	if !strings.HasPrefix(att.URL, "/api/uploads/") || att.Filename != "最终报告.md" ||
		att.Size == nil || *att.Size != 7 || att.ContentType != "text/markdown" {
		t.Fatalf("attachment wrong: %+v", att)
	}
	if _, err := os.Stat(filepath.Join(uploads, filepath.Base(att.URL))); err != nil {
		t.Fatalf("stored file missing: %v", err)
	}
	if !strings.Contains(out.Text, "已导出") || !strings.Contains(out.Text, att.URL) {
		t.Fatalf("summary should carry link: %s", out.Text)
	}

	// 不传 filename 时用 path 末段，且保留 Path 相对路径
	out = ExecuteWithOutput(context.Background(), "sess1", "export_artifact", map[string]any{"path": "report.md"})
	if IsErrorResult(out.Text) || out.Attachments[0].Filename != "report.md" || out.Attachments[0].Path != "report.md" {
		t.Fatalf("default filename/path wrong: %s %+v", out.Text, out.Attachments)
	}

	// 导出多级子目录文件：Filename 截断为基名，Path 完整保留相对路径
	writeWsFile(t, root, "src/components/Header.tsx", "header content")
	outNested := ExecuteWithOutput(context.Background(), "sess1", "export_artifact", map[string]any{"path": "src/components/Header.tsx"})
	if IsErrorResult(outNested.Text) {
		t.Fatalf("nested export failed: %s", outNested.Text)
	}
	nestedAtt := outNested.Attachments[0]
	if nestedAtt.Filename != "Header.tsx" || nestedAtt.Path != "src/components/Header.tsx" {
		t.Fatalf("nested attachment wrong: %+v", nestedAtt)
	}
}

func TestExportArtifactMissing(t *testing.T) {
	setupWorkspace(t)
	out := Execute(context.Background(), "sess1", "export_artifact", map[string]any{"path": "nope.md"})
	if !IsErrorResult(out) || !strings.Contains(out, "[未读取]") {
		t.Fatalf("missing file should fail clearly: %s", out)
	}
}


// TestEnvInjectionIntoBash 阶段五：skill secrets 经 tools.WithEnv 注入 context，
// bash / python_exec 执行时应转交给沙箱进程（local 驱动即进程环境变量）
func TestEnvInjectionIntoBash(t *testing.T) {
	setupWorkspace(t)
	ctx := WithEnv(context.Background(), SortedEnv(map[string]string{"SKILL_SECRET_KEY": "s3cr3t-val"}))
	out := Execute(ctx, "sess1", "bash", map[string]any{"command": "echo KEY=$SKILL_SECRET_KEY"})
	if IsErrorResult(out) {
		t.Fatalf("bash failed: %s", out)
	}
	if !strings.Contains(out, "KEY=s3cr3t-val") {
		t.Fatalf("env 未注入沙箱进程: %s", out)
	}
	// 未注入时不应凭空出现
	out = Execute(context.Background(), "sess1", "bash", map[string]any{"command": "echo KEY=$SKILL_SECRET_KEY"})
	if strings.Contains(out, "s3cr3t-val") {
		t.Fatalf("env 不应泄漏到未注入的调用: %s", out)
	}
}

// TestSkillMountDirWriteGuard 挂载进工作区的 skills/ 目录是只读引用，
// 写入类工具应给出可自纠正提示而不是放行污染
func TestSkillMountDirWriteGuard(t *testing.T) {
	setupWorkspace(t)
	for name, args := range map[string]map[string]any{
		"write_file": {"path": "skills/fetch-url/SKILL.md", "content": "篡改"},
		"edit_file":  {"path": "skills/fetch-url/SKILL.md", "old_string": "a", "new_string": "b"},
	} {
		out := Execute(context.Background(), "sess1", name, args)
		if !IsErrorResult(out) || !strings.Contains(out, "只读") {
			t.Fatalf("%s 应拦截 skills/ 写入: %s", name, out)
		}
	}
	// 前缀混淆变体（./、多余斜杠）同样拦截
	out := Execute(context.Background(), "sess1", "write_file",
		map[string]any{"path": "./skills/x/y.txt", "content": "x"})
	if !IsErrorResult(out) {
		t.Fatalf("skills/ 前缀变体未拦截: %s", out)
	}
}

// TestLoadSkillSchemaArgs load_skill 的参数校验：缺 name 给可照抄示例
func TestLoadSkillSchemaArgs(t *testing.T) {
	schema := LoadSkillSchema()
	fn := schema["function"].(map[string]any)
	if fn["name"] != LoadSkillTool {
		t.Fatalf("schema name wrong: %v", fn["name"])
	}
	if msg := CheckArgs(LoadSkillTool, map[string]any{}); !strings.Contains(msg, "[参数缺失]") || !strings.Contains(msg, `"name": "fetch-url"`) {
		t.Fatalf("load_skill 缺参提示异常: %s", msg)
	}
}
