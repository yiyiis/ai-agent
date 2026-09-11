package dao

import (
	"context"
	"time"

	"backend/dal/model"
	"backend/pkg/db"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// CreateMessage 插入单条消息
func CreateMessage(ctx context.Context, msg *model.Message) error {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	err := db.GetRawDB().WithContext(ctx).Create(msg).Error
	if err != nil {
		return errors.Wrap(err, "插入消息记录失败")
	}
	return nil
}

// ListMessagesBySessionID 按自增 id 正序获取会话的全部历史消息
func ListMessagesBySessionID(ctx context.Context, sessionID string) ([]model.Message, error) {
	var msgs []model.Message
	err := db.GetRawDB().WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("id ASC").
		Find(&msgs).Error
	if err != nil {
		return nil, errors.Wrap(err, "查询会话消息历史失败")
	}
	return msgs, nil
}

// GetMessageByMessageID 根据 message_id 查询指定消息
func GetMessageByMessageID(ctx context.Context, messageID string) (*model.Message, error) {
	var msg model.Message
	err := db.GetRawDB().WithContext(ctx).
		Where("message_id = ?", messageID).
		First(&msg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "查询消息失败")
	}
	return &msg, nil
}
