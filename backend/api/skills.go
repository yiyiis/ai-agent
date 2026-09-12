package api

import (
	"context"
	"time"

	"backend/pkg/db"
)

type SkillOut struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	DirName      string    `json:"dir_name"`
	CreatedAt    time.Time `json:"created_at"`
	OwnerID      int64     `json:"owner_id"`
	Confidential bool      `json:"confidential"`
	Model        *string   `json:"model"`
}

type ListSkillsReq struct{}

// ListSkills 获取当前可用技能列表 (GET /api/skills)
func ListSkills(ctx context.Context, _ *ListSkillsReq) (*[]SkillOut, error) {
	skills, err := db.Ctx(ctx).Skill.WithContext(ctx).Find()
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
			CreatedAt:    s.CreatedAt,
			OwnerID:      s.OwnerID,
			Confidential: s.Confidential,
			Model:        s.Model,
		})
	}
	return &res, nil
}
