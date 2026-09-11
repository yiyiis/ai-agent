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
	if err := db.Ctx(ctx).Message.WithContext(ctx).Create(msg); err != nil {
		return errors.Wrap(err, "插入消息记录失败")
	}
	return nil
}

// ListMessagesBySessionID 按自增 id 正序获取会话的全部历史消息
func ListMessagesBySessionID(ctx context.Context, sessionID string) ([]model.Message, error) {
	m := db.Ctx(ctx).Message
	list, err := m.WithContext(ctx).
		Where(m.SessionID.Eq(sessionID)).
		Order(m.ID).
		Find()
	if err != nil {
		return nil, errors.Wrap(err, "查询会话消息历史失败")
	}

	msgs := make([]model.Message, 0, len(list))
	for _, item := range list {
		msgs = append(msgs, *item)
	}
	return msgs, nil
}

// GetMessageByMessageID 根据 message_id 查询指定消息
func GetMessageByMessageID(ctx context.Context, messageID string) (*model.Message, error) {
	m := db.Ctx(ctx).Message
	msg, err := m.WithContext(ctx).
		Where(m.MessageID.Eq(messageID)).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "查询消息失败")
	}
	return msg, nil
}

// CountMessagesBySessionID 统计指定会话的消息总数
func CountMessagesBySessionID(ctx context.Context, sessionID string) (int64, error) {
	m := db.Ctx(ctx).Message
	return m.WithContext(ctx).
		Where(m.SessionID.Eq(sessionID)).
		Count()
}
