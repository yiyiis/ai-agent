package dao

import (
	"context"
	"time"

	"backend/dal/model"
	"backend/pkg/db"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// CreateSession 创建新会话
func CreateSession(ctx context.Context, session *model.Session) error {
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = time.Now()
	}
	err := db.GetRawDB().WithContext(ctx).Create(session).Error
	if err != nil {
		return errors.Wrap(err, "创建会话记录失败")
	}
	return nil
}

// GetSessionBySessionID 根据 session_id 查询会话
func GetSessionBySessionID(ctx context.Context, sessionID string) (*model.Session, error) {
	var s model.Session
	err := db.GetRawDB().WithContext(ctx).
		Where("session_id = ?", sessionID).
		First(&s).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "查询会话失败")
	}
	return &s, nil
}

// ListSessions 查询会话列表
func ListSessions(ctx context.Context, userID, companyID int64, limit, offset int) ([]model.Session, int64, error) {
	var sessions []model.Session
	var total int64

	tx := db.GetRawDB().WithContext(ctx).Model(&model.Session{})
	if userID > 0 {
		tx = tx.Where("user_id = ?", userID)
	}
	if companyID > 0 {
		tx = tx.Where("company_id = ?", companyID)
	}

	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, errors.Wrap(err, "统计会话数量失败")
	}

	if limit <= 0 {
		limit = 20
	}
	err := tx.Order("id DESC").
		Limit(limit).
		Offset(offset).
		Find(&sessions).Error
	if err != nil {
		return nil, 0, errors.Wrap(err, "获取会话列表失败")
	}

	return sessions, total, nil
}

// UpdateSessionTitle 修改会话标题
func UpdateSessionTitle(ctx context.Context, sessionID, title string) error {
	err := db.GetRawDB().WithContext(ctx).Model(&model.Session{}).
		Where("session_id = ?", sessionID).
		Updates(map[string]interface{}{
			"title":      title,
			"updated_at": time.Now(),
		}).Error
	if err != nil {
		return errors.Wrap(err, "更新会话标题失败")
	}
	return nil
}

// DeleteSession 删除会话及级联清理消息
func DeleteSession(ctx context.Context, sessionID string) error {
	return db.GetRawDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 删除关联消息
		if err := tx.Where("session_id = ?", sessionID).Delete(&model.Message{}).Error; err != nil {
			return err
		}
		// 删除会话记录
		if err := tx.Where("session_id = ?", sessionID).Delete(&model.Session{}).Error; err != nil {
			return err
		}
		return nil
	})
}
