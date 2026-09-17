// files.go 技能工作副本的在线文件编辑（技能管理最小闭环之二）。
//
// 工作副本即 skills/<dir>/ 目录本身，所有路径操作经 safeJoin 收口：
// 拒绝绝对路径与 .. 越界，顶层不得触碰 .secrets / .versions 等保留目录，
// 根下的 SKILL.md 是技能入口声明，禁止删除与重命名（改内容走写入）。
// 二进制文件按「NUL 字节 / 非 UTF-8」判定（对末尾截断的多字节字符做容忍），
// 读取时二进制文件不回传内容，前端按占位提示渲染。
package skill

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"backend/pkg/errors"
)

// 保留目录名：在线编辑不可触达（secrets 单独管理，版本快照属发布机制）
var reservedTopDirs = map[string]bool{".secrets": true, ".versions": true}

// FileNode 工作副本文件的扁平清单项（前端自行构建树）
type FileNode struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	IsBinary bool   `json:"is_binary"`
}

// isBinaryBytes 二进制判定：NUL 字节或整体非法 UTF-8。采样边界可能正好切在
// 多字节 UTF-8 字符中间，末尾 3 字节内的解码失败做剔除重试容忍
func isBinaryBytes(data []byte) bool {
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	if utf8.Valid(data) {
		return false
	}
	if n := len(data); n > 3 {
		if utf8.Valid(data[:n-3]) {
			return false
		}
	}
	return true
}

// safeJoin 把工作副本内相对路径安全拼成宿主绝对路径（编辑器入口统一收口）
func safeJoin(dirName, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	rel = strings.TrimPrefix(strings.TrimPrefix(rel, "/"), "\\")
	if rel == "" {
		return "", errors.NewMsg("路径不能为空")
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.NewMsg("非法路径：" + rel)
		}
	}
	if reservedTopDirs[parts[0]] {
		return "", errors.NewMsg("路径不可访问：" + parts[0])
	}
	abs, err := filepath.Abs(filepath.Join(rootDir, dirName, filepath.FromSlash(path.Join(parts...))))
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(filepath.Join(rootDir, dirName))
	if err != nil {
		return "", err
	}
	if r, err := filepath.Rel(rootAbs, abs); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.NewMsg("路径越界：" + rel)
	}
	return abs, nil
}

// ListWorkingTree 工作副本全量文件清单（扁平、按路径排序；跳过隐藏目录）
func ListWorkingTree(dirName string) ([]FileNode, error) {
	root := filepath.Join(rootDir, dirName)
	var out []FileNode
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if parts := strings.Split(relSlash, "/"); reservedTopDirs[parts[0]] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		binary := true
		if f, err := os.Open(p); err == nil {
			head := make([]byte, 8192)
			n, _ := f.Read(head)
			_ = f.Close()
			binary = isBinaryBytes(head[:n])
		}
		out = append(out, FileNode{Path: relSlash, Size: info.Size(), IsBinary: binary})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.NewMsg("技能目录不存在，可能已被删除")
		}
		return nil, errors.Wrap(err, "读取技能文件清单失败")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadWorkingFile 读取工作副本文件；二进制文件返回空内容 + isBinary=true
func ReadWorkingFile(dirName, rel string) (content string, isBinary bool, err error) {
	p, err := safeJoin(dirName, rel)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, errors.NewMsg("文件不存在：" + rel)
		}
		return "", false, errors.Wrap(err, "读取文件失败")
	}
	if isBinaryBytes(data[:min(len(data), 8192)]) {
		return "", true, nil
	}
	return string(data), false, nil
}

// WriteWorkingFile 写入文本文件；createOnly=true 时已存在则报错（新建入口防覆盖）
func WriteWorkingFile(dirName, rel, content string, createOnly bool) error {
	p, err := safeJoin(dirName, rel)
	if err != nil {
		return err
	}
	if createOnly {
		if _, err := os.Stat(p); err == nil {
			return errors.NewMsg("文件已存在：" + rel)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return errors.Wrap(err, "创建目录失败")
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return errors.Wrap(err, "写入文件失败")
	}
	return nil
}

// DeleteWorkingFile 删除工作副本文件；根下 SKILL.md 是技能入口，禁止删除
func DeleteWorkingFile(dirName, rel string) error {
	p, err := safeJoin(dirName, rel)
	if err != nil {
		return err
	}
	rootAbs, _ := filepath.Abs(filepath.Join(rootDir, dirName))
	if filepath.Clean(filepath.Dir(p)) == filepath.Clean(rootAbs) && filepath.Base(p) == "SKILL.md" {
		return errors.NewMsg("SKILL.md 是技能入口声明，不可删除")
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return errors.NewMsg("文件不存在：" + rel)
		}
		return errors.Wrap(err, "删除文件失败")
	}
	return nil
}

// RenameWorkingFile 重命名 / 移动；根下 SKILL.md 与已存在目标均拒绝
func RenameWorkingFile(dirName, oldRel, newRel string) error {
	src, err := safeJoin(dirName, oldRel)
	if err != nil {
		return err
	}
	dst, err := safeJoin(dirName, newRel)
	if err != nil {
		return err
	}
	rootAbs, _ := filepath.Abs(filepath.Join(rootDir, dirName))
	if filepath.Clean(filepath.Dir(src)) == filepath.Clean(rootAbs) && filepath.Base(src) == "SKILL.md" {
		return errors.NewMsg("SKILL.md 是技能入口声明，不可重命名")
	}
	if _, err := os.Stat(src); err != nil {
		return errors.NewMsg("文件不存在：" + oldRel)
	}
	if _, err := os.Stat(dst); err == nil {
		return errors.NewMsg("目标已存在：" + newRel)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return errors.Wrap(err, "创建目录失败")
	}
	if err := os.Rename(src, dst); err != nil {
		return errors.Wrap(err, "重命名失败")
	}
	return nil
}

// ZipWorkingTree 把工作副本打包为 zip（卡片下载用；文件路径为 zip 内相对路径）
func ZipWorkingTree(dirName string) ([]byte, error) {
	root := filepath.Join(rootDir, dirName)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return nil, errors.Wrap(err, "打包技能失败")
	}
	if err := zw.Close(); err != nil {
		return nil, errors.Wrap(err, "打包技能失败")
	}
	return buf.Bytes(), nil
}

// CreateEmpty 从空新建技能：写入骨架 SKILL.md 并返回目录名（已存在同名目录时报错）
func CreateEmpty(name, description string) (string, error) {
	dirName := SanitizeDirName(name)
	target := filepath.Join(rootDir, dirName)
	if _, err := os.Stat(target); err == nil {
		return "", errors.NewMsg("已存在同名技能：" + dirName)
	}
	front := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" +
		"# " + name + "\n\n（在文件页补充使用说明：何时使用、如何调用、参数与示例。）\n"
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", errors.Wrap(err, "创建技能目录失败")
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(front), 0o644); err != nil {
		return "", errors.Wrap(err, "写入 SKILL.md 失败")
	}
	return dirName, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
