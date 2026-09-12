package dao

import (
	"context"
	"time"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"
	"gorm.io/gorm"
)

// CreateMessage 插入单条消息
func CreateMessage(ctx context.Context, msg *model.Message) error {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	if err := db.Ctx(ctx).Message.WithContext(ctx).Create(msg); err != nil {
		return errors.Join(err, errors.New("插入消息记录失败"), errors.NewMsg("系统异常"))
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
		return nil, errors.Join(err, errors.New("查询会话消息历史失败"), errors.NewMsg("系统异常"))
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
		return nil, errors.Join(err, errors.New("查询消息失败"), errors.NewMsg("系统异常"))
	}
	return msg, nil
}

// CountMessagesBySessionID 统计指定会话的消息总数
func CountMessagesBySessionID(ctx context.Context, sessionID string) (int64, error) {
	m := db.Ctx(ctx).Message
	count, err := m.WithContext(ctx).
		Where(m.SessionID.Eq(sessionID)).
		Count()
	if err != nil {
		return 0, errors.Join(err, errors.New("统计会话消息数量失败"), errors.NewMsg("系统异常"))
	}
	return count, nil
}

// DeleteMessagesFromRowID 删除会话内自增 id >= fromRowID 的全部消息。
// 编辑历史提问重发时，从被编辑的那条起整段截断。
func DeleteMessagesFromRowID(ctx context.Context, sessionID string, fromRowID int64) error {
	m := db.Ctx(ctx).Message
	if _, err := m.WithContext(ctx).
		Where(m.SessionID.Eq(sessionID), m.ID.Gte(fromRowID)).
		Delete(); err != nil {
		return errors.Join(err, errors.New("截断会话消息失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// RollbackDanglingTurn 回滚会话末尾的"悬空轮"：最后一个 user 消息之后没有任何
// 实质回复（既无带正文的 assistant、也无 tool 结果）。服务中断/用户中止都可能
// 留下这种只有提问没有回答的轮次——读取会话时统一清理，让问题回到输入框重发。
// 幂等：无事可做时返回 nil。
func RollbackDanglingTurn(ctx context.Context, sessionID string) *model.Message {
	msgs, err := ListMessagesBySessionID(ctx, sessionID)
	if err != nil || len(msgs) == 0 {
		return nil
	}

	lastUserIdx, substantive := -1, false
	for i, m := range msgs {
		switch {
		case m.Role == "user":
			lastUserIdx, substantive = i, false
		case m.Role == "tool":
			substantive = true
		case m.Role == "assistant" && m.Content != "":
			substantive = true
		}
	}
	if lastUserIdx < 0 || substantive {
		return nil
	}

	victim := msgs[lastUserIdx]
	if err := DeleteMessagesFromRowID(ctx, sessionID, victim.ID); err != nil {
		return nil
	}
	return &victim
}
