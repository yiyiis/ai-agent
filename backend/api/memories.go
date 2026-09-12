package api

import (
	"context"
	"time"

	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/jwt"
)

type MemoryStatusResp struct {
	Enabled bool     `json:"enabled"`
	Reasons []string `json:"reasons"`
}

type MemoryStatusReq struct{}

// MemoryStatus 查询跨会话记忆状态 (GET /api/memories/status)
func MemoryStatus(ctx context.Context, _ *MemoryStatusReq) (*MemoryStatusResp, error) {
	return &MemoryStatusResp{Enabled: true, Reasons: []string{}}, nil
}

type MemoryOut struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Topic     string    `json:"topic"`
	Content   string    `json:"content"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ListMemoriesReq struct{}

// ListMemories 查询跨会话记忆列表 (GET /api/memories)
func ListMemories(ctx context.Context, _ *ListMemoriesReq) (*[]MemoryOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	m := db.Ctx(ctx).Memory
	memories, err := m.WithContext(ctx).
		Where(m.UserID.Eq(identity.UserID)).
		Order(m.Pinned.Desc(), m.UpdatedAt.Desc()).
		Find()
	if err != nil {
		return nil, err
	}

	res := make([]MemoryOut, 0, len(memories))
	for _, mo := range memories {
		res = append(res, MemoryOut{
			ID:        mo.MemoryID,
			Scope:     mo.Scope,
			Topic:     mo.Topic,
			Content:   mo.Content,
			Pinned:    mo.Pinned,
			CreatedAt: mo.CreatedAt,
			UpdatedAt: mo.UpdatedAt,
		})
	}
	return &res, nil
}
