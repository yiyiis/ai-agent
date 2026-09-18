package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/dispatcher"
	"backend/pkg/skill"
	"github.com/gin-gonic/gin"
)

type apiKeyCtxKey struct{}

// ApiKeyFromCtx 从 Context 提取已通过校验的 ApiKey 实体
func ApiKeyFromCtx(ctx context.Context) *model.ApiKey {
	if v, ok := ctx.Value(apiKeyCtxKey{}).(*model.ApiKey); ok {
		return v
	}
	return nil
}

// ApiKeyAuthMiddleware 开放 API Key 鉴权中间件 (支持 X-API-Key 与 Bearer Token)
func ApiKeyAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := ""
		if key := c.Request.Header.Get("X-API-Key"); key != "" {
			tokenStr = strings.TrimSpace(key)
		} else if auth := c.Request.Header.Get("Authorization"); auth != "" {
			tokenStr = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}

		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code": http.StatusUnauthorized,
				"msg":  "未授权：缺少 API Key 凭据，请在 Header 中携带 X-API-Key 或 Authorization: Bearer <key>",
			})
			return
		}

		keyRecord, err := dao.GetApiKey(c.Request.Context(), tokenStr)
		if err != nil || keyRecord == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code": http.StatusUnauthorized,
				"msg":  "未授权：无效的 API Key 或密钥已被停用",
			})
			return
		}

		ctx := context.WithValue(c.Request.Context(), apiKeyCtxKey{}, keyRecord)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// SubmitSkillRunReq 提交任务入参
type SubmitSkillRunReq struct {
	SkillName      string         `json:"skill_name" binding:"required"`
	Params         map[string]any `json:"params"`
	Mode           string         `json:"mode"`
	Model          string         `json:"model"`
	UserID         int64          `json:"user_id"` // 有头模式：指定关联的员工 ID；0 则为系统无头模式
	MaxAttempts    int64          `json:"max_attempts"`
	IdempotencyKey string         `json:"idempotency_key"`
}

