// skills.go 阶段五：turn 级技能装配与 load_skill 自主装载。
//
// 装配分两层（见 README 5.3）：
//   - 会话启用（manual）的技能在 turn 开始时全量挂载进沙箱工作区 skills/<dir>/，
//     secrets 合并进 turn 环境变量；
//   - 其余技能（会话开启 auto_skill 且可见）构成 Catalog，仅注入"名字 + 摘要"
//     目录段；模型按意图调用 load_skill 时由 runLoadSkill 拦截装载——挂载文件、
//     合并 secrets、回传 SKILL.md，并以 source=auto 持久化关联。
//
// load_skill 必须先于同轮其它工具串行执行（它会改写 turn 内的 secrets 合并表，
// 并行的话后跑的工具可能拿到还没合并机密的旧环境）。
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/errors"
	"backend/pkg/skill"
	"backend/pkg/tools"
)

// MaxAutoSkillLoads 单轮允许 load_skill 自动装载的技能数上限，
// 防止模型把目录挨个加载一遍吃光上下文
const MaxAutoSkillLoads = 3

// catalogItem 目录条目：技能库注册行 + 元数据（Name 匹配用注册表名字）
type catalogItem struct {
	skill.Entry
	ID int64
}

// turnSkills 一轮 turn 的技能状态（load_skill 命中时会往里追加）
type turnSkills struct {
	// enabled 已挂载的启用技能（mount 失败的会被剔除，避免 prompt 撒谎）
	enabled []skill.Entry
	// enabledByName name -> dir_name，load_skill 未命中时区分
	// "压根没有"与"已启用无需加载"
	enabledByName map[string]string
	// catalog 可按需加载的候选（会话开启 auto_skill 时非空）
	catalog       []catalogItem
	catalogByName map[string]catalogItem
	// envs 已启用/已装载技能的 secrets 合并表（bash / python_exec 注入）
	envs map[string]string
	// autoLoaded 本轮经 load_skill 装载的技能名（去重与上限用）
	autoLoaded []string
}

// loadTurnSkills 装配 turn 的起始技能状态并挂载启用技能。
// 技能是增强能力而非对话前提：任何一步失败都降级（告警 + 跳过），
// 不让技能库故障拖死整轮对话。
func loadTurnSkills(ctx context.Context, session *model.Session) *turnSkills {
	ts := &turnSkills{
		enabledByName: map[string]string{},
		catalogByName: map[string]catalogItem{},
		envs:          map[string]string{},
	}
	rows, err := dao.ListSessionSkills(ctx, session.SessionID)
	if err != nil {
		slog.Warn("加载会话启用技能失败，本轮降级为无技能对话", "session", session.SessionID, "err", errors.Stack(err))
		return ts
	}

	// 分流：全量挂载（full / auto_loaded）vs 智能按需候选（auto）
	var fullRows []dao.SessionSkillRow
	var autoRows []dao.SessionSkillRow
	for _, row := range rows {
		if row.Source == "auto" {
			autoRows = append(autoRows, row)
		} else {
			fullRows = append(fullRows, row)
		}
	}

	// 1. 挂载常驻全量技能
	for _, row := range fullRows {
		entry := skill.Entry{DirName: row.Skill.DirName, Name: row.Skill.Name, Description: row.Skill.Description}
		if _, err := skill.MountToSandbox(ctx, session.SessionID, row.Skill.DirName); err != nil {
			slog.Warn("挂载启用技能失败，本轮跳过该技能", "session", session.SessionID, "skill", row.Skill.DirName, "err", errors.Stack(err))
			continue
		}
		ts.enabled = append(ts.enabled, entry)
		ts.enabledByName[entry.Name] = entry.DirName
	}
	ts.envs = skill.CollectEnvs(dirNames(ts.enabled))

	// 2. 组装按需候选 Catalog（严格互斥去重：已在 full 中的绝不重复进 catalog）
	if !session.AutoSkill {
		return ts
	}

	if len(autoRows) > 0 {
		for _, row := range autoRows {
			if _, exists := ts.enabledByName[row.Skill.Name]; exists {
				continue // 去重：已全量挂载的剔除
			}
			if row.Skill.Confidential && (row.Skill.OwnerID == 0 || row.Skill.OwnerID != session.UserID) {
				continue
			}
			item := catalogItem{
				Entry: skill.Entry{DirName: row.Skill.DirName, Name: row.Skill.Name, Description: row.Skill.Description},
				ID:    row.Skill.ID,
			}
			ts.catalog = append(ts.catalog, item)
			ts.catalogByName[item.Name] = item
		}
	} else {
		// 降级兜底：若会话未单独配置按需列表，但开启了 auto_skill，则全库未启用技能进 Catalog
		all, err := dao.ListSkills(ctx)
		if err != nil {
			slog.Warn("加载技能目录失败，本轮不提供按需装载", "session", session.SessionID, "err", errors.Stack(err))
			return ts
		}
		for _, s := range all {
			if _, exists := ts.enabledByName[s.Name]; exists {
				continue
			}
			if s.Confidential && (s.OwnerID == 0 || s.OwnerID != session.UserID) {
				continue
			}
			item := catalogItem{
				Entry: skill.Entry{DirName: s.DirName, Name: s.Name, Description: s.Description},
				ID:    s.ID,
			}
			ts.catalog = append(ts.catalog, item)
			ts.catalogByName[item.Name] = item
		}
	}

	return ts
}

