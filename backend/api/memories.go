package api

import (
	"net/http"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/jwt"
	"github.com/gin-gonic/gin"
)

// MemoryStatus 查询跨会话记忆状态 (GET /api/memories/status)
func MemoryStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled": true,
		"reasons": []string{},
	})
}

// ListMemories 查询跨会话记忆列表 (GET /api/memories)
func ListMemories(c *gin.Context) {
	identity, err := jwt.GetIdentityFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var memories []model.Memory
	_ = db.GetRawDB().WithContext(c.Request.Context()).
		Where("user_id = ?", identity.UserID).
		Order("pinned DESC, updated_at DESC").
		Find(&memories).Error

	res := make([]gin.H, 0, len(memories))
	for _, m := range memories {
		res = append(res, gin.H{
			"id":         m.MemoryID,
			"scope":      m.Scope,
			"topic":      m.Topic,
			"content":    m.Content,
			"pinned":     m.Pinned,
			"created_at": m.CreatedAt,
			"updated_at": m.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, res)
}
