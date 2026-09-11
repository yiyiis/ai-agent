package api

import (
	"net/http"

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
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	m := db.Ctx(c.Request.Context()).Memory
	memories, _ := m.WithContext(c.Request.Context()).
		Where(m.UserID.Eq(identity.UserID)).
		Order(m.Pinned.Desc(), m.UpdatedAt.Desc()).
		Find()

	res := make([]gin.H, 0, len(memories))
	for _, mo := range memories {
		res = append(res, gin.H{
			"id":         mo.MemoryID,
			"scope":      mo.Scope,
			"topic":      mo.Topic,
			"content":    mo.Content,
			"pinned":     mo.Pinned,
			"created_at": mo.CreatedAt,
			"updated_at": mo.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, res)
}
