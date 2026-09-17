// Package skill 动态 Skill 插件引擎（阶段五核心）。
//
// 技能库以文件系统为唯一源头：每个 skill 是技能库根目录下的一个子目录，
// 以 SKILL.md 为入口声明（YAML frontmatter 元数据 + Markdown 正文指引），
// 其余文件（脚本、数据）随目录整体挂载进会话沙箱。
//
// 装配模式分两层（见 README 5.3）：
//   - 已启用技能：文件全量挂载到沙箱工作区 skills/<dir>/，System Prompt
//     注入完整说明段；
//   - 未启用技能：仅以「名字 + 用途摘要」进入 Skill Catalog 目录段，
//     模型按意图调用 load_skill 动态装载（解析与拦截在 pkg/agent）。
//
// 机密密钥（secrets）单独存放在 .secrets/<dir>.env，不随技能文件挂载进
// 沙箱；仅在技能启用/装载时合并为进程环境变量注入（见 tools.WithEnv）。
package skill

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxTagLen / MaxTags 标签规范化上限：防止把长句塞进标签字段
const (
	MaxTagLen = 16
	MaxTags   = 8
)

// Meta SKILL.md frontmatter 的解析结果。Parameters 为可选的类 JSON-Schema
// 入参契约（供开放 API 异步任务使用，阶段六消费；目录与 Prompt 只用
// name / description）。
type Meta struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Tags        []string       `yaml:"tags"`
	Parameters  map[string]any `yaml:"parameters"`
}

// ParsedSkill 一份 SKILL.md 的完整解析产物
type ParsedSkill struct {
	Meta
	Body string // 去掉 frontmatter 后的 Markdown 正文（使用指引）
}

// frontmatterRe 匹配文件头部的 YAML frontmatter 块（--- ... ---）。
// (?s) 让 . 跨行——frontmatter 天然是多行的
var frontmatterRe = regexp.MustCompile(`(?s)\A---\s*\n(.*?)\n---\s*\n?`)

// ParseSkillMD 解析 SKILL.md 文本：拆出 YAML frontmatter 与正文。
// frontmatter 缺失或不是合法 YAML 时按"无元数据"处理（Body 为全文），
// 由调用方决定是否因缺 name 而拒绝该 skill。
func ParseSkillMD(text string) ParsedSkill {
	var p ParsedSkill
	p.Body = text
	m := frontmatterRe.FindStringSubmatch(text)
	if m == nil {
		return p
	}
	p.Body = strings.TrimPrefix(text, m[0])
	// 宽松解析：未知字段忽略，类型不符的字段退化为零值而不是整份报废
	_ = yaml.Unmarshal([]byte(m[1]), &p.Meta)
	p.Meta.Name = strings.TrimSpace(p.Meta.Name)
	p.Meta.Description = strings.TrimSpace(p.Meta.Description)
	p.Meta.Tags = NormalizeTags(p.Meta.Tags)
	return p
}

// NormalizeTags 标签清洗：去空白、截长、去重（保序）、限量
func NormalizeTags(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		tag := strings.TrimSpace(item)
		if len(tag) > MaxTagLen {
			tag = strings.TrimSpace(tag[:MaxTagLen])
		}
		if tag == "" {
			continue
		}
		dup := false
		for _, v := range out {
			if v == tag {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, tag)
		}
		if len(out) >= MaxTags {
			break
		}
	}
	return out
}

// dirNameRe 目录名仅保留字母、数字、下划线与连字符，其余折叠为连字符
var dirNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// SanitizeDirName 由 skill 名生成安全的目录名（小写；清洗后无效时回退 skill）
func SanitizeDirName(name string) string {
	s := strings.ToLower(dirNameRe.ReplaceAllString(strings.TrimSpace(name), "-"))
	s = strings.Trim(s, "-")
	if s == "" {
		return "skill"
	}
	return s
}
