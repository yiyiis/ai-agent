package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/dispatcher"
	"backend/pkg/sandbox"
)

func TestOpenApiAuthAndValidation(t *testing.T) {
	setupTestServer()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: t.TempDir()})

	// 1. 无 Header 访问 -> 401
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/skill-runs", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing API Key, got %d", w.Code)
	}

	// 2. 伪造错误 Key -> 401
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/skill-runs", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "sk_fake_invalid_key_12345")
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid API Key, got %d", w.Code)
	}
}

func TestOpenApiWhitelistAndConcurrency(t *testing.T) {
	setupTestServer()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: t.TempDir()})
	ctx := db.WithContext(context.Background())

	// 准备合法的技能数据
	skName := "api-test-skill"
	q := db.Ctx(ctx)
	_ = q.Skill.WithContext(ctx).Create(&model.Skill{
		Name:        skName,
		Description: "开放 API 测试技能",
		DirName:     skName,
	})
	t.Cleanup(func() {
		_, _ = q.Skill.WithContext(ctx).Where(q.Skill.Name.Eq(skName)).Delete()
	})

	// 创建带白名单与并发限制的 API Key
	apiKeyStr := fmt.Sprintf("sk_test_%d", time.Now().UnixNano())
	allowedJSON := `["other-skill"]`
	key := &model.ApiKey{
		APIKey:        apiKeyStr,
		Name:          "白名单测试Key",
		CompanyID:     999,
		IsGlobal:      false,
		AllowedSkills: &allowedJSON,
		MaxConcurrent: 1,
		IsActive:      true,
	}
	if err := dao.CreateApiKey(ctx, key); err != nil {
		t.Fatalf("CreateApiKey failed: %v", err)
	}

	// 3. 调用未在白名单的技能 -> 403 Forbidden
	body, _ := json.Marshal(map[string]any{
		"skill_name": skName,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/skill-runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKeyStr)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for forbidden skill, got %d body=%s", w.Code, w.Body.String())
	}

	// 更新 Key 为全局权限 (is_global=1)，测试并发配额限制
	q.ApiKey.WithContext(ctx).Where(q.ApiKey.ID.Eq(key.ID)).Update(q.ApiKey.IsGlobal, true)

	// 创建一条处于 pending 的假任务占用并发配额
	occupiedRunID := fmt.Sprintf("run_occ_%d", time.Now().UnixNano())
	_ = dao.CreateSkillRun(ctx, &model.SkillRun{
		RunID:       occupiedRunID,
		SkillName:   skName,
		CompanyID:   999,
		APIKeyID:    &key.ID,
		Status:      "pending",
		Attempt:     1,
		MaxAttempts: 1,
	})
	t.Cleanup(func() {
		_, _ = q.SkillRun.WithContext(ctx).Where(q.SkillRun.RunID.Eq(occupiedRunID)).Delete()
	})

	// 4. 超出最大并发上限 (max_concurrent=1) -> 429 Too Many Requests
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/skill-runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKeyStr)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for concurrency limit, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOpenApiSubmitGetAndCancel(t *testing.T) {
	setupTestServer()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: t.TempDir()})
	ctx := db.WithContext(context.Background())

	disp := dispatcher.Init(5)
	disp.Start(ctx)
	defer disp.Stop()

	skName := "api-lifecycle-skill"
	q := db.Ctx(ctx)
	_ = q.Skill.WithContext(ctx).Create(&model.Skill{
		Name:        skName,
		Description: "生命周期测试技能",
		DirName:     skName,
	})
	t.Cleanup(func() {
		_, _ = q.Skill.WithContext(ctx).Where(q.Skill.Name.Eq(skName)).Delete()
	})

	apiKeyStr := fmt.Sprintf("sk_global_%d", time.Now().UnixNano())
	key := &model.ApiKey{
		APIKey:        apiKeyStr,
		Name:          "全局测试Key",
		CompanyID:     888,
		IsGlobal:      true,
		MaxConcurrent: 10,
		IsActive:      true,
	}
	if err := dao.CreateApiKey(ctx, key); err != nil {
		t.Fatalf("CreateApiKey failed: %v", err)
	}

	// 5. 提交任务 -> 201 Created
	submitBody, _ := json.Marshal(map[string]any{
		"skill_name": skName,
		"params":     map[string]any{"input": "hello"},
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/skill-runs", bytes.NewReader(submitBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKeyStr)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d body=%s", w.Code, w.Body.String())
	}

	var submitResp struct {
		Code int `json:"code"`
		Data struct {
			RunID     string `json:"run_id"`
			SkillName string `json:"skill_name"`
			Status    string `json:"status"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &submitResp)
	runID := submitResp.Data.RunID
	if runID == "" {
		t.Fatalf("expected non-empty run_id")
	}

	// 6. 查询任务详情 -> 200 OK
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/skill-runs/"+runID, nil)
	req.Header.Set("X-API-Key", apiKeyStr)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get run, got %d", w.Code)
	}

	// 7. 取消任务 -> 200 OK (status: canceled)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/skill-runs/"+runID+"/cancel", nil)
	req.Header.Set("X-API-Key", apiKeyStr)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for cancel, got %d", w.Code)
	}

	var cancelResp struct {
		Code int `json:"code"`
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &cancelResp)
	if cancelResp.Data.Status != "canceled" && cancelResp.Data.Status != "success" {
		t.Fatalf("unexpected cancel status: %s", cancelResp.Data.Status)
	}

	// 8. 查询当前 Key 可用的技能清单 -> 200 OK
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/skills", nil)
	req.Header.Set("X-API-Key", apiKeyStr)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list skills, got %d", w.Code)
	}
}

func TestOpenApiHeadedAndMultiTenantAuth(t *testing.T) {
	setupTestServer()
	ctx := db.WithContext(context.Background())

	suffix := time.Now().UnixNano()
	codeA := fmt.Sprintf("tenant_a_%d", suffix)
	compA := &model.Company{
		Name:   "租户企业 A",
		Code:   codeA,
		Status: 1,
	}
	_ = dao.CreateCompany(ctx, compA)

	codeB := fmt.Sprintf("tenant_b_%d", suffix)
	compB := &model.Company{
		Name:   "租户企业 B",
		Code:   codeB,
		Status: 1,
	}
	_ = dao.CreateCompany(ctx, compB)

	// 1. 在 企业 A 注册员工 alice
	uname := fmt.Sprintf("alice_%d", suffix)
	regReqA, _ := json.Marshal(map[string]string{
		"username":     uname,
		"password":     "pass123",
		"nickname":     "爱丽丝A",
		"company_code": codeA,
	})
	w := httptest.NewRecorder()
	httpReq, _ := http.NewRequest("POST", "/api/auth/register", bytes.NewReader(regReqA))
	httpReq.Header.Set("Content-Type", "application/json")
	testEngine.ServeHTTP(w, httpReq)
	if w.Code != http.StatusOK {
		t.Fatalf("Register alice in Company A failed: %s", w.Body.String())
	}
	var regRespA struct {
		Data struct {
			Token string `json:"token"`
			User  struct {
				UserID    int64 `json:"user_id"`
				CompanyID int64 `json:"company_id"`
			} `json:"user"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &regRespA)
	aliceAUserID := regRespA.Data.User.UserID

	// 2. 在 企业 B 注册同名员工 alice
	regReqB, _ := json.Marshal(map[string]string{
		"username":     uname,
		"password":     "pass456",
		"nickname":     "爱丽丝B",
		"company_code": codeB,
	})
	w = httptest.NewRecorder()
	httpReq, _ = http.NewRequest("POST", "/api/auth/register", bytes.NewReader(regReqB))
	httpReq.Header.Set("Content-Type", "application/json")
	testEngine.ServeHTTP(w, httpReq)
	if w.Code != http.StatusOK {
		t.Fatalf("Register same alice in Company B failed: %s", w.Body.String())
	}

	// 3. 准备 企业 A 的 API Key
	keyStrA := fmt.Sprintf("sk_a_%d", suffix)
	keyA := &model.ApiKey{
		APIKey:        keyStrA,
		Name:          "企业A的Key",
		CompanyID:     compA.ID,
		IsGlobal:      true,
		MaxConcurrent: 5,
		IsActive:      true,
	}
	_ = dao.CreateApiKey(ctx, keyA)

	// 准备可用技能
	skName := fmt.Sprintf("sk_%d", suffix)
	_ = db.Ctx(ctx).Skill.WithContext(ctx).Create(&model.Skill{
		Name:        skName,
		Description: "测试技能",
		DirName:     skName,
	})

	// 4. 使用 企业 A 的 Key 发起有头任务，指定属于 企业 B 的用户 -> 应报 400 Bad Request
	otherUser, _ := dao.GetUserByUsernameAndCompany(ctx, uname, compB.ID)
	invalidBody, _ := json.Marshal(map[string]any{
		"skill_name": skName,
		"user_id":    otherUser.UserID,
	})
	w = httptest.NewRecorder()
	httpReq, _ = http.NewRequest("POST", "/api/v1/skill-runs", bytes.NewReader(invalidBody))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", keyStrA)
	testEngine.ServeHTTP(w, httpReq)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for cross-tenant user_id, got %d", w.Code)
	}

	// 5. 使用 企业 A 的 Key 发起有头任务，指定属于企业 A 的 alice -> 成功
	validBody, _ := json.Marshal(map[string]any{
		"skill_name": skName,
		"user_id":    aliceAUserID,
	})
	w = httptest.NewRecorder()
	httpReq, _ = http.NewRequest("POST", "/api/v1/skill-runs", bytes.NewReader(validBody))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", keyStrA)
	testEngine.ServeHTTP(w, httpReq)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("expected 200/201 for valid headed run, got %d body=%s", w.Code, w.Body.String())
	}

	var runResp struct {
		Data struct {
			RunID string `json:"run_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &runResp)
	createdRun, _ := dao.GetSkillRunByRunID(ctx, runResp.Data.RunID)
	if createdRun == nil || createdRun.UserID != aliceAUserID || createdRun.CompanyID != compA.ID {
		t.Fatalf("SkillRun record user_id/company_id mismatch, got: %+v", createdRun)
	}

	// 6. 测试会话 RBAC scope=all 接口
	// 在企业 A 创建一个员工会话并带有消息
	sessIDA := fmt.Sprintf("sess_alice_%d", suffix)
	_ = dao.CreateSession(ctx, &model.Session{
		SessionID: sessIDA,
		CompanyID: compA.ID,
		UserID:    aliceAUserID,
		Title:     "爱丽丝对话",
		Model:     "MiniMax-Text-01",
	})
	_ = dao.CreateMessage(ctx, &model.Message{
		MessageID: fmt.Sprintf("msg_%d", suffix),
		SessionID: sessIDA,
		Role:      "user",
		Content:   "你好",
	})

	// aliceA (普通员工) 请求 /api/sessions?scope=all -> 自动受限于本人
	w = httptest.NewRecorder()
	httpReq, _ = http.NewRequest("GET", "/api/sessions?scope=all", nil)
	httpReq.Header.Set("Authorization", "Bearer "+regRespA.Data.Token)
	testEngine.ServeHTTP(w, httpReq)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions?scope=all failed: %d", w.Code)
	}
}

