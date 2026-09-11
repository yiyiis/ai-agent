package api

import (
	"context"
	"time"

	"backend/config"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/errors"
	"github.com/google/uuid"
)

type CreateSessionReq struct {
	Title        string `json:"title"`
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
}

type CreateSessionResp struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
}

func CreateSession(ctx context.Context, req *CreateSessionReq) (*CreateSessionResp, error) {
	title := req.Title
	if title == "" {
		title = "新会话"
	}
	modelName := req.Model
	if modelName == "" {
		modelName = config.GetConfig().LLM.DefaultModel
	}

	sessionID := uuid.New().String()
	session := &model.Session{
		SessionID:    sessionID,
		Title:        title,
		Model:        modelName,
		SystemPrompt: req.SystemPrompt,
		AutoSkill:    true,
		Interactive:  false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := dao.CreateSession(ctx, session); err != nil {
		return nil, errors.NewMsg("创建会话失败")
	}

	return &CreateSessionResp{
		SessionID: sessionID,
		Title:     title,
		Model:     modelName,
	}, nil
}

type ListSessionsReq struct {
	Page     int `form:"page"`
	PageSize int `form:"page_size"`
}

type SessionItem struct {
	SessionID    string    `json:"session_id"`
	Title        string    `json:"title"`
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ListSessionsResp struct {
	List  []SessionItem `json:"list"`
	Total int64         `json:"total"`
}

func ListSessions(ctx context.Context, req *ListSessionsReq) (*ListSessionsResp, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	sessions, total, err := dao.ListSessions(ctx, 0, 0, pageSize, offset)
	if err != nil {
		return nil, errors.NewMsg("查询会话列表失败")
	}

	list := make([]SessionItem, 0, len(sessions))
	for _, s := range sessions {
		list = append(list, SessionItem{
			SessionID:    s.SessionID,
			Title:        s.Title,
			Model:        s.Model,
			SystemPrompt: s.SystemPrompt,
			CreatedAt:    s.CreatedAt,
			UpdatedAt:    s.UpdatedAt,
		})
	}

	return &ListSessionsResp{
		List:  list,
		Total: total,
	}, nil
}

type SessionDetailReq struct {
	SessionID string `uri:"id" validate:"required"`
}

type SessionDetailResp struct {
	SessionID    string          `json:"session_id"`
	Title        string          `json:"title"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt,omitempty"`
	Messages     []model.Message `json:"messages"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func GetSessionDetail(ctx context.Context, req *SessionDetailReq) (*SessionDetailResp, error) {
	session, err := dao.GetSessionBySessionID(ctx, req.SessionID)
	if err != nil || session == nil {
		return nil, errors.NewMsg("会话不存在")
	}

	messages, err := dao.ListMessagesBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, errors.NewMsg("获取会话历史消息失败")
	}

	return &SessionDetailResp{
		SessionID:    session.SessionID,
		Title:        session.Title,
		Model:        session.Model,
		SystemPrompt: session.SystemPrompt,
		Messages:     messages,
		CreatedAt:    session.CreatedAt,
		UpdatedAt:    session.UpdatedAt,
	}, nil
}

type RenameSessionReq struct {
	SessionID string `uri:"id" validate:"required"`
	Title     string `json:"title" validate:"required"`
}

type RenameSessionResp struct {
	Success bool `json:"success"`
}

func RenameSession(ctx context.Context, req *RenameSessionReq) (*RenameSessionResp, error) {
	if err := dao.UpdateSessionTitle(ctx, req.SessionID, req.Title); err != nil {
		return nil, errors.NewMsg("修改会话标题失败")
	}
	return &RenameSessionResp{Success: true}, nil
}

type DeleteSessionReq struct {
	SessionID string `uri:"id" validate:"required"`
}

type DeleteSessionResp struct {
	Success bool `json:"success"`
}

func DeleteSession(ctx context.Context, req *DeleteSessionReq) (*DeleteSessionResp, error) {
	if err := dao.DeleteSession(ctx, req.SessionID); err != nil {
		return nil, errors.NewMsg("删除会话失败")
	}
	return &DeleteSessionResp{Success: true}, nil
}
