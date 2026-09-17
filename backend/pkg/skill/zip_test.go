package skill

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildZip 构造测试 zip：entries 为 路径 → 内容
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func withRoot(t *testing.T, fn func()) {
	t.Helper()
	old := rootDir
	rootDir = t.TempDir()
	t.Cleanup(func() { rootDir = old })
	fn()
}

const sampleSKILLMD = "---\nname: demo-skill\ndescription: 演示技能\n---\n# Demo\n用法见 scripts"

func TestPeekAndInstallNestedRoot(t *testing.T) {
	withRoot(t, func() {
		z := buildZip(t, map[string]string{
			"demo-skill/SKILL.md":        sampleSKILLMD,
			"demo-skill/scripts/run.py":  "print('run')",
			"demo-skill/secrets.env":     "DEMO_KEY=abc\n# 注释\n",
			"demo-skill/docs/note.md":    "文档",
		})
		meta, err := PeekZip(z)
		if err != nil {
			t.Fatalf("peek: %v", err)
		}
		if meta.Name != "demo-skill" || meta.Description != "演示技能" {
			t.Fatalf("meta 异常: %+v", meta)
		}
		if meta.RootDir != "demo-skill" {
			t.Fatalf("root dir = %q", meta.RootDir)
		}

		secrets, err := InstallFromZip(z, "demo-skill")
		if err != nil {
			t.Fatalf("install: %v", err)
		}
		if secrets["DEMO_KEY"] != "abc" || len(secrets) != 1 {
			t.Fatalf("secrets 抽取异常: %+v", secrets)
		}
		// secrets.env 不落工作副本；脚本与文档就位
		if _, err := os.Stat(filepath.Join(rootDir, "demo-skill", "secrets.env")); !os.IsNotExist(err) {
			t.Fatal("secrets.env 不应进工作副本")
		}
		for _, rel := range []string{"SKILL.md", "scripts/run.py", "docs/note.md"} {
			if _, err := os.Stat(filepath.Join(rootDir, "demo-skill", rel)); err != nil {
				t.Fatalf("文件 %s 未安装: %v", rel, err)
			}
		}
		// ListDir 可见
		entries := ListDir()
		if len(entries) != 1 || entries[0].Name != "demo-skill" {
			t.Fatalf("ListDir 异常: %+v", entries)
		}
	})
}

func TestInstallFlatRoot(t *testing.T) {
	withRoot(t, func() {
		z := buildZip(t, map[string]string{"SKILL.md": sampleSKILLMD})
		meta, err := PeekZip(z)
		if err != nil {
			t.Fatalf("peek: %v", err)
		}
		if meta.RootDir != "" {
			t.Fatalf("平铺根 RootDir 应为空串: %q", meta.RootDir)
		}
		if _, err := InstallFromZip(z, "demo-skill"); err != nil {
			t.Fatalf("install: %v", err)
		}
		if _, err := os.Stat(filepath.Join(rootDir, "demo-skill", "SKILL.md")); err != nil {
			t.Fatalf("平铺安装失败: %v", err)
		}
	})
}

func TestZipSlipRejected(t *testing.T) {
	withRoot(t, func() {
		for name := range map[string]string{
			"../../evil.txt":     "",
			"good/SKILL.md":     sampleSKILLMD,
			"/etc/passwd":       "",
			"C:/windows/x.txt":  "",
		} {
			z := buildZip(t, map[string]string{
				"good/SKILL.md": sampleSKILLMD,
				name:            "x",
			})
			if _, err := PeekZip(z); err == nil {
				t.Fatalf("越界路径 %q 应被拒绝", name)
			}
		}
	})
}

func TestZipMissingSkillMDOrName(t *testing.T) {
	withRoot(t, func() {
		if _, err := PeekZip(buildZip(t, map[string]string{"readme.txt": "没有 SKILL.md"})); err == nil {
			t.Fatal("缺 SKILL.md 应报错")
		}
		noName := buildZip(t, map[string]string{"SKILL.md": "---\ndescription: 缺名字\n---\nbody"})
		if _, err := PeekZip(noName); err == nil {
			t.Fatal("缺 name 应报错")
		}
		if _, err := PeekZip([]byte("这不是zip")); err == nil {
			t.Fatal("非 zip 内容应报错")
		}
	})
}

func TestInstallOverwriteReplacesTree(t *testing.T) {
	withRoot(t, func() {
		v1 := buildZip(t, map[string]string{
			"SKILL.md":       sampleSKILLMD,
			"old-file.txt":   "旧文件",
		})
		if _, err := InstallFromZip(v1, "demo-skill"); err != nil {
			t.Fatalf("v1 install: %v", err)
		}
		v2 := buildZip(t, map[string]string{
			"SKILL.md":      "---\nname: demo-skill\ndescription: v2\n---\n# v2",
			"new-file.txt":  "新文件",
		})
		if _, err := InstallFromZip(v2, "demo-skill"); err != nil {
			t.Fatalf("v2 install: %v", err)
		}
		if _, err := os.Stat(filepath.Join(rootDir, "demo-skill", "old-file.txt")); !os.IsNotExist(err) {
			t.Fatal("覆盖安装应清掉旧文件")
		}
		if _, err := os.Stat(filepath.Join(rootDir, "demo-skill", "new-file.txt")); err != nil {
			t.Fatalf("新文件缺失: %v", err)
		}
	})
}

func TestGBKFilenameDecoded(t *testing.T) {
	withRoot(t, func() {
		// "脚本.py" 的 GBK 编码字节（非法 UTF-8）。Go 的 zip.Writer 对非法
		// UTF-8 名字不置 UTF-8 标志位——正是 Windows 压缩工具的产物形态
		gbkName := string([]byte{0xBD, 0xC5, 0xB1, 0xBE, 0x2E, 0x70, 0x79})
		z := buildZip(t, map[string]string{
			"SKILL.md": sampleSKILLMD,
			gbkName:    "print('中文脚本')",
		})

		secrets, err := InstallFromZip(z, "demo-skill")
		if err != nil {
			t.Fatalf("install: %v", err)
		}
		if len(secrets) != 0 {
			t.Fatalf("不应抽出 secrets: %+v", secrets)
		}
		decoded := filepath.Join(rootDir, "demo-skill", "脚本.py")
		if data, err := os.ReadFile(decoded); err != nil || !strings.Contains(string(data), "中文脚本") {
			t.Fatalf("GBK 文件名未正确解码落盘: %v", err)
		}
	})
}

func TestWriteAndRemoveSecrets(t *testing.T) {
	withRoot(t, func() {
		if err := os.MkdirAll(filepath.Join(rootDir, "demo-skill"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteSecrets("demo-skill", map[string]string{"B_KEY": "2", "A_KEY": "1"}); err != nil {
			t.Fatalf("write: %v", err)
		}
		// 键排序输出稳定
		raw, _ := os.ReadFile(filepath.Join(rootDir, ".secrets", "demo-skill.env"))
		if string(raw) != "A_KEY=1\nB_KEY=2\n" {
			t.Fatalf("secrets 文件内容异常: %q", raw)
		}
		if got := ReadSecrets("demo-skill"); got["A_KEY"] != "1" || got["B_KEY"] != "2" {
			t.Fatalf("回读异常: %+v", got)
		}
		if err := RemoveSkill("demo-skill"); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if _, err := os.Stat(filepath.Join(rootDir, ".secrets", "demo-skill.env")); !os.IsNotExist(err) {
			t.Fatal("删除技能应连带清理机密文件")
		}
		if err := RemoveSkill("../escape"); err == nil {
			t.Fatal("越界目录名应被拒绝")
		}
	})
}
