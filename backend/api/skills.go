package api

import (
	"context"
	"log/slog"
	"time"

	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/skill"
)

type SkillOut struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	DirName      string    `json:"dir_name"`
	Tags         *string   `json:"tags"`
	CreatedAt    time.Time `json:"created_at"`
	OwnerID      int64     `json:"owner_id"`
	Confidential bool      `json:"confidential"`
	Model        *string   `json:"model"`
}

type ListSkillsReq struct{}

// syncSkillRegistry 文件系统技能库 → skills 注册表同步（新增 / 更新 / 清理）。
// 技能目录是唯一源头，注册表是会话关联与目录检索的投影。
func syncSkillRegistry(ctx context.Context) error {
	items := make([]dao.SkillRegistryItem, 0)
	// ListDir 已剔除无 SKILL.md / 缺 name 的坏目录（告警不中断）
	for _, e := range skill.ListDir() {
		items = append(items, dao.SkillRegistryItem{
			Name:        e.Name,
			Description: e.Description,
			DirName:     e.DirName,
			TagsJSON:    dao.MarshalTags(e.Tags),
		})
	}
	return dao.SyncSkills(ctx, items)
}

// ListSkills 获取当前可用技能列表 (GET /api/skills)。
// 每次调用先做一次文件系统 → 注册表同步，保证目录里增删技能即时可见。
func ListSkills(ctx context.Context, _ *ListSkillsReq) (*[]SkillOut, error) {
	if err := syncSkillRegistry(ctx); err != nil {
		return nil, err
	}
	skills, err := dao.ListSkills(ctx)
	if err != nil {
		return nil, err
	}

	res := make([]SkillOut, 0, len(skills))
	for _, s := range skills {
		res = append(res, SkillOut{
			ID:           s.ID,
			Name:         s.Name,
			Description:  s.Description,
			DirName:      s.DirName,
			Tags:         s.Tags,
			CreatedAt:    s.CreatedAt,
			OwnerID:      s.OwnerID,
			Confidential: s.Confidential,
			Model:        s.Model,
		})
	}
	return &res, nil
}

// InitSkillRegistry 启动时同步一次技能注册表（main 调用），
// 让预置技能目录无需等首次 /api/skills 调用即可被会话使用
func InitSkillRegistry() {
	ctx := db.WithContext(context.Background())
	if err := syncSkillRegistry(ctx); err != nil {
		// 注册表同步失败不阻断启动：会话侧技能装配自行降级
		slog.Warn("启动同步技能注册表失败", "err", errors.Stack(err))
	}
}
