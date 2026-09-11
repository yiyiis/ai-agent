package model

import (
	"time"
)

// Message 一条会话消息。自增主键保证同一秒内多轮 tool-use 消息顺序完全单调递增
type Message struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	MessageID   string    `gorm:"type:varchar(36);uniqueIndex;not null" json:"message_id"`
	SessionID   string    `gorm:"type:varchar(36);index;not null" json:"session_id"`
	Role        string    `gorm:"type:varchar(32);not null" json:"role"` // user / assistant / system / tool
	Content     string    `gorm:"type:longtext;not null" json:"content"`
	ToolCalls   *string   `gorm:"type:json" json:"tool_calls,omitempty"`
	ToolCallID  *string   `gorm:"type:varchar(64)" json:"tool_call_id,omitempty"`
	Name        *string   `gorm:"type:varchar(128)" json:"name,omitempty"`
	Attachments *string   `gorm:"type:json" json:"attachments,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (Message) TableName() string {
	return "messages"
}
