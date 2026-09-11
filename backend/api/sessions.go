package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"backend/config"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/jwt"
	"github.com/gin-gonic/gin"
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
}

// CreateSession 创建会话 (POST /api/sessions)
func CreateSession(c *gin.Context) {
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Title        string  `json:"title"`
		Model        string  `json:"model"`
		SystemPrompt *string `json:"system_prompt"`
	}
	_ = c.ShouldBindJSON(&req)

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新会话"
	}

	// 防重复创建机制：若用户存在尚无任何消息的空会话，直接复用返回现有空会话
	latestEmpty, err := dao.GetLatestEmptySession(c.Request.Context(), identity.UserID)
	if err == nil && latestEmpty != nil {
		var promptPtr *string
		if latestEmpty.SystemPrompt != "" {
			promptPtr = &latestEmpty.SystemPrompt
		}
		c.JSON(http.StatusOK, SessionOut{
			ID:           latestEmpty.SessionID,
			Title:        latestEmpty.Title,
			Model:        latestEmpty.Model,
			SystemPrompt: promptPtr,
			AutoSkill:    latestEmpty.AutoSkill,
			CreatedAt:    latestEmpty.CreatedAt.Format(time.RFC3339),
			UpdatedAt:    latestEmpty.UpdatedAt.Format(time.RFC3339),
		})
		return
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = config.GetConfig().LLM.DefaultModel
	}
	if modelName == "" {
		modelName = "MiniMax-Text-01"
	}
	sysPrompt := ""
	if req.SystemPrompt != nil {
		sysPrompt = *req.SystemPrompt
	}

	sessionID := uuid.New().String()
	now := time.Now()
	session := &model.Session{
		SessionID:    sessionID,
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

	if err := dao.CreateSession(c.Request.Context(), session); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建会话失败: " + err.Error()})
		return
	}

	var promptPtr *string
	if sysPrompt != "" {
		promptPtr = &sysPrompt
	}

	c.JSON(http.StatusOK, SessionOut{
		ID:           sessionID,
		Title:        title,
		Model:        modelName,
		SystemPrompt: promptPtr,
		AutoSkill:    true,
		CreatedAt:    now.Format(time.RFC3339),
		UpdatedAt:    now.Format(time.RFC3339),
	})
}

// ListSessions 获取当前用户所有会话列表 (GET /api/sessions)
func ListSessions(c *gin.Context) {
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sessions, _, err := dao.ListSessions(c.Request.Context(), identity.UserID, identity.CompanyID, 100, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询会话失败: " + err.Error()})
		return
	}

	res := make([]SessionOut, 0, len(sessions))
	for _, s := range sessions {
		var promptPtr *string
		if s.SystemPrompt != "" {
			promptPtr = &s.SystemPrompt
		}
		res = append(res, SessionOut{
			ID:           s.SessionID,
			Title:        s.Title,
			Model:        s.Model,
			SystemPrompt: promptPtr,
			AutoSkill:    s.AutoSkill,
			CreatedAt:    s.CreatedAt.Format(time.RFC3339),
			UpdatedAt:    s.UpdatedAt.Format(time.RFC3339),
		})
	}

	c.JSON(http.StatusOK, res)
}

// GetSessionDetail 获取会话详情及历史消息 (GET /api/sessions/:id)
func GetSessionDetail(c *gin.Context) {
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sessionID := c.Param("id")
	session, err := dao.GetSessionBySessionID(c.Request.Context(), sessionID)
	if err != nil || session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	if session.UserID != identity.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	messages, err := dao.ListMessagesBySessionID(c.Request.Context(), sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取历史消息失败: " + err.Error()})
		return
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

	var promptPtr *string
	if session.SystemPrompt != "" {
		promptPtr = &session.SystemPrompt
	}

	c.JSON(http.StatusOK, SessionDetailOut{
		SessionOut: SessionOut{
			ID:           session.SessionID,
			Title:        session.Title,
			Model:        session.Model,
			SystemPrompt: promptPtr,
			AutoSkill:    session.AutoSkill,
			CreatedAt:    session.CreatedAt.Format(time.RFC3339),
			UpdatedAt:    session.UpdatedAt.Format(time.RFC3339),
		},
		Messages:        msgOuts,
		EnabledSkillIDs: []int64{},
	})
}

// UpdateSession 更新会话 (PATCH /api/sessions/:id)
func UpdateSession(c *gin.Context) {
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sessionID := c.Param("id")
	session, err := dao.GetSessionBySessionID(c.Request.Context(), sessionID)
	if err != nil || session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	if session.UserID != identity.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	var req struct {
		Title        *string `json:"title"`
		SystemPrompt *string `json:"system_prompt"`
		Model        *string `json:"model"`
		AutoSkill    *bool   `json:"auto_skill"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
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
		if err := dao.UpdateSession(c.Request.Context(), sessionID, updates); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败: " + err.Error()})
			return
		}
	}

	var promptPtr *string
	if session.SystemPrompt != "" {
		promptPtr = &session.SystemPrompt
	}

	c.JSON(http.StatusOK, SessionOut{
		ID:           session.SessionID,
		Title:        session.Title,
		Model:        session.Model,
		SystemPrompt: promptPtr,
		AutoSkill:    session.AutoSkill,
		CreatedAt:    session.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    time.Now().Format(time.RFC3339),
	})
}

// DeleteSession 删除会话 (DELETE /api/sessions/:id)
func DeleteSession(c *gin.Context) {
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sessionID := c.Param("id")
	session, err := dao.GetSessionBySessionID(c.Request.Context(), sessionID)
	if err != nil || session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	if session.UserID != identity.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	if err := dao.DeleteSession(c.Request.Context(), sessionID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除会话失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SetEnabledSkills 设置会话启用的技能 (PUT /api/sessions/:id/skills)
func SetEnabledSkills(c *gin.Context) {
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sessionID := c.Param("id")
	session, err := dao.GetSessionBySessionID(c.Request.Context(), sessionID)
	if err != nil || session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	if session.UserID != identity.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	// 预留技能关联同步逻辑
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

