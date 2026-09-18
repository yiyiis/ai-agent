// skill.go 技能注册表与会话技能关联的数据访问。
//
// skills 表是文件系统技能库（backend/skills/）的注册表投影：目录内容为唯一
// 源头，SyncSkills 把扫描结果同步进表（新增 / 更新元数据 / 清理已删除目录，
// 保留 id 与归属等运营字段）；session_skills 记录会话启用的技能，load_skill
// 自动装载以 source=auto 落同一张表。
package dao

import (
	"context"
	"encoding/json"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"

	"gorm.io/gorm"
)

// SkillRegistryItem 文件系统扫描出的技能注册项（TagsJSON 为 JSON 数组文本或空）
type SkillRegistryItem struct {
	Name        string
	Description string
	DirName     string
	TagsJSON    string
}

// SyncSkills 以 dir_name 为键把文件系统技能库同步进 skills 注册表：
// 新目录插入、元数据变化更新、目录已删除的行连同 session_skills 关联清理。
// owner / confidential 等运营字段只属于注册表，同步不触碰。
func SyncSkills(ctx context.Context, items []SkillRegistryItem) error {
	q := db.Ctx(ctx)
	sk := q.Skill

	existing, err := sk.WithContext(ctx).Find()
	if err != nil {
		return errors.Join(err, errors.New("查询技能注册表失败"), errors.NewMsg("系统异常"))
	}
	byDir := make(map[string]*model.Skill, len(existing))
	byName := make(map[string]*model.Skill, len(existing))
	for _, row := range existing {
		byDir[row.DirName] = row
		byName[row.Name] = row
	}

	itemByDir := make(map[string]SkillRegistryItem, len(items))
	itemByName := make(map[string]SkillRegistryItem, len(items))
	for _, it := range items {
		itemByDir[it.DirName] = it
		itemByName[it.Name] = it
		row, ok := byDir[it.DirName]
		if !ok {
			// 若按目录未匹配，尝试按唯一 Name 匹配（应对目录重命名场景，避免触发唯一键冲突）
			row, ok = byName[it.Name]
		}
		if !ok {
			tags := tagsPtr(it.TagsJSON)
			if err := sk.WithContext(ctx).Create(&model.Skill{
				Name:        it.Name,
				Description: it.Description,
				DirName:     it.DirName,
				Tags:        tags,
			}); err != nil {
				return errors.Join(err, errors.New("写入技能注册表失败"), errors.NewMsg("系统异常"))
			}
			continue
		}
		if row.Name == it.Name && row.DirName == it.DirName && row.Description == it.Description && strEqPtr(row.Tags, tagsPtr(it.TagsJSON)) {
			continue // 无变化，免写
		}
		updates := map[string]interface{}{
			"name":        it.Name,
			"dir_name":    it.DirName,
			"description": it.Description,
		}
		if tags := tagsPtr(it.TagsJSON); tags != nil {
			updates["tags"] = *tags
		}
		if _, err := sk.WithContext(ctx).Where(sk.ID.Eq(row.ID)).UpdateColumns(updates); err != nil {
			return errors.Join(err, errors.New("更新技能注册表失败"), errors.NewMsg("系统异常"))
		}
	}

	// 目录里已不存在的注册行：连带清理会话关联（技能没了，关联没有意义）
	var staleIDs []int64
	for _, row := range existing {
		if _, ok := itemByDir[row.DirName]; !ok {
			if _, okByName := itemByName[row.Name]; !okByName {
				staleIDs = append(staleIDs, row.ID)
			}
		}
	}
	if len(staleIDs) > 0 {
		ss := q.SessionSkill
		if _, err := ss.WithContext(ctx).Where(ss.SkillID.In(staleIDs...)).Delete(); err != nil {
			return errors.Join(err, errors.New("清理失效技能关联失败"), errors.NewMsg("系统异常"))
		}
		if _, err := sk.WithContext(ctx).Where(sk.ID.In(staleIDs...)).Delete(); err != nil {
			return errors.Join(err, errors.New("清理失效技能注册行失败"), errors.NewMsg("系统异常"))
		}
	}
	return nil
}

func tagsPtr(tagsJSON string) *string {
	if tagsJSON == "" || tagsJSON == "null" {
		return nil
	}
	return &tagsJSON
}

func strEqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ListSkills 注册表全量技能（按名字排序）
func ListSkills(ctx context.Context) ([]*model.Skill, error) {
	q := db.Ctx(ctx)
	skills, err := q.Skill.WithContext(ctx).Order(q.Skill.Name).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询技能列表失败"), errors.NewMsg("系统异常"))
	}
	return skills, nil
}

// GetSkillsByIDs 按主键集合查询技能（存在性校验与启用装配用）
func GetSkillsByIDs(ctx context.Context, ids []int64) ([]*model.Skill, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := db.Ctx(ctx)
	skills, err := q.Skill.WithContext(ctx).Where(q.Skill.ID.In(ids...)).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询技能失败"), errors.NewMsg("系统异常"))
	}
	return skills, nil
}

