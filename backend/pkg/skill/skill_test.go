package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backend/pkg/sandbox"
)

// readFileViaSandbox 经默认 local 沙箱驱动读回挂载产物，验证真实落点
func readFileViaSandbox(sessionID, rel string) ([]byte, error) {
	return sandbox.ReadFile(context.Background(), sessionID, rel)
}

func TestParseSkillMD(t *testing.T) {
	text := "---\n" +
		"name: fetch-url\n" +
		"description: 抓取网页转 Markdown\n" +
		"tags: [工具, 网页]\n" +
		"---\n" +
		"# 正文标题\n使用说明…"

	p := ParseSkillMD(text)
	if p.Name != "fetch-url" {
		t.Fatalf("name = %q, want fetch-url", p.Name)
	}
	if p.Description != "抓取网页转 Markdown" {
		t.Fatalf("description = %q", p.Description)
	}
	if len(p.Tags) != 2 || p.Tags[0] != "工具" || p.Tags[1] != "网页" {
		t.Fatalf("tags = %v", p.Tags)
	}
	if !strings.HasPrefix(p.Body, "# 正文标题") {
		t.Fatalf("body 应以正文开头，实际 %q", p.Body)
	}
}

func TestParseSkillMDParameters(t *testing.T) {
	// README 5.3：frontmatter 可携带类 JSON-Schema 入参契约
	text := "---\n" +
		"name: demo\n" +
		"parameters:\n" +
		"  type: object\n" +
		"  required: [url]\n" +
		"---\n" +
		"body"
	p := ParseSkillMD(text)
	if p.Parameters["type"] != "object" {
		t.Fatalf("parameters.type = %v", p.Parameters["type"])
	}
	if p.Body != "body" {
		t.Fatalf("body = %q", p.Body)
	}
}

func TestParseSkillMDNoFrontmatter(t *testing.T) {
	p := ParseSkillMD("# 只有正文")
	if p.Name != "" {
		t.Fatalf("无 frontmatter 时 name 应为空，实际 %q", p.Name)
	}
	if p.Body != "# 只有正文" {
		t.Fatalf("body = %q", p.Body)
	}
}

func TestParseSkillMDBadYAML(t *testing.T) {
	// frontmatter 坏成一行乱文本：不 panic，按无元数据降级
	p := ParseSkillMD("---\n: : :\nnot valid yaml ][\n---\n正文")
	if p.Body == "" {
		t.Fatal("坏 frontmatter 不应丢正文")
	}
}

func TestNormalizeTags(t *testing.T) {
	got := NormalizeTags([]string{" a ", "", "b", "b", "  c  ", strings.Repeat("x", 30)})
	if len(got) != 4 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("tags = %v", got)
	}
	// 超长标签截到 MaxTagLen 而不是丢弃
	if got[3] != strings.Repeat("x", MaxTagLen) {
		t.Fatalf("长标签应截到 %d 字符: %q", MaxTagLen, got[3])
	}
	if len(NormalizeTags(nil)) != 0 {
		t.Fatal("nil 输入应返回空")
	}
}

func TestSanitizeDirName(t *testing.T) {
	cases := map[string]string{
		"Fetch URL":  "fetch-url",
		"工具/搜索":     "skill", // 非法字符折叠后为空 → 回退
		"my_skill-2": "my_skill-2",
		"  spaced ":  "spaced",
	}
	for in, want := range cases {
		if got := SanitizeDirName(in); got != want {
			t.Fatalf("SanitizeDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildPrompts(t *testing.T) {
	enabled := []Entry{{DirName: "fetch-url", Name: "fetch-url", Description: "抓网页"}}
	sp := BuildSkillsPrompt(enabled)
	if !strings.Contains(sp, "可用 Skills") || !strings.Contains(sp, "**fetch-url**（fetch-url/）") {
		t.Fatalf("启用段内容异常:\n%s", sp)
	}

	long := strings.Repeat("很长的描述", 100) // 500 字符
	cp := BuildCatalogPrompt([]Entry{{DirName: "d", Name: "n", Description: long}})
	if !strings.Contains(cp, "load_skill") {
		t.Fatal("目录段应指引 load_skill")
	}
	// 描述截断到上限附近
	if got := len([]rune(cp)); got > CatalogDescLimit+300 {
		t.Fatalf("目录段未截断描述，长度 %d", got)
	}
	if BuildCatalogPrompt(nil) != "" || BuildSkillsPrompt(nil) != "" {
		t.Fatal("空输入应返回空串")
	}
}

// fixtureLib 在临时目录里搭一个最小技能库
func fixtureLib(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good-skill/SKILL.md", "---\nname: good\ndescription: 好技能\n---\n# Good\n说明")
	write("good-skill/scripts/run.py", "print('hi')")
	write("no-name/SKILL.md", "---\ndescription: 缺名字\n---\nbody")
	write("no-md/README.md", "没有 SKILL.md")
	write(".secrets/good-skill.env", "# 注释\nGOOD_KEY=\"v1\"\nPLAIN=abc\n坏行没有等号\n")
	return root
}

func TestListDirAndSecrets(t *testing.T) {
	old := rootDir
	rootDir = fixtureLib(t)
	t.Cleanup(func() { rootDir = old })

	entries := ListDir()
	if len(entries) != 1 {
		t.Fatalf("应只收录 1 个合法技能，实际 %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "good" || entries[0].DirName != "good-skill" {
		t.Fatalf("entry 异常: %+v", entries[0])
	}

	secrets := ReadSecrets("good-skill")
	if secrets["GOOD_KEY"] != "v1" || secrets["PLAIN"] != "abc" || len(secrets) != 2 {
		t.Fatalf("secrets 解析异常: %+v", secrets)
	}
	merged := CollectEnvs([]string{"good-skill", "不存在"})
	if merged["GOOD_KEY"] != "v1" {
		t.Fatalf("CollectEnvs 异常: %+v", merged)
	}

	md, err := ReadSkillMD("good-skill")
	if err != nil || !strings.HasPrefix(md, "---") {
		t.Fatalf("ReadSkillMD 异常: %v", md)
	}
	if _, err := ReadSkillMD("不存在"); err == nil {
		t.Fatal("不存在的技能应报错")
	}
}

func TestListDirMissingRoot(t *testing.T) {
	old := rootDir
	rootDir = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { rootDir = old })
	if got := ListDir(); len(got) != 0 {
		t.Fatalf("缺失目录应返回空列表，实际 %+v", got)
	}
}

func TestMountToSandbox(t *testing.T) {
	old := rootDir
	rootDir = fixtureLib(t)
	t.Cleanup(func() { rootDir = old })

	sessionID := "skill-mount-test"
	t.Cleanup(func() { sandbox.Close(sessionID) })
	n, err := MountToSandbox(context.Background(), sessionID, "good-skill")
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	if n != 2 {
		t.Fatalf("挂载文件数 = %d, want 2", n)
	}

	// 经默认 local 沙箱驱动读回，验证落点与内容（含子目录脚本）
	data, err := readFileViaSandbox(sessionID, MountDir+"/good-skill/SKILL.md")
	if err != nil || !strings.Contains(string(data), "好技能") {
		t.Fatalf("SKILL.md 挂载异常: %v %q", err, data)
	}
	script, err := readFileViaSandbox(sessionID, MountDir+"/good-skill/scripts/run.py")
	if err != nil || !strings.Contains(string(script), "print") {
		t.Fatalf("脚本挂载异常: %v %q", err, script)
	}

	if _, err := MountToSandbox(context.Background(), sessionID, "不存在"); err == nil {
		t.Fatal("挂载不存在的技能应报错")
	}
}
