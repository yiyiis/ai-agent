package dao

import (
	"context"
	"time"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"

	"gorm.io/gen"
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
	if err := db.Ctx(ctx).Session.WithContext(ctx).Create(session); err != nil {
		return errors.Join(err, errors.New("创建会话记录失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// GetSessionBySessionID 根据 session_id 查询会话
func GetSessionBySessionID(ctx context.Context, sessionID string) (*model.Session, error) {
	s := db.Ctx(ctx).Session
	session, err := s.WithContext(ctx).
		Where(s.SessionID.Eq(sessionID)).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询会话失败"), errors.NewMsg("系统异常"))
	}
	return session, nil
}

// GetLatestEmptySession 获取用户最近一条未发送任何消息的空会话（若有）
func GetLatestEmptySession(ctx context.Context, userID int64) (*model.Session, error) {
	q := db.Ctx(ctx)
	s, m := q.Session, q.Message
	session, err := s.WithContext(ctx).
		Where(s.UserID.Eq(userID)).
		Where(gen.Columns{s.SessionID}.NotIn(m.WithContext(ctx).Select(m.SessionID))).
		Order(s.ID.Desc()).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询空会话失败"), errors.NewMsg("系统异常"))
	}
	return session, nil
}

// ListSessions 查询会话列表（仅展示有真实对话消息的会话，过滤无消息空会话）
func ListSessions(ctx context.Context, userID, companyID int64, limit, offset int) ([]model.Session, int64, error) {
	q := db.Ctx(ctx)
	s, m := q.Session, q.Message
	sq := s.WithContext(ctx)

	// 只展示有实际对话消息的会话，彻底杜绝未对话的空会话污染列表
	conds := func() []gen.Condition {
		cs := []gen.Condition{gen.Columns{s.SessionID}.In(m.WithContext(ctx).Select(m.SessionID))}
		if userID > 0 {
			cs = append(cs, s.UserID.Eq(userID))
		}
		if companyID > 0 {
			cs = append(cs, s.CompanyID.Eq(companyID))
		}
		return cs
	}

	total, err := sq.Where(conds()...).Count()
	if err != nil {
		return nil, 0, errors.Join(err, errors.New("统计会话数量失败"), errors.NewMsg("系统异常"))
	}

	if limit <= 0 {
		limit = 20
	}
	list, err := sq.Where(conds()...).
		Order(s.ID.Desc()).
		Limit(limit).
		Offset(offset).
		Find()
	if err != nil {
		return nil, 0, errors.Join(err, errors.New("获取会话列表失败"), errors.NewMsg("系统异常"))
	}

	sessions := make([]model.Session, 0, len(list))
	for _, item := range list {
		sessions = append(sessions, *item)
	}
	return sessions, total, nil
}

// UpdateSessionTitle 修改会话标题
func UpdateSessionTitle(ctx context.Context, sessionID, title string) error {
	return UpdateSession(ctx, sessionID, map[string]interface{}{
		"title": title,
	})
}

// UpdateSession 更新会话指定字段
func UpdateSession(ctx context.Context, sessionID string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	s := db.Ctx(ctx).Session
	_, err := s.WithContext(ctx).
		Where(s.SessionID.Eq(sessionID)).
		Updates(updates)
	if err != nil {
		return errors.Join(err, errors.New("更新会话失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// DeleteSession 删除会话及级联清理消息
func DeleteSession(ctx context.Context, sessionID string) error {
	return db.Transition(ctx, func(txCtx context.Context) error {
		m := db.Ctx(txCtx).Message
		if _, err := m.WithContext(txCtx).
			Where(m.SessionID.Eq(sessionID)).
			Delete(); err != nil {
			return errors.Join(err, errors.New("删除会话消息失败"), errors.NewMsg("系统异常"))
		}

		s := db.Ctx(txCtx).Session
		if _, err := s.WithContext(txCtx).
			Where(s.SessionID.Eq(sessionID)).
			Delete(); err != nil {
			return errors.Join(err, errors.New("删除会话记录失败"), errors.NewMsg("系统异常"))
		}
		return nil
	})
}