// SubmitSkillRun 处理异步技能任务提交 (POST /api/v1/skill-runs)
func SubmitSkillRun(c *gin.Context) {
	apiKey := ApiKeyFromCtx(c.Request.Context())
	if apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": http.StatusUnauthorized, "msg": "未授权"})
		return
	}

	var req SubmitSkillRunReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "msg": "参数格式错误: " + err.Error()})
		return
	}

	ctx := c.Request.Context()
	q := db.Ctx(ctx)

	// 1. 目标技能合法性校验
	sk, err := q.Skill.WithContext(ctx).Where(q.Skill.Name.Eq(req.SkillName)).First()
	if err != nil || sk == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "msg": fmt.Sprintf("目标技能 `%s` 不存在", req.SkillName)})
		return
	}

	// 2. 技能白名单鉴权 (is_global=0 时检查 allowed_skills)
	if !apiKey.IsGlobal {
		allowed := false
		if apiKey.AllowedSkills != nil && strings.TrimSpace(*apiKey.AllowedSkills) != "" {
			var allowedList []string
			if json.Unmarshal([]byte(*apiKey.AllowedSkills), &allowedList) == nil {
				for _, name := range allowedList {
					if name == req.SkillName {
						allowed = true
						break
					}
				}
			}
		}
		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{
				"code": http.StatusForbidden,
				"msg":  fmt.Sprintf("该 API Key 无权调用技能 `%s`，未在权限白名单内", req.SkillName),
			})
			return
		}
	}

	// 3. 校验有头模式下关联的 user_id 是否合法且属于当前企业
	if req.UserID > 0 {
		targetUser, uErr := dao.GetUserById(ctx, int32(req.UserID))
		if uErr != nil || targetUser == nil || targetUser.CompanyID != apiKey.CompanyID {
			c.JSON(http.StatusBadRequest, gin.H{
				"code": http.StatusBadRequest,
				"msg":  fmt.Sprintf("指定的目标用户 (user_id=%d) 不存在或不属于当前 API Key 归属的企业", req.UserID),
			})
			return
		}
	}

	// 4. 并发守卫检查
	activeCount, err := dao.CountActiveRunsByKey(ctx, apiKey.ID)
	if err == nil && activeCount >= apiKey.MaxConcurrent {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"code": http.StatusTooManyRequests,
			"msg":  fmt.Sprintf("并发执行任务数已达到上限 (max_concurrent=%d)，请稍后重试", apiKey.MaxConcurrent),
		})
		return
	}

	// 5. 幂等性检测
	if req.IdempotencyKey != "" {
		existing, _ := dao.GetSkillRunByRunIDAndCompany(ctx, req.IdempotencyKey, apiKey.CompanyID)
		if existing != nil {
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": gin.H{
					"run_id":     existing.RunID,
					"skill_name": existing.SkillName,
					"status":     existing.Status,
					"created_at": existing.CreatedAt.UTC().Format(time.RFC3339),
				},
			})
			return
		}
	}

	// 6. 生成 run_id
	runID := req.IdempotencyKey
	if runID == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		runID = "run_" + hex.EncodeToString(b)
	}

	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if maxAttempts > 5 {
		maxAttempts = 5
	}

	var paramsJSONStr *string
	if req.Params != nil {
		if b, err := json.Marshal(req.Params); err == nil {
			s := string(b)
			paramsJSONStr = &s
		}
	}

	execCtxMap := map[string]any{
		"mode":  req.Mode,
		"model": req.Model,
	}
	execCtxBytes, _ := json.Marshal(execCtxMap)
	execCtxStr := string(execCtxBytes)

	now := time.Now()
	newRun := &model.SkillRun{
		RunID:       runID,
		SkillName:   req.SkillName,
		SkillID:     &sk.ID,
		CompanyID:   apiKey.CompanyID,
		UserID:      req.UserID,
		APIKeyID:    &apiKey.ID,
		Status:      "pending",
		Params:      paramsJSONStr,
		Attempt:     1,
		MaxAttempts: maxAttempts,
		ExecContext: &execCtxStr,
		CreatedAt:   now,
	}

	if err := dao.CreateSkillRun(ctx, newRun); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "msg": "创建任务记录失败: " + err.Error()})
		return
	}

	// 投递至后台调度引擎
	dispatcher.Default().Submit(runID)

	c.JSON(http.StatusCreated, gin.H{
		"code": 0,
		"msg":  "success",
		"data": gin.H{
			"run_id":     runID,
			"skill_name": req.SkillName,
			"status":     "pending",
			"created_at": now.UTC().Format(time.RFC3339),
		},
	})
}

// GetSkillRun 查询单次任务详情与产物 (GET /api/v1/skill-runs/:run_id)
func GetSkillRun(c *gin.Context) {
	apiKey := ApiKeyFromCtx(c.Request.Context())
	if apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": http.StatusUnauthorized, "msg": "未授权"})
		return
	}

	runID := c.Param("run_id")
	ctx := c.Request.Context()

	run, err := dao.GetSkillRunByRunIDAndCompany(ctx, runID, apiKey.CompanyID)
	if err != nil || run == nil {
		c.JSON(http.StatusNotFound, gin.H{"code": http.StatusNotFound, "msg": "任务不存在或无权访问"})
		return
	}

	respData := formatRunDetail(run)
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "success",
		"data": respData,
	})
}

// CancelSkillRun 取消/强杀任务 (POST /api/v1/skill-runs/:run_id/cancel)
func CancelSkillRun(c *gin.Context) {
	apiKey := ApiKeyFromCtx(c.Request.Context())
	if apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": http.StatusUnauthorized, "msg": "未授权"})
		return
	}

	runID := c.Param("run_id")
	ctx := c.Request.Context()

	run, err := dao.GetSkillRunByRunIDAndCompany(ctx, runID, apiKey.CompanyID)
	if err != nil || run == nil {
		c.JSON(http.StatusNotFound, gin.H{"code": http.StatusNotFound, "msg": "任务不存在或无权访问"})
		return
	}

	if run.Status == "success" || run.Status == "failed" || run.Status == "canceled" {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"msg":  "success",
			"data": gin.H{"status": run.Status},
		})
		return
	}

	if err := dispatcher.Default().Cancel(ctx, runID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "msg": "取消任务失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "success",
		"data": gin.H{"status": "canceled"},
	})
}

