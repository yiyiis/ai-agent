package model

import (
	"time"
)

// ApiKey 开放 API 访问密钥表
type ApiKey struct {
	ID            int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Key           string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"key"`
	Name          string    `gorm:"type:varchar(64);default:'';not null" json:"name"`
	CompanyID     int64     `gorm:"index;default:0;not null" json:"company_id"`
	IsGlobal      bool      `gorm:"default:false;not null" json:"is_global"`
	AllowedSkills *string   `gorm:"type:json" json:"allowed_skills,omitempty"`
	MaxConcurrent int       `gorm:"default:3;not null" json:"max_concurrent"`
	IsActive      bool      `gorm:"default:true;not null" json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
}

func (ApiKey) TableName() string {
	return "api_keys"
}

// SkillRun 任务执行与异步调度状态机表
type SkillRun struct {
	ID          int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	RunID       string     `gorm:"type:varchar(36);uniqueIndex;not null" json:"run_id"`
	SkillName   string     `gorm:"type:varchar(128);index;not null" json:"skill_name"`
	SkillID     *int64     `gorm:"index" json:"skill_id,omitempty"`
	CompanyID   int64      `gorm:"index;default:0;not null" json:"company_id"`
	UserID      int64      `gorm:"index;default:0;not null" json:"user_id"`
	APIKeyID    *int64     `gorm:"index" json:"api_key_id,omitempty"`
	SessionID   *string    `gorm:"type:varchar(36);index" json:"session_id,omitempty"`
	Status      string     `gorm:"type:varchar(20);index;not null" json:"status"` // queued / running / succeeded / failed / canceled / needs_input / retry_pending
	Params      *string    `gorm:"type:json" json:"params,omitempty"`
	Result      *string    `gorm:"type:json" json:"result,omitempty"`
	Artifacts   *string    `gorm:"type:json" json:"artifacts,omitempty"`
	Progress    *string    `gorm:"type:json" json:"progress,omitempty"`
	Error       *string    `gorm:"type:text" json:"error,omitempty"`
	StdoutTail  *string    `gorm:"type:text" json:"stdout_tail,omitempty"`
	StderrTail  *string    `gorm:"type:text" json:"stderr_tail,omitempty"`
	DurationMs  *int       `json:"duration_ms,omitempty"`
	Attempt     int        `gorm:"default:1;not null" json:"attempt"`
	MaxAttempts int        `gorm:"default:1;not null" json:"max_attempts"`
	FailureKind *string    `gorm:"type:varchar(32)" json:"failure_kind,omitempty"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	ExecContext *string    `gorm:"type:json" json:"exec_context,omitempty"`
	Attempts    *string    `gorm:"type:json" json:"attempts,omitempty"`
	CreatedAt   time.Time  `gorm:"index" json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

func (SkillRun) TableName() string {
	return "skill_runs"
}
