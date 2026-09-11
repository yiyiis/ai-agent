package model

import (
	"time"
)

// TokenUsage 每次模型调用 Token 消耗明细审计表
type TokenUsage struct {
	ID               int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID        string    `gorm:"type:varchar(36);index;not null" json:"session_id"`
	CompanyID        int64     `gorm:"index;default:0;not null" json:"company_id"`
	UserID           int64     `gorm:"index;default:0;not null" json:"user_id"`
	Model            string    `gorm:"type:varchar(128);index;not null" json:"model"`
	ProviderKey      string    `gorm:"type:varchar(64);default:'';not null" json:"provider_key"`
	RoundIndex       int       `gorm:"default:0;not null" json:"round_index"`
	Purpose          string    `gorm:"type:varchar(16);index;default:'turn';not null" json:"purpose"` // turn / compact / extract / rerank
	PromptTokens     int       `gorm:"default:0;not null" json:"prompt_tokens"`
	CompletionTokens int       `gorm:"default:0;not null" json:"completion_tokens"`
	CachedTokens     int       `gorm:"default:0;not null" json:"cached_tokens"`
	ReasoningTokens  int       `gorm:"default:0;not null" json:"reasoning_tokens"`
	CreatedAt        time.Time `gorm:"index" json:"created_at"`
}

func (TokenUsage) TableName() string {
	return "token_usages"
}
