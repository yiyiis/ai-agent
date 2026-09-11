package model

import (
	"time"
)

// Memory 跨会话长期记忆表
type Memory struct {
	ID              int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	MemoryID        string     `gorm:"type:varchar(36);uniqueIndex;not null" json:"memory_id"`
	CompanyID       int64      `gorm:"index;uniqueIndex:uk_owner_key;default:0;not null" json:"company_id"`
	UserID          int64      `gorm:"index;uniqueIndex:uk_owner_key;default:0;not null" json:"user_id"`
	Scope           string     `gorm:"type:varchar(32);uniqueIndex:uk_owner_key;default:'preference';not null" json:"scope"`
	Topic           string     `gorm:"type:varchar(64);index;default:'';not null" json:"topic"`
	KeyHint         string     `gorm:"type:varchar(255);uniqueIndex:uk_owner_key;not null" json:"key_hint"`
	Content         string     `gorm:"type:varchar(255);not null" json:"content"`
	Source          string     `gorm:"type:varchar(16);index;default:'explicit';not null" json:"source"` // explicit / extracted / manual
	SourceSessionID *string    `gorm:"type:varchar(36)" json:"source_session_id,omitempty"`
	SourceMessageID *string    `gorm:"type:varchar(36)" json:"source_message_id,omitempty"`
	Pinned          bool       `gorm:"default:false;not null" json:"pinned"`
	HitCount        int        `gorm:"default:0;not null" json:"hit_count"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	DeletedAt       *time.Time `gorm:"index" json:"deleted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (Memory) TableName() string {
	return "memories"
}

// SessionDigest 长会话折叠压缩摘要表
type SessionDigest struct {
	ID              int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID       string    `gorm:"type:varchar(36);index;not null" json:"session_id"`
	UptoMessageID   int64     `gorm:"index;not null" json:"upto_message_id"`
	Content         string    `gorm:"type:longtext;not null" json:"content"`
	CoveredTokens   int       `gorm:"default:0;not null" json:"covered_tokens"`
	CoveredMessages int       `gorm:"default:0;not null" json:"covered_messages"`
	CreatedAt       time.Time `json:"created_at"`
}

func (SessionDigest) TableName() string {
	return "session_digests"
}
