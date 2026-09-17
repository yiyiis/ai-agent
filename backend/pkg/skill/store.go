// store.go 技能库文件存储：目录扫描、SKILL.md 读取、secrets 管理与沙箱挂载。
//
// 目录布局（root 默认为后端运行目录下的 skills/）：
//
//	skills/
//	  <dir-name>/SKILL.md      入口声明（必需）
//	  <dir-name>/scripts/...   执行脚本与配套文件（随目录挂载）
//	  .secrets/<dir-name>.env  机密密钥（KEY=value；不挂载进沙箱，仅注入环境变量）
package skill

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"backend/pkg/errors"
	"backend/pkg/sandbox"
)

// defaultRoot 技能库根目录默认值（相对后端运行目录）
const defaultRoot = "skills"

// MountDir 挂载到沙箱工作区时的根目录名（工作区内相对路径 skills/<dir>/）
const MountDir = "skills"

// 挂载防护上限：防止病态技能目录（上万文件 / 巨型二进制）拖垮传输与沙箱
const (
	maxMountFiles     = 200
	maxMountTotalByte = 16 << 20 // 16MB
)

// Config 技能库配置段（包自持配置、由 config 包组合，yaml 键与 etc/config.yaml 对齐）
type Config struct {
	Dir string `yaml:"Dir" mapstructure:"Dir"` // 技能库根目录，空则默认 skills/
}

var rootDir = defaultRoot

// Init 装配技能库根目录（main 启动时调用一次；未调用时开箱即用默认值）
func Init(cfg Config) {
	if cfg.Dir != "" {
		rootDir = cfg.Dir
	}
}

// Root 技能库根目录
func Root() string { return rootDir }

// Entry 技能库目录里一个可装载的 skill（Catalog 条目 / 启用清单元素）
type Entry struct {
	DirName     string   `json:"dir_name"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}

// ListDir 扫描技能库根目录，返回全部含合法 SKILL.md 的技能条目（按名字排序）。
// 目录缺失时返回空列表（技能库允许为空）；个别目录解析失败只告警不中断，
// 坏一个 skill 不该让整个技能体系下线。
func ListDir() []Entry {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("技能库目录读取失败", "dir", rootDir, "err", errors.Stack(err))
		}
		return nil
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // 跳过 .secrets 等内部目录
		}
		parsed, ok := readDirEntry(e.Name())
		if !ok {
			continue
		}
		out = append(out, Entry{
			DirName:     e.Name(),
			Name:        parsed.Name,
			Description: parsed.Description,
			Tags:        parsed.Tags,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// readDirEntry 读取并校验单个技能目录的 SKILL.md
func readDirEntry(dirName string) (Meta, bool) {
	text, err := os.ReadFile(filepath.Join(rootDir, dirName, "SKILL.md"))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("SKILL.md 读取失败", "skill", dirName, "err", errors.Stack(err))
		}
		return Meta{}, false
	}
	parsed := ParseSkillMD(string(text))
	if parsed.Name == "" {
		slog.Warn("SKILL.md frontmatter 缺少 name 字段，跳过", "skill", dirName)
		return Meta{}, false
	}
	return parsed.Meta, true
}

// ReadSkillMD 读取技能的 SKILL.md 全文（正文 + frontmatter）；不存在返回错误
func ReadSkillMD(dirName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(rootDir, dirName, "SKILL.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.NewMsg("skill `" + dirName + "` 缺少 SKILL.md，安装不完整")
		}
		return "", errors.Wrap(err, "读取 SKILL.md 失败")
	}
	return string(data), nil
}

// ---------- secrets：.secrets/<dir>.env ----------

// parseEnvFile 极简 .env 解析：KEY=value，支持引号与 # 注释行
func parseEnvFile(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// ReadSecrets 读取技能的机密密钥；未配置返回空表（读取失败降级为空并告警，
// 机密缺失表现为"变量拿不到值"，技能脚本自行报错比整轮对话失败更可接受）
func ReadSecrets(dirName string) map[string]string {
	data, err := os.ReadFile(filepath.Join(rootDir, ".secrets", dirName+".env"))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("skill secrets 读取失败", "skill", dirName, "err", errors.Stack(err))
		}
		return map[string]string{}
	}
	return parseEnvFile(string(data))
}

// CollectEnvs 合并多个技能的 secrets（后者覆盖前者；键冲突极少见）
func CollectEnvs(dirNames []string) map[string]string {
	merged := map[string]string{}
	for _, d := range dirNames {
		for k, v := range ReadSecrets(d) {
			merged[k] = v
		}
	}
	return merged
}

// ---------- 沙箱挂载 ----------

// MountToSandbox 把技能目录的全部文件写入会话沙箱工作区的 skills/<dirName>/
// 下（ephemeral 工作区模型：每轮重挂载，与附件物化同策略，三驱动语义一致）。
// 返回挂载的文件数。
func MountToSandbox(ctx context.Context, sessionID, dirName string) (int, error) {
	srcRoot := filepath.Join(rootDir, dirName)
	var files []string
	var total int64
	// 先收集清单与体积（越界直接拒绝，不挂一半）
	err := filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != srcRoot && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir // 跳过 .git 等隐藏目录
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if len(files) >= maxMountFiles {
			return errors.NewMsg("skill `" + dirName + "` 文件数超过 " + strconv.Itoa(maxMountFiles) + " 上限，拒绝挂载")
		}
		total += info.Size()
		if total > maxMountTotalByte {
			return errors.NewMsg("skill `" + dirName + "` 总体积超过 16MB 上限，拒绝挂载")
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return 0, errors.Wrapf(err, "扫描 skill 目录失败: %s", dirName)
	}

	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return 0, errors.Wrapf(err, "读取 skill 文件失败: %s", dirName)
		}
		rel, err := filepath.Rel(srcRoot, p)
		if err != nil {
			return 0, errors.Wrapf(err, "解析 skill 文件相对路径失败: %s", dirName)
		}
		// 工作区统一使用斜杠分隔的相对路径（local/docker/e2b 驱动一致）
		dst := path.Join(MountDir, dirName, filepath.ToSlash(rel))
		if err := sandbox.WriteFile(ctx, sessionID, dst, data); err != nil {
			return 0, errors.Wrapf(err, "挂载 skill 文件进沙箱失败: %s", dirName)
		}
	}
	return len(files), nil
}