// ListApiSkills 查询当前 API Key 允许调用的技能清单 (GET /api/v1/skills)
func ListApiSkills(c *gin.Context) {
	apiKey := ApiKeyFromCtx(c.Request.Context())
	if apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": http.StatusUnauthorized, "msg": "未授权"})
		return
	}

	ctx := c.Request.Context()
	allSkills, err := dao.ListSkills(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "msg": "查询技能列表失败"})
		return
	}

	allowedMap := make(map[string]bool)
	if !apiKey.IsGlobal && apiKey.AllowedSkills != nil {
		var list []string
		_ = json.Unmarshal([]byte(*apiKey.AllowedSkills), &list)
		for _, name := range list {
			allowedMap[name] = true
		}
	}

	var results []gin.H
	for _, s := range allSkills {
		if !apiKey.IsGlobal && !allowedMap[s.Name] {
			continue
		}

		var tags []string
		if s.Tags != nil && *s.Tags != "" {
			_ = json.Unmarshal([]byte(*s.Tags), &tags)
		}

		// 尝试从 SKILL.md 解析 parameters
		var paramsMeta map[string]any
		if body, err := skill.ReadSkillMD(s.DirName); err == nil {
			parsed := skill.ParseSkillMD(body)
			paramsMeta = parsed.Parameters
		}

		results = append(results, gin.H{
			"name":        s.Name,
			"description": s.Description,
			"dir_name":    s.DirName,
			"tags":        tags,
			"parameters":  paramsMeta,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "success",
		"data": results,
	})
}

func formatRunDetail(run *model.SkillRun) gin.H {
	var paramsObj any = map[string]any{}
	if run.Params != nil && *run.Params != "" {
		_ = json.Unmarshal([]byte(*run.Params), &paramsObj)
	}

	var resultObj any
	if run.Result != nil && *run.Result != "" {
		_ = json.Unmarshal([]byte(*run.Result), &resultObj)
	}

	var artifactsObj any = []any{}
	if run.Artifacts != nil && *run.Artifacts != "" {
		_ = json.Unmarshal([]byte(*run.Artifacts), &artifactsObj)
	}

	var progressObj any = []any{}
	if run.Progress != nil && *run.Progress != "" {
		_ = json.Unmarshal([]byte(*run.Progress), &progressObj)
	}

	var attemptsObj any = []any{}
	if run.Attempts != nil && *run.Attempts != "" {
		_ = json.Unmarshal([]byte(*run.Attempts), &attemptsObj)
	}

	var finishedAtStr *string
	if run.FinishedAt != nil {
		s := run.FinishedAt.UTC().Format(time.RFC3339)
		finishedAtStr = &s
	}

	// 动态判定可重试状态
	retryable := (run.Status == "failed" || run.Status == "timeout" || run.Status == "canceled")

	return gin.H{
		"id":           run.RunID,
		"status":       run.Status,
		"duration_ms":  run.DurationMs,
		"error":        run.Error,
		"created_at":   run.CreatedAt.UTC().Format(time.RFC3339),
		"finished_at":  finishedAtStr,
		"params":       paramsObj,
		"result":       resultObj,
		"progress":     progressObj,
		"artifacts":    artifactsObj,
		"stderr_tail":  run.StderrTail,
		"attempt":      run.Attempt,
		"max_attempts": run.MaxAttempts,
		"failure_kind": run.FailureKind,
		"retryable":    retryable,
		"attempts":     attemptsObj,
	}
}
