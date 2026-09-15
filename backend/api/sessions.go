package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"backend/config"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"backend/pkg/sandbox"
	"github.com/google/uuid"
)

type SessionOut struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Model        string  `json:"model"`
	SystemPrompt *string `json:"system_prompt"`
	AutoSkill    bool    `json:"auto_skill"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

type MessageOut struct {
	ID          string          `json:"id"`
	Role        string          `json:"role"`
	Content     string          `json:"content"`
	Reasoning   *string         `json:"reasoning,omitempty"`
	ToolCalls   json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID  *string         `json:"tool_call_id,omitempty"`
	Name        *string         `json:"name,omitempty"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

type SessionDetailOut struct {
	SessionOut
	Messages        []MessageOut `json:"messages"`
	EnabledSkillIDs []int64      `json:"enabled_skill_ids"`
	// 读取时回滚掉的悬空提问（服务中断/中止留下的无回答轮次），
	// 前端应把它还原到输入框让用户重发
	PendingQuestion *PendingQuestionOut `json:"pending_question,omitempty"`
}

type PendingQuestionOut struct {
	Content     string          `json:"content"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
}

type StatusOKResp struct {
	Status string `json:"status"`
}

// toSessionOut 实体 → 出参（空 system_prompt 输出 null）
func toSessionOut(s *model.Session) SessionOut {
	var promptPtr *string
	if s.SystemPrompt != "" {
		promptPtr = &s.SystemPrompt
	}
	return SessionOut{
		ID:           s.SessionID,
		Title:        s.Title,
		Model:        s.Model,
		SystemPrompt: promptPtr,
		AutoSkill:    s.AutoSkill,
		CreatedAt:    s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    s.UpdatedAt.Format(time.RFC3339),
	}
}

// loadOwnedSession 加载会话并严格实施用户隔离：
// 不存在与他人会话统一报"会话不存在"，防存在性探测
func loadOwnedSession(ctx context.Context, identity jwt.TokenClaims, sessionID string) (*model.Session, error) {
	session, err := dao.GetSessionBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.UserID != identity.UserID {
		return nil, errors.NewMsg("会话不存在")
	}
	return session, nil
}

type CreateSessionReq struct {
	Title        string  `json:"title"`
	Model        string  `json:"model"`
	SystemPrompt *string `json:"system_prompt"`
}

// CreateSession 创建会话 (POST /api/sessions)
func CreateSession(ctx context.Context, req *CreateSessionReq) (*SessionOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新会话"
	}

	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = config.GetConfig().LLM.DefaultModel
	}
	if modelName == "" {
		modelName = "MiniMax-Text-01"
	}

	// 防重复创建机制：若用户存在尚无任何消息的空会话，直接复用返回现有空会话
	latestEmpty, err := dao.GetLatestEmptySession(ctx, identity.UserID)
	if err == nil && latestEmpty != nil {
		if modelName != "" && latestEmpty.Model != modelName {
			_ = dao.UpdateSession(ctx, latestEmpty.SessionID, map[string]interface{}{"model": modelName})
			latestEmpty.Model = modelName
		}
		out := toSessionOut(latestEmpty)
		return &out, nil
	}
	sysPrompt := ""
	if req.SystemPrompt != nil {
		sysPrompt = *req.SystemPrompt
	}

	now := time.Now()
	session := &model.Session{
		SessionID:    uuid.New().String(),
		UserID:       identity.UserID,
		CompanyID:    identity.CompanyID,
		Title:        title,
		Model:        modelName,
		SystemPrompt: sysPrompt,
		AutoSkill:    true,
		Interactive:  false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := dao.CreateSession(ctx, session); err != nil {
		return nil, err
	}

	out := toSessionOut(session)
	return &out, nil
}

type ListSessionsReq struct{}

// ListSessions 获取当前用户所有会话列表 (GET /api/sessions)
func ListSessions(ctx context.Context, _ *ListSessionsReq) (*[]SessionOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	sessions, _, err := dao.ListSessions(ctx, identity.UserID, identity.CompanyID, 100, 0)
	if err != nil {
		return nil, err
	}

	res := make([]SessionOut, 0, len(sessions))
	for i := range sessions {
		res = append(res, toSessionOut(&sessions[i]))
	}
	return &res, nil
}

type GetSessionDetailReq struct {
	ID string `uri:"id"`
}

// GetSessionDetail 获取会话详情及历史消息 (GET /api/sessions/:id)。
// 顺带回滚末尾悬空轮（只有提问没有回答的轮次），被回滚的提问经 pending_question 返回。
func GetSessionDetail(ctx context.Context, req *GetSessionDetailReq) (*SessionDetailOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	session, err := loadOwnedSession(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}

	var pending *PendingQuestionOut
	// 后台 Turn 正在跑时，"末尾只有提问没有回答"是本轮尚未产出而非悬空轮，
	// 不能回滚（否则会把在跑轮次刚落库的提问删掉）
	if !turnRegistry.Running(req.ID) {
		if victim := dao.RollbackDanglingTurn(ctx, req.ID); victim != nil {
			pending = &PendingQuestionOut{Content: victim.Content}
			if victim.Attachments != nil && *victim.Attachments != "" {
				pending.Attachments = json.RawMessage(*victim.Attachments)
			}
		}
	}

	messages, err := dao.ListMessagesBySessionID(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	msgOuts := make([]MessageOut, 0, len(messages))
	for _, m := range messages {
		msgItem := MessageOut{
			ID:         m.MessageID,
			Role:       m.Role,
			Content:    m.Content,
			Reasoning:  m.Reasoning,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
			CreatedAt:  m.CreatedAt.Format(time.RFC3339),
		}
		if m.ToolCalls != nil && *m.ToolCalls != "" {
			msgItem.ToolCalls = json.RawMessage(*m.ToolCalls)
		}
		if m.Attachments != nil && *m.Attachments != "" {
			msgItem.Attachments = json.RawMessage(*m.Attachments)
		}
		msgOuts = append(msgOuts, msgItem)
	}

	return &SessionDetailOut{
		SessionOut:      toSessionOut(session),
		Messages:        msgOuts,
		EnabledSkillIDs: []int64{},
		PendingQuestion: pending,
	}, nil
}

type UpdateSessionReq struct {
	ID           string  `uri:"id"`
	Title        *string `json:"title"`
	SystemPrompt *string `json:"system_prompt"`
	Model        *string `json:"model"`
	AutoSkill    *bool   `json:"auto_skill"`
}

// UpdateSession 更新会话 (PATCH /api/sessions/:id)
func UpdateSession(ctx context.Context, req *UpdateSessionReq) (*SessionOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	session, err := loadOwnedSession(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}

	updates := make(map[string]interface{})
	if req.Title != nil {
		updates["title"] = *req.Title
		session.Title = *req.Title
	}
	if req.SystemPrompt != nil {
		updates["system_prompt"] = *req.SystemPrompt
		session.SystemPrompt = *req.SystemPrompt
	}
	if req.Model != nil {
		updates["model"] = *req.Model
		session.Model = *req.Model
	}
	if req.AutoSkill != nil {
		updates["auto_skill"] = *req.AutoSkill
		session.AutoSkill = *req.AutoSkill
	}

	if len(updates) > 0 {
		if err := dao.UpdateSession(ctx, req.ID, updates); err != nil {
			return nil, err
		}
	}

	out := toSessionOut(session)
	out.UpdatedAt = time.Now().Format(time.RFC3339)
	return &out, nil
}

type DeleteSessionReq struct {
	ID string `uri:"id"`
}

// DeleteSession 删除会话 (DELETE /api/sessions/:id)
func DeleteSession(ctx context.Context, req *DeleteSessionReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	if _, err := loadOwnedSession(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	// 先中止该会话在跑的后台 Turn，避免级联删除与后台写入竞争
	turnRegistry.Abort(req.ID)

	if err := dao.DeleteSession(ctx, req.ID); err != nil {
		return nil, err
	}

	// 沙箱资源随会话销毁（docker 驱动强制移除容器；尽力而为，失败不影响删除结果）
	sandbox.Close(req.ID)
	return &StatusOKResp{Status: "ok"}, nil
}

type SetEnabledSkillsReq struct {
	ID string `uri:"id"`
	Skills []struct {
		SkillID             int64  `json:"skill_id"`
		PinnedVersionNumber *int64 `json:"pinned_version_number"`
	} `json:"skills"`
}

// SetEnabledSkills 设置会话启用的技能 (PUT /api/sessions/:id/skills)
func SetEnabledSkills(ctx context.Context, req *SetEnabledSkillsReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	if _, err := loadOwnedSession(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	// 预留技能关联同步逻辑
	return &StatusOKResp{Status: "ok"}, nil
}
