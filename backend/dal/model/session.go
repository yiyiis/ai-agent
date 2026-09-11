package model

import (
	"time"
)

// Session 代表一个会话
type Session struct {
	ID                           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID                    string    `gorm:"type:varchar(36);uniqueIndex;not null" json:"session_id"`
	CompanyID                    int64     `gorm:"index;default:0" json:"company_id"`
	UserID                       int64     `gorm:"index;default:0" json:"user_id"`
	Title                        string    `gorm:"type:varchar(255);not null;default:'新会话'" json:"title"`
	Model                        string    `gorm:"type:varchar(128);not null" json:"model"`
	SystemPrompt                 string    `gorm:"type:longtext" json:"system_prompt,omitempty"`
	AutoSkill                    bool      `gorm:"default:true;not null" json:"auto_skill"`
	Interactive                  bool      `gorm:"default:false;not null" json:"interactive"`
	APIKeyID                     *int64    `gorm:"index" json:"api_key_id,omitempty"`
	LastContextTokens            int       `gorm:"default:0;not null" json:"last_context_tokens"`
	MemoryExtractedUptoMessageID int64     `gorm:"default:0;not null" json:"memory_extracted_upto_message_id"`
	CreatedAt                    time.Time `json:"created_at"`
	UpdatedAt                    time.Time `json:"updated_at"`

	Messages []Message `gorm:"foreignKey:SessionID;references:SessionID" json:"messages,omitempty"`
}

func (Session) TableName() string {
	return "sessions"
}
