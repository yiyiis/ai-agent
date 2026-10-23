package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"backend/config"
	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/errors"
	"backend/pkg/human"
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
	UserID       int64   `json:"user_id,omitempty"`
	CreatorName  string  `json:"creator_name,omitempty"`
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

type EnabledSkillOut struct {
	SkillID             int64  `json:"skill_id"`
	PinnedVersionNumber *int64 `json:"pinned_version_number"`
	Source              string `json:"source"`
}

// EnabledMcpOut 会话启用的 MCP Server 概况（工具数供前端抽屉展示）
type EnabledMcpOut struct {
	McpServerID int64  `json:"mcp_server_id"`
	Name        string `json:"name"`
	ToolCount   int    `json:"tool_count"`
}

type SessionDetailOut struct {
	SessionOut
	Messages        []MessageOut      `json:"messages"`
	EnabledSkillIDs []int64           `json:"enabled_skill_ids"`
	EnabledSkills   []EnabledSkillOut `json:"enabled_skills"`
	// 阶段八：会话启用的 MCP Server（回显给 McpPanel 抽屉）
	EnabledMcpIDs []int64         `json:"enabled_mcp_ids"`
	EnabledMcps   []EnabledMcpOut `json:"enabled_mcps"`
	// 读取时回滚掉的悬空提问（服务中断/中止留下的无回答轮次），
	// 前端应把它还原到输入框让用户重发
	PendingQuestion *PendingQuestionOut `json:"pending_question,omitempty"`
	PendingInputs   []human.Request     `json:"pending_inputs"`
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
		UserID:       s.UserID,
		CreatedAt:    s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    s.UpdatedAt.Format(time.RFC3339),
	}
}

// loadOwnedSession 加载会话并按三级 RBAC 权限严格实施租户与用户隔离：
// 不存在与他人会话统一报"会话不存在"，防存在性探测
func loadOwnedSession(ctx context.Context, identity jwt.TokenClaims, sessionID string) (*model.Session, error) {
	session, err := dao.GetSessionBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.NewMsg("会话不存在")
	}
	if err := dao.CheckSessionAccess(identity, session); err != nil {
		return nil, err
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

type ListSessionsReq struct {
	Scope string `form:"scope"`
}

// ListSessions 获取会话列表 (GET /api/sessions)
// 支持根据角色与 scope 动态过滤：普通员工仅查本人会话；
// 企管 scope=all 查本企业全员会话与 API 任务会话；超管 scope=all 查全平台会话。
func ListSessions(ctx context.Context, req *ListSessionsReq) (*[]SessionOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	queryUserID := identity.UserID
	queryCompanyID := identity.CompanyID

	if req != nil && req.Scope == "all" {
		if identity.UserType == constants.SuperManager {
			queryUserID = 0
			queryCompanyID = 0
		} else if identity.UserType == constants.Manager {
			queryUserID = 0
			queryCompanyID = identity.CompanyID
		}
	}

	sessions, _, err := dao.ListSessions(ctx, queryUserID, queryCompanyID, 100, 0)
	if err != nil {
		return nil, err
	}

	// 收集涉及的用户 ID 批量查昵称/用户名，方便全企会话列表下展示创建人
	userIDs := make([]int64, 0, len(sessions))
	for i := range sessions {
		if sessions[i].UserID > 0 {
			userIDs = append(userIDs, sessions[i].UserID)
		}
	}
	var nameMap map[int64]string
	if len(userIDs) > 0 {
		nameMap, _ = dao.GetUserNamesByIDs(ctx, userIDs)
	}

	res := make([]SessionOut, 0, len(sessions))
	for i := range sessions {
		out := toSessionOut(&sessions[i])
		if sessions[i].UserID == 0 {
			out.CreatorName = "开放API (系统任务)"
		} else if nameMap != nil {
			if n, ok := nameMap[sessions[i].UserID]; ok {
				out.CreatorName = n
			}
		}
		res = append(res, out)
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

	skillRows, err := dao.ListSessionSkills(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	enabledIDs := make([]int64, 0, len(skillRows))
	enabledSkills := make([]EnabledSkillOut, 0, len(skillRows))
	for _, sr := range skillRows {
		enabledIDs = append(enabledIDs, sr.Skill.ID)
		enabledSkills = append(enabledSkills, EnabledSkillOut{
			SkillID:             sr.Skill.ID,
			PinnedVersionNumber: sr.Pinned,
			Source:              sr.Source,
		})
	}

	// 阶段八：会话启用的 MCP Server 回显
	mcpRows, err := dao.ListSessionMcps(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	mcpIDs := make([]int64, 0, len(mcpRows))
	enabledMcps := make([]EnabledMcpOut, 0, len(mcpRows))
	for _, mr := range mcpRows {
		if !mr.Server.Enabled {
			continue // 停用的 server 不回显（关联保留，重新启用后自然恢复）
		}
		mcpIDs = append(mcpIDs, mr.Server.ID)
		enabledMcps = append(enabledMcps, EnabledMcpOut{
			McpServerID: mr.Server.ID,
			Name:        mr.Server.Name,
			ToolCount:   int(mr.Server.ToolCount),
		})
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
		EnabledSkillIDs: enabledIDs,
		EnabledSkills:   enabledSkills,
		EnabledMcpIDs:   mcpIDs,
		EnabledMcps:     enabledMcps,
		PendingQuestion: pending,
		PendingInputs:   human.Pending(messages),
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

	session, err := loadOwnedSession(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := dao.CheckSessionWriteAccess(identity, session); err != nil {
		return nil, err
	}
	if err := turnRegistry.DeleteWhenStopped(ctx, req.ID, func() error {
		if err := dao.DeleteSession(ctx, req.ID); err != nil {
			return err
		}
		sandbox.Close(req.ID)
		return nil
	}); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}

type SetEnabledSkillsReq struct {
	ID     string `uri:"id"`
	Skills []struct {
		SkillID             int64  `json:"skill_id"`
		PinnedVersionNumber *int64 `json:"pinned_version_number"`
		Source              string `json:"source"`
	} `json:"skills"`
}

// SetEnabledSkills 设置会话启用的技能 (PUT /api/sessions/:id/skills)。
// 整体替换语义：请求里的清单就是启用全集；技能 ID 先经注册表存在性校验，
// 防止挂空关联。
func SetEnabledSkills(ctx context.Context, req *SetEnabledSkillsReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	if _, err := loadOwnedSession(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	// 去重（同技能多次出现以最后一次为准）+ 存在性校验
	seen := make(map[int64]dao.SessionSkillItem, len(req.Skills))
	items := make([]dao.SessionSkillItem, 0, len(req.Skills))
	for _, s := range req.Skills {
		if s.SkillID <= 0 {
			return nil, errors.NewMsg("技能 ID 非法")
		}
		src := s.Source
		if src == "" {
			src = "auto"
		}
		seen[s.SkillID] = dao.SessionSkillItem{
			SkillID:             s.SkillID,
			PinnedVersionNumber: s.PinnedVersionNumber,
			Source:              src,
		}
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	skills, err := dao.GetSkillsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(skills) != len(ids) {
		return nil, errors.NewMsg("存在无效的技能 ID，请刷新技能列表后重试")
	}
	for _, item := range seen {
		items = append(items, item)
	}

	if err := dao.ReplaceSessionSkills(ctx, req.ID, items); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}
