// zip.go 技能 zip 包安装（技能管理最小闭环）。
//
// 支持两种 zip 结构：根目录直接含 SKILL.md，或根目录下一个子目录含 SKILL.md
// （压缩工具普遍会多包一层）。安装流程：安全解压到临时目录 → 定位 SKILL.md
// 所在目录作为技能根 → 校验 frontmatter → 把 secrets 文件（secrets.env / .env）
// 从技能根抽出（不落工作副本）→ 整树拷贝到 skills/<dirName>/。
//
// 安全防护：
//   - zip-slip：拒绝绝对路径、盘符与 ".." 越界段，解压目标必须落在临时目录内；
//   - 体积与数量上限：与挂载防护同阈值（200 文件 / 16MB），外加 zip 本身 30MB；
//   - 文件名编码：Windows / 部分国产压缩工具打中文 zip 不置 UTF-8 标志位
//     （general purpose flag bit 11）、按 GBK 编码——解出来的文件名是乱码，
//     SKILL.md 里写的相对路径会全部失配。这里对非 UTF-8 文件名按 GBK 重解码。
package skill

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"backend/pkg/errors"
)

// MaxZipBytes 单个技能 zip 的体积上限（与聊天附件上限对齐）
const MaxZipBytes = 30 << 20

// secretsFileNames 上传 zip 里被识别为机密文件的技能根文件名；
// 只认技能根下的这两个名字，避免把子目录里的同名文件误当机密
var secretsFileNames = map[string]bool{"secrets.env": true, ".env": true}

// driveRe Windows 盘符前缀（C:/x、C:\x）
var driveRe = regexp.MustCompile(`^[a-zA-Z]:`)

// ZipMeta zip 的预检结果：SKILL.md frontmatter 元数据 + 技能根在 zip 内的相对目录
type ZipMeta struct {
	Meta
	RootDir string // zip 内技能根目录（"" 表示压缩包根）
}

// extractZip 安全解压到临时目录，返回 zip 内技能根相对临时目录的路径。
// dirName 仅用于错误文案。
func extractZip(zipBytes []byte, dest string, forInstall string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", errors.NewMsg("zip 包损坏或不是合法的 zip 格式")
	}

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			continue // 目录条目跳过，写文件时按需建父目录
		}
		name := decodeZipName(f)
		rel, err := safeZipName(name)
		if err != nil {
			return "", err
		}
		if err := writeZipEntry(f, filepath.Join(dest, filepath.FromSlash(rel)), destAbs, &total); err != nil {
			return "", err
		}
	}

	// 定位技能根：最浅的 SKILL.md 所在目录
	root, err := findSkillRoot(dest)
	if err != nil {
		return "", errors.Wrapf(err, "%s 安装失败", forInstall)
	}
	rel, err := filepath.Rel(dest, root)
	if err != nil {
		return "", err
	}
	if rel == "." {
		rel = "" // 压缩包根即技能根
	}
	return filepath.ToSlash(rel), nil
}

// decodeZipName 还原 zip entry 的真实文件名：Go 的 archive/zip 不做编码
// 转换，UTF-8 标志位没置且内容非合法 UTF-8 时按 GBK 解一次（解不动保持原样）
func decodeZipName(f *zip.File) string {
	const utf8Flag = 0x800
	if f.Flags&utf8Flag != 0 || utf8.ValidString(f.Name) {
		return f.Name
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().String(f.Name); err == nil && utf8.ValidString(decoded) {
		return decoded
	}
	return f.Name
}

// safeZipName 校验并规范化 entry 名（zip 内为斜杠分隔），拦截 zip-slip
func safeZipName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.NewMsg("zip 内存在空文件名条目")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") || driveRe.MatchString(name) {
		return "", errors.NewMsg("zip 包含非法路径：" + name)
	}
	cleaned := path.Clean(name)
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == ".." {
			return "", errors.NewMsg("zip 路径不允许包含 ..：" + name)
		}
	}
	return cleaned, nil
}

// writeZipEntry 解出单个文件。total 累计解压体积做 zip 炸弹防护；
// 落点必须仍在 destAbs 内（对解析后的名字再做一次防御性校验）
func writeZipEntry(f *zip.File, target, destAbs string, total *int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(destAbs, targetAbs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.NewMsg("zip 路径穿越：" + f.Name)
	}

	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer dst.Close()
	n, err := io.Copy(dst, io.LimitReader(src, maxMountTotalByte+1))
	*total += n
	if *total > maxMountTotalByte {
		return errors.NewMsg(fmt.Sprintf("zip 解压总大小超过 %dMB 上限", maxMountTotalByte>>20))
	}
	if n > maxMountTotalByte {
		return errors.NewMsg("zip 内存在超过 16MB 的单文件")
	}
	return err
}

