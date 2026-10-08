// prompt.go Skill 相关的 System Prompt 装配段。
//
// 两段职责严格区分（见 README 5.3 Catalog 模式）：
//   - BuildSkillsPrompt：已启用技能——文件已挂载、secrets 已注入，给完整使用说明；
//   - BuildCatalogPrompt：未启用技能——只有名字与摘要，必须先 load_skill 才能用。
package skill

import (
	"fmt"
	"strings"
)

// CatalogDescLimit 目录里每条描述的截断长度。description 往往写得很长，
// 全量塞进去会把目录段撑到几千 token；截到这个长度足够模型判断"要不要"。
const CatalogDescLimit = 160

// SkillMDPreviewLimit load_skill 回传的 SKILL.md 正文上限，防止巨型 SKILL.md
// 一次吃光上下文；截断后提示模型需要细节时自己 read_file 续读。
const SkillMDPreviewLimit = 8000

// truncate 按 rune 截断并追加省略号
func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= limit {
		return text
	}
	cut := string([]rune(text)[:limit])
	return strings.TrimRight(cut, " \t\n") + "…"
}

// BuildSkillsPrompt 已启用技能的说明段：文件在工作区 skills/<dir>/ 下，
// 模型按 SKILL.md 指引经 bash / python_exec 调用其中脚本。
func BuildSkillsPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	lines := []string{
		"## 可用 Skills（已挂载）",
		"",
		"以下技能已完成沙箱工作区挂载并在当前会话直接可用：",
		"每个技能在工作区中的完整路径为 `" + MountDir + "/<目录名>/`。若需要查阅完整使用说明，请使用 `read_file` 读取其入口文件 `" + MountDir + "/<目录名>/SKILL.md`，随后按指引用 bash 或 python_exec 调用其中的脚本。",
		"运行技能脚本的标准形式：`cd " + MountDir + "/<目录名> && python3 scripts/xxx.py`——bash 的当前目录始终是工作区根、不是脚本所在目录，脚本依赖同目录文件时必须先 cd 进技能目录再执行。",
		"技能配置的机密密钥已直接注入当前进程环境变量（可直接从 os.environ 读取）。",
		"",
	}
	for _, e := range entries {
		desc := e.Description
		if desc == "" {
			desc = "（无描述）"
		}
		lines = append(lines, fmt.Sprintf("- **%s**（%s/）：%s（入口文件：`%s/%s/SKILL.md`）", e.Name, e.DirName, desc, MountDir, e.DirName))
	}
	return strings.Join(lines, "\n")
}

// BuildCatalogPrompt 未启用技能的目录段：描述截断、文件未挂载，
// 真要用必须先 load_skill（schema 仅在目录非空时暴露，见 pkg/agent）。
func BuildCatalogPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	lines := []string{
		"## Skill 目录（按需加载）",
		"",
		"平台里还有下面这些 skill。它们**尚未加载**——工作区里没有它们的文件，",
		"你现在看到的只是名字和用途摘要。",
		"当用户的需求明显落在某个 skill 的用途范围内时，先调用 `load_skill(name)`，",
		"拿到完整说明后再按说明操作；用途不匹配就正常回答，**不要为了用而用**。",
		"",
		"注意：不要用 read_file / bash（ls、find 等）去探测或读取未加载技能的文件——",
		"它们不在工作区里，必然扑空；使用未加载技能的唯一入口是 load_skill(name)，",
		"它的返回已经包含你需要的全部说明。",
		"",
	}
	for _, e := range entries {
		desc := truncate(e.Description, CatalogDescLimit)
		if desc == "" {
			desc = "（无描述）"
		}
		lines = append(lines, fmt.Sprintf("- `%s`：%s", e.Name, desc))
	}
	lines = append(lines, "", "判断不了要不要用时，优先直接回答或反问用户，而不是把目录里的 skill 挨个加载一遍。")
	return strings.Join(lines, "\n")
}
