package api

import (
	"net/http"

	"backend/dal/model"
	"backend/pkg/db"
	"github.com/gin-gonic/gin"
)

// ListSkills 获取当前可用技能列表 (GET /api/skills)
func ListSkills(c *gin.Context) {
	var skills []model.Skill
	_ = db.GetRawDB().WithContext(c.Request.Context()).Find(&skills).Error

	res := make([]gin.H, 0, len(skills))
	for _, s := range skills {
		res = append(res, gin.H{
			"id":          s.ID,
			"name":        s.Name,
			"description": s.Description,
			"dir_name":    s.DirName,
			"created_at":  s.CreatedAt,
			"owner_id":    s.OwnerID,
			"confidential": s.Confidential,
			"model":       s.Model,
		})
	}
	c.JSON(http.StatusOK, res)
}