// SessionSkillRow 会话启用的一项技能（含锁定的版本号，NULL 为跟随最新）
type SessionSkillRow struct {
	Skill  model.Skill
	Pinned *int64
	Source string
}

// ListSessionSkills 查询会话启用的全部技能（含手动启用与 load_skill 自动装载）
func ListSessionSkills(ctx context.Context, sessionID string) ([]SessionSkillRow, error) {
	q := db.Ctx(ctx)
	sk, ss := q.Skill, q.SessionSkill
	rows, err := ss.WithContext(ctx).
		Where(ss.SessionID.Eq(sessionID)).
		Order(ss.ID).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询会话技能失败"), errors.NewMsg("系统异常"))
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.SkillID)
	}
	skills, err := sk.WithContext(ctx).Where(sk.ID.In(ids...)).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询会话技能失败"), errors.NewMsg("系统异常"))
	}
	byID := make(map[int64]*model.Skill, len(skills))
	for _, s := range skills {
		byID[s.ID] = s
	}
	out := make([]SessionSkillRow, 0, len(rows))
	for _, r := range rows {
		s, ok := byID[r.SkillID]
		if !ok {
			continue // 注册行被并发清理：跳过悬空关联
		}
		out = append(out, SessionSkillRow{Skill: *s, Pinned: r.PinnedVersionNumber, Source: r.Source})
	}
	return out, nil
}

// ListEnabledSkillIDs 查询会话启用的技能 ID（详情回显用）
func ListEnabledSkillIDs(ctx context.Context, sessionID string) ([]int64, error) {
	q := db.Ctx(ctx)
	ss := q.SessionSkill
	rows, err := ss.WithContext(ctx).Where(ss.SessionID.Eq(sessionID)).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询会话技能失败"), errors.NewMsg("系统异常"))
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.SkillID)
	}
	return ids, nil
}

// SessionSkillItem 会话启用技能的写入项
type SessionSkillItem struct {
	SkillID             int64
	PinnedVersionNumber *int64
	Source              string // "full", "auto", "auto_loaded"
}

// ReplaceSessionSkills 整体替换会话启用的技能集合（PUT 语义，事务内删旧插新）
func ReplaceSessionSkills(ctx context.Context, sessionID string, items []SessionSkillItem) error {
	return db.Transition(ctx, func(txCtx context.Context) error {
		q := db.Ctx(txCtx)
		ss := q.SessionSkill
		if _, err := ss.WithContext(txCtx).Where(ss.SessionID.Eq(sessionID)).Delete(); err != nil {
			return errors.Join(err, errors.New("更新会话技能失败"), errors.NewMsg("系统异常"))
		}
		if len(items) == 0 {
			return nil
		}
		rows := make([]*model.SessionSkill, 0, len(items))
		for _, it := range items {
			source := it.Source
			if source == "" {
				source = "full"
			}
			rows = append(rows, &model.SessionSkill{
				SessionID:           sessionID,
				SkillID:             it.SkillID,
				PinnedVersionNumber: it.PinnedVersionNumber,
				Source:              source,
			})
		}
		if err := ss.WithContext(txCtx).Create(rows...); err != nil {
			return errors.Join(err, errors.New("更新会话技能失败"), errors.NewMsg("系统异常"))
		}
		return nil
	})
}

// AddOrPromoteSessionSkill 记录或提升 load_skill 自动装载（若之前是 auto 候选，提升为 auto_loaded）
func AddOrPromoteSessionSkill(ctx context.Context, sessionID string, skillID int64) error {
	q := db.Ctx(ctx)
	ss := q.SessionSkill
	row, err := ss.WithContext(ctx).
		Where(ss.SessionID.Eq(sessionID), ss.SkillID.Eq(skillID)).
		First()
	if err != nil {
		return ss.WithContext(ctx).Create(&model.SessionSkill{
			SessionID: sessionID,
			SkillID:   skillID,
			Source:    "auto_loaded",
		})
	}
	if row.Source == "auto" {
		_, _ = ss.WithContext(ctx).
			Where(ss.ID.Eq(row.ID)).
			Update(ss.Source, "auto_loaded")
	}
	return nil
}

// AddSessionSkillIfAbsent 记录 load_skill 自动装载（兼容旧名）
func AddSessionSkillIfAbsent(ctx context.Context, sessionID string, skillID int64) error {
	return AddOrPromoteSessionSkill(ctx, sessionID, skillID)
}

// MarshalTags 标签切片 → skills.tags 列的 JSON 文本（空切片返回空串）
func MarshalTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(b)
}

// GetSkillByID 按主键查询技能（不存在返回 nil）
func GetSkillByID(ctx context.Context, id int64) (*model.Skill, error) {
	q := db.Ctx(ctx)
	row, err := q.Skill.WithContext(ctx).Where(q.Skill.ID.Eq(id)).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询技能失败"), errors.NewMsg("系统异常"))
	}
	return row, nil
}