func dirNames(entries []skill.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.DirName)
	}
	return out
}

// systemPromptSection 追加到 system 提示的技能段（空串表示无技能内容）
func (ts *turnSkills) systemPromptSection() string {
	var sections []string
	if s := skill.BuildSkillsPrompt(ts.enabled); s != "" {
		sections = append(sections, s)
	}
	if s := skill.BuildCatalogPrompt(ts.catalogEntries()); s != "" {
		sections = append(sections, s)
	}
	return strings.Join(sections, "\n\n")
}

// catalogEntries 目录条目（prompt 构建用）
func (ts *turnSkills) catalogEntries() []skill.Entry {
	out := make([]skill.Entry, 0, len(ts.catalog))
	for _, c := range ts.catalog {
		out = append(out, c.Entry)
	}
	return out
}

// hasCatalog 是否存在可按需装载的候选（决定是否暴露 load_skill 工具）
func (ts *turnSkills) hasCatalog() bool { return len(ts.catalog) > 0 }

// runLoadSkill 拦截执行 load_skill：挂载目录 + 合并 secrets + 回传 SKILL.md。
// 装载成功时同时返回 skill_loaded 事件（由调用方 emit，前端同步启用列表并提示）。
// 结果文案均为模型可自纠正的普通文本（非 [执行失败] 前缀），前端不会弹失败卡；
// 只有挂载基础设施失败才带错误前缀。
func (ts *turnSkills) runLoadSkill(ctx context.Context, sessionID string, tc savedCall) (tools.Output, *Event) {
	args, parseErr := tools.ParseArguments(tc.Function.Arguments)
	if parseErr != "" {
		return tools.Output{Text: parseErr}, nil
	}
	rawName := strings.TrimSpace(fmt.Sprint(args["name"]))

	item, ok := ts.catalogByName[rawName]
	if !ok {
		if dir, enabled := ts.enabledByName[rawName]; enabled {
			return tools.Output{Text: fmt.Sprintf(
				"[无需加载] `%s` 已在本会话启用，文件就在 %s/%s/ 下，直接按 SKILL.md 操作即可。" +
					"（已启用的技能不出现在待加载目录里，所以 load_skill 找不到它，这不代表它不存在。）",
				rawName, skill.MountDir, dir)}, nil
		}
		avail := "（无）"
		if names := ts.catalogNames(); len(names) > 0 {
			avail = strings.Join(names, "、")
		}
		enabledList := "（无）"
		if names := ts.enabledNames(); len(names) > 0 {
			enabledList = strings.Join(names, "、")
		}
		return tools.Output{Text: fmt.Sprintf(
			"[未找到] 目录里没有名为 `%s` 的技能。当前可加载：%s；已启用（无需加载，直接用）：%s。"+
				"如果两边都没有匹配的，就不要再调用 load_skill，直接用你自己的能力回答。",
			rawName, avail, enabledList)}, nil
	}
	if containsStr(ts.autoLoaded, rawName) {
		return tools.Output{Text: fmt.Sprintf(
			"[已加载] `%s` 本轮已经加载过了，文件在 %s/%s/ 下，直接用即可。",
			rawName, skill.MountDir, item.DirName)}, nil
	}
	if len(ts.autoLoaded) >= MaxAutoSkillLoads {
		return tools.Output{Text: fmt.Sprintf(
			"[已达上限] 本轮已自动加载 %d 个技能，不能再加载了。请用已加载的技能完成任务，"+
				"或告诉用户需要在技能面板里手工启用 `%s`。", MaxAutoSkillLoads, rawName)}, nil
	}

	if _, err := skill.MountToSandbox(ctx, sessionID, item.DirName); err != nil {
		slog.Warn("自动装载技能挂载失败", "session", sessionID, "skill", item.DirName, "err", errors.Stack(err))
		return tools.Output{Text: "[执行失败] 挂载技能 `" + rawName + "` 失败：" + err.Error()}, nil
	}
	body, err := skill.ReadSkillMD(item.DirName)
	if err != nil {
		return tools.Output{Text: fmt.Sprintf(
			"[加载异常] `%s` 已挂到 %s/%s/ 下，但读取它的 SKILL.md 失败（%v）。"+
				"可以用 bash 看看目录里有什么，或告诉用户这个技能安装不完整。",
			rawName, skill.MountDir, item.DirName, err)}, nil
	}

	// 生效顺序要紧：先合并 secrets 与装载记录，再持久化、再回结果——
	// 保证同一轮里紧接着跑的 bash / python_exec 已经拿得到机密
	for k, v := range skill.ReadSecrets(item.DirName) {
		ts.envs[k] = v
	}
	ts.autoLoaded = append(ts.autoLoaded, rawName)
	ts.enabled = append(ts.enabled, item.Entry)
	ts.enabledByName[rawName] = item.DirName
	delete(ts.catalogByName, rawName)
	for idx, c := range ts.catalog {
		if c.ID == item.ID {
			ts.catalog = append(ts.catalog[:idx], ts.catalog[idx+1:]...)
			break
		}
	}
	if err := dao.AddOrPromoteSessionSkill(ctx, sessionID, item.ID); err != nil {
		// 持久化失败不影响本轮使用，只是刷新后关联丢失，告警即可
		slog.Warn("自动装载技能关联持久化失败", "session", sessionID, "skill", item.DirName, "err", errors.Stack(err))
	}

	if fullLen := len([]rune(body)); fullLen > skill.SkillMDPreviewLimit {
		body = string([]rune(body)[:skill.SkillMDPreviewLimit]) +
			fmt.Sprintf("\n\n…（SKILL.md 共 %d 字符，此处截断。需要后面的内容时用 read_file 读 %s/%s/SKILL.md）",
				fullLen, skill.MountDir, item.DirName)
	}
	slog.Info("技能自动装载成功", "session", sessionID, "skill", rawName, "dir", item.DirName, "md_len", len(body))
	return tools.Output{Text: fmt.Sprintf(
		"已加载技能 `%s`，文件在工作区 %s/%s/ 下（只读）。它配置的机密密钥已注入进程环境变量。以下是 SKILL.md：\n\n%s",
		rawName, skill.MountDir, item.DirName, body)}, &Event{Type: "skill_loaded", ID: tc.ID, Name: rawName, SkillID: item.ID}
}

func (ts *turnSkills) catalogNames() []string {
	out := make([]string, 0, len(ts.catalog))
	for _, c := range ts.catalog {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

func (ts *turnSkills) enabledNames() []string {
	out := make([]string, 0, len(ts.enabled))
	for _, e := range ts.enabled {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}
