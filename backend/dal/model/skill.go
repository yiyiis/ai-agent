package model

import (
	"time"
)

// Skill 技能插件元数据表
type Skill struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name         string    `gorm:"type:varchar(128);uniqueIndex;not null" json:"name"`
	Description  string    `gorm:"type:longtext" json:"description,omitempty"`
	DirName      string    `gorm:"type:varchar(128);uniqueIndex;not null" json:"dir_name"`
	OwnerID      int64     `gorm:"default:0;not null" json:"owner_id"`
	OwnerName    string    `gorm:"type:varchar(64);default:'';not null" json:"owner_name"`
	Confidential bool      `gorm:"default:false;not null" json:"confidential"`
	Model        *string   `gorm:"type:varchar(128)" json:"model,omitempty"`
	Tags         *string   `gorm:"type:json" json:"tags,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (Skill) TableName() string {
	return "skills"
}

// SkillVersion 技能版本快照表
type SkillVersion struct {
	ID            int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	SkillID       int64     `gorm:"index;not null" json:"skill_id"`
	VersionNumber int       `gorm:"not null" json:"version_number"`
	Notes         string    `gorm:"type:text" json:"notes,omitempty"`
	CreatedBy     int64     `gorm:"default:0;not null" json:"created_by"`
	CreatedByName string    `gorm:"type:varchar(64);default:'';not null" json:"created_by_name"`
	Source        string    `gorm:"type:varchar(16);default:'publish';not null" json:"source"`
	CreatedAt     time.Time `json:"created_at"`
}

func (SkillVersion) TableName() string {
	return "skill_versions"
}

// SessionSkill 会话与技能关联表
type SessionSkill struct {
	ID                  int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID           string  `gorm:"type:varchar(36);index;uniqueIndex:uk_session_skill;not null" json:"session_id"`
	SkillID             int64   `gorm:"index;uniqueIndex:uk_session_skill;not null" json:"skill_id"`
	PinnedVersionNumber *int    `json:"pinned_version_number,omitempty"`
	Source              string  `gorm:"type:varchar(16);default:'manual';not null" json:"source"` // manual / auto
}

func (SessionSkill) TableName() string {
	return "session_skills"
}
