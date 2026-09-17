package skill

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedWorkingCopy(t *testing.T) {
	t.Helper()
	write := func(rel, content string) {
		p := filepath.Join(rootDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("edit-skill/SKILL.md", "---\nname: edit\ndescription: 编辑测试\n---\n# Edit")
	write("edit-skill/scripts/run.py", "print('hi')")
	write("edit-skill/scripts/sub/deep.txt", "deep")
	write("edit-skill/data.bin", "\x00\x01\x02binary")
}

func TestListAndReadWorkingTree(t *testing.T) {
	withRoot(t, func() {
		seedWorkingCopy(t)
		nodes, err := ListWorkingTree("edit-skill")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		byPath := map[string]FileNode{}
		for _, n := range nodes {
			byPath[n.Path] = n
		}
		if len(nodes) != 4 {
			t.Fatalf("应含 4 个文件: %+v", nodes)
		}
		if n := byPath["data.bin"]; !n.IsBinary {
			t.Fatal("NUL 字节文件应判定为二进制")
		}
		if n := byPath["scripts/run.py"]; n.IsBinary || n.Size == 0 {
			t.Fatalf("文本文件判定异常: %+v", n)
		}

		content, isBin, err := ReadWorkingFile("edit-skill", "scripts/run.py")
		if err != nil || isBin || !strings.Contains(content, "print") {
			t.Fatalf("读取文本异常: %v %q", err, content)
		}
		_, isBin, err = ReadWorkingFile("edit-skill", "data.bin")
		if err != nil || !isBin {
			t.Fatalf("二进制应返回 isBinary 且不报错: %v %v", err, isBin)
		}
		if _, _, err := ReadWorkingFile("edit-skill", "nope.txt"); err == nil {
			t.Fatal("不存在文件应报错")
		}
	})
}

func TestWorkingFileGuards(t *testing.T) {
	withRoot(t, func() {
		seedWorkingCopy(t)

		// 越界与保留目录
		for _, bad := range []string{"../escape.txt", ".secrets/x.env", "sub/../../out.txt"} {
			if err := WriteWorkingFile("edit-skill", bad, "x", false); err == nil {
				t.Fatalf("路径 %q 应被拒绝", bad)
			}
		}
		// 根 SKILL.md 保护
		if err := DeleteWorkingFile("edit-skill", "SKILL.md"); err == nil {
			t.Fatal("根 SKILL.md 不可删除")
		}
		if err := RenameWorkingFile("edit-skill", "SKILL.md", "RENAMED.md"); err == nil {
			t.Fatal("根 SKILL.md 不可重命名")
		}
		// SKILL.md 内容可以改
		if err := WriteWorkingFile("edit-skill", "SKILL.md", "---\nname: edit\n---\nv2", false); err != nil {
			t.Fatalf("SKILL.md 内容应可写: %v", err)
		}
	})
}

func TestWorkingFileWriteRenameDelete(t *testing.T) {
	withRoot(t, func() {
		seedWorkingCopy(t)

		// 新建：createOnly 防覆盖
		if err := WriteWorkingFile("edit-skill", "notes.md", "v1", true); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := WriteWorkingFile("edit-skill", "notes.md", "v1", true); err == nil {
			t.Fatal("已存在文件 createOnly 应报错")
		}
		// 保存覆盖
		if err := WriteWorkingFile("edit-skill", "notes.md", "v2", false); err != nil {
			t.Fatalf("write: %v", err)
		}
		if c, _, _ := ReadWorkingFile("edit-skill", "notes.md"); c != "v2" {
			t.Fatalf("覆盖写入失败: %q", c)
		}
		// 重命名 + 移动
		if err := RenameWorkingFile("edit-skill", "notes.md", "docs/note.md"); err != nil {
			t.Fatalf("rename: %v", err)
		}
		if err := RenameWorkingFile("edit-skill", "docs/note.md", "scripts/run.py"); err == nil {
			t.Fatal("目标已存在应拒绝")
		}
		// 删除
		if err := DeleteWorkingFile("edit-skill", "docs/note.md"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := os.Stat(filepath.Join(rootDir, "edit-skill", "docs", "note.md")); !os.IsNotExist(err) {
			t.Fatal("删除后文件不应存在")
		}
	})
}

func TestZipWorkingTree(t *testing.T) {
	withRoot(t, func() {
		seedWorkingCopy(t)
		data, err := ZipWorkingTree("edit-skill")
		if err != nil {
			t.Fatalf("zip: %v", err)
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("zip 解析: %v", err)
		}
		names := map[string]bool{}
		for _, f := range zr.File {
			names[f.Name] = true
		}
		if !names["SKILL.md"] || !names["scripts/sub/deep.txt"] || len(names) != 4 {
			t.Fatalf("zip 内容异常: %v", names)
		}
	})
}

func TestCreateEmpty(t *testing.T) {
	withRoot(t, func() {
		dirName, err := CreateEmpty("My Tool", "空技能")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if dirName != "my-tool" {
			t.Fatalf("目录名 = %q", dirName)
		}
		parsed, ok := readDirEntry(dirName)
		if !ok || parsed.Name != "My Tool" || parsed.Description != "空技能" {
			t.Fatalf("骨架 SKILL.md 异常: %+v", parsed)
		}
		if _, err := CreateEmpty("My Tool", "x"); err == nil {
			t.Fatal("同名应报错")
		}
	})
}