// findSkillRoot 递归查找最浅的 SKILL.md，返回其所在目录
func findSkillRoot(dir string) (string, error) {
	var best string
	var bestDepth int
	first := true
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		root := filepath.Dir(p)
		depth := strings.Count(strings.TrimPrefix(strings.TrimPrefix(p, dir), string(filepath.Separator)), string(filepath.Separator))
		if first || depth < bestDepth {
			best, bestDepth, first = root, depth, false
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if first {
		return "", errors.NewMsg("zip 中未找到 SKILL.md")
	}
	return best, nil
}

// PeekZip 只解析 zip 拿 SKILL.md 元数据，不落盘。上传接口用它先看名字，
// 再决定"新建还是同名覆盖"。
func PeekZip(zipBytes []byte) (ZipMeta, error) {
	tmp, err := os.MkdirTemp("", "skill-peek-*")
	if err != nil {
		return ZipMeta{}, err
	}
	defer os.RemoveAll(tmp)
	rootRel, err := extractZip(zipBytes, tmp, "技能")
	if err != nil {
		return ZipMeta{}, err
	}
	text, err := os.ReadFile(filepath.Join(tmp, filepath.FromSlash(rootRel), "SKILL.md"))
	if err != nil {
		return ZipMeta{}, err
	}
	parsed := ParseSkillMD(string(text))
	if parsed.Name == "" {
		return ZipMeta{}, errors.NewMsg("SKILL.md frontmatter 缺少 name 字段")
	}
	return ZipMeta{Meta: parsed.Meta, RootDir: rootRel}, nil
}

// InstallFromZip 把 zip 安装到技能库 skills/<dirName>/（已存在则整体替换），
// 返回从技能根抽出的 secrets（zip 未携带时为空 map，调用方不应据此清空已有机密）。
func InstallFromZip(zipBytes []byte, dirName string) (map[string]string, error) {
	tmp, err := os.MkdirTemp("", "skill-install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	rootRel, err := extractZip(zipBytes, tmp, "技能")
	if err != nil {
		return nil, err
	}
	srcRoot := filepath.Join(tmp, filepath.FromSlash(rootRel))

	// 抽出技能根的 secrets 文件，不进工作副本
	extracted := map[string]string{}
	for name := range secretsFileNames {
		p := filepath.Join(srcRoot, name)
		if data, err := os.ReadFile(p); err == nil {
			extracted = mergeEnv(extracted, parseEnvFile(string(data)))
			_ = os.Remove(p)
		}
	}

	// 整树替换安装
	target := filepath.Join(rootDir, dirName)
	if err := os.RemoveAll(target); err != nil {
		return nil, errors.Wrapf(err, "清理旧技能目录失败: %s", dirName)
	}
	if err := copyTree(srcRoot, target); err != nil {
		return nil, errors.Wrapf(err, "安装技能文件失败: %s", dirName)
	}
	return extracted, nil
}

func mergeEnv(dst, src map[string]string) map[string]string {
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// WriteSecrets 整体替换技能的机密文件 .secrets/<dir>.env（键排序，输出稳定）
func WriteSecrets(dirName string, kv map[string]string) error {
	dir := filepath.Join(rootDir, ".secrets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errors.Wrap(err, "创建机密目录失败")
	}
	var b strings.Builder
	for _, k := range sortedKeys(kv) {
		fmt.Fprintf(&b, "%s=%s\n", k, kv[k])
	}
	if err := os.WriteFile(filepath.Join(dir, dirName+".env"), []byte(b.String()), 0o600); err != nil {
		return errors.Wrap(err, "写入机密文件失败")
	}
	return nil
}

// RemoveSkill 删除技能目录与其机密文件（注册表行由调用方清理）
func RemoveSkill(dirName string) error {
	if dirName == "" || dirName == "." || dirName == ".." || strings.ContainsAny(dirName, `/\`) {
		return errors.NewMsg("非法技能目录名")
	}
	if err := os.RemoveAll(filepath.Join(rootDir, dirName)); err != nil {
		return errors.Wrapf(err, "删除技能目录失败: %s", dirName)
	}
	_ = os.Remove(filepath.Join(rootDir, ".secrets", dirName+".env"))
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
