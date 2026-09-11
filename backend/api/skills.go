package api

import (
	"net/http"

	"backend/pkg/db"
	"github.com/gin-gonic/gin"
)

// ListSkills 获取当前可用技能列表 (GET /api/skills)
func ListSkills(c *gin.Context) {
	ctx := c.Request.Context()
	skills, _ := db.Ctx(ctx).Skill.WithContext(ctx).Find()

	res := make([]gin.H, 0, len(skills))
	for _, s := range skills {
		res = append(res, gin.H{
			"id":           s.ID,
			"name":         s.Name,
			"description":  s.Description,
			"dir_name":     s.DirName,
			"created_at":   s.CreatedAt,
			"owner_id":     s.OwnerID,
			"confidential": s.Confidential,
			"model":        s.Model,
		})
	}
	c.JSON(http.StatusOK, res)
}