// GetSkillByDirName 按目录名查询技能（上传判定"新建还是覆盖"用）
func GetSkillByDirName(ctx context.Context, dirName string) (*model.Skill, error) {
	q := db.Ctx(ctx)
	row, err := q.Skill.WithContext(ctx).Where(q.Skill.DirName.Eq(dirName)).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询技能失败"), errors.NewMsg("系统异常"))
	}
	return row, nil
}

// UploadedSkillItem 上传安装的注册行（含归属与机密标记，创建时落库、更新时保留归属）
type UploadedSkillItem struct {
	Name         string
	Description  string
	DirName      string
	TagsJSON     *string
	OwnerID      int64
	OwnerName    string
	Confidential bool
}

// UpsertUploadedSkill 上传路径的注册行 upsert（按 dir_name）：不存在则建行
//（带归属），存在则只更新内容字段（name / description / tags / confidential），
// 归属字段不迁移。
func UpsertUploadedSkill(ctx context.Context, item UploadedSkillItem) (*model.Skill, error) {
	q := db.Ctx(ctx)
	existing, err := GetSkillByDirName(ctx, item.DirName)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		row := &model.Skill{
			Name:         item.Name,
			Description:  item.Description,
			DirName:      item.DirName,
			OwnerID:      item.OwnerID,
			OwnerName:    item.OwnerName,
			Confidential: item.Confidential,
			Tags:         item.TagsJSON,
		}
		if err := q.Skill.WithContext(ctx).Create(row); err != nil {
			return nil, errors.Join(err, errors.New("写入技能注册表失败"), errors.NewMsg("系统异常"))
		}
		return row, nil
	}
	updates := map[string]interface{}{
		"name":         item.Name,
		"description":  item.Description,
		"confidential": item.Confidential,
	}
	if item.TagsJSON != nil {
		updates["tags"] = *item.TagsJSON
	}
	if _, err := q.Skill.WithContext(ctx).Where(q.Skill.ID.Eq(existing.ID)).UpdateColumns(updates); err != nil {
		return nil, errors.Join(err, errors.New("更新技能注册表失败"), errors.NewMsg("系统异常"))
	}
	existing.Name = item.Name
	existing.Description = item.Description
	existing.Confidential = item.Confidential
	existing.Tags = item.TagsJSON
	return existing, nil
}

// UpdateSkillColumns 技能注册行的局部更新（PATCH：机密 / 模型 / 标签）
func UpdateSkillColumns(ctx context.Context, id int64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	q := db.Ctx(ctx)
	if _, err := q.Skill.WithContext(ctx).Where(q.Skill.ID.Eq(id)).UpdateColumns(updates); err != nil {
		return errors.Join(err, errors.New("更新技能失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// NextSkillVersionNumber 技能的下一个版本号（当前最大 +1，首个为 1）
func NextSkillVersionNumber(ctx context.Context, skillID int64) (int64, error) {
	q := db.Ctx(ctx)
	sv := q.SkillVersion
	row, err := sv.WithContext(ctx).
		Where(sv.SkillID.Eq(skillID)).
		Order(sv.VersionNumber.Desc()).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 1, nil
		}
		return 0, errors.Join(err, errors.New("查询技能版本失败"), errors.NewMsg("系统异常"))
	}
	return row.VersionNumber + 1, nil
}

// CreateSkillVersion 记录一次版本产生（上传安装 / 发布）
func CreateSkillVersion(ctx context.Context, v *model.SkillVersion) error {
	q := db.Ctx(ctx)
	if err := q.SkillVersion.WithContext(ctx).Create(v); err != nil {
		return errors.Join(err, errors.New("写入技能版本失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// ListSkillVersions 技能的版本历史（版本号倒序）
func ListSkillVersions(ctx context.Context, skillID int64) ([]*model.SkillVersion, error) {
	q := db.Ctx(ctx)
	sv := q.SkillVersion
	rows, err := sv.WithContext(ctx).
		Where(sv.SkillID.Eq(skillID)).
		Order(sv.VersionNumber.Desc()).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询技能版本失败"), errors.NewMsg("系统异常"))
	}
	return rows, nil
}

// DeleteSkillCascade 删除技能注册行并连带清理会话关联与版本历史（事务）
func DeleteSkillCascade(ctx context.Context, skillID int64) error {
	return db.Transition(ctx, func(txCtx context.Context) error {
		q := db.Ctx(txCtx)
		ss, sv := q.SessionSkill, q.SkillVersion
		if _, err := ss.WithContext(txCtx).Where(ss.SkillID.Eq(skillID)).Delete(); err != nil {
			return errors.Join(err, errors.New("清理技能会话关联失败"), errors.NewMsg("系统异常"))
		}
		if _, err := sv.WithContext(txCtx).Where(sv.SkillID.Eq(skillID)).Delete(); err != nil {
			return errors.Join(err, errors.New("清理技能版本失败"), errors.NewMsg("系统异常"))
		}
		if _, err := q.Skill.WithContext(txCtx).Where(q.Skill.ID.Eq(skillID)).Delete(); err != nil {
			return errors.Join(err, errors.New("删除技能失败"), errors.NewMsg("系统异常"))
		}
		return nil
	})
}
