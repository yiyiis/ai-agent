package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"backend/api"
	"backend/config"
	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/jwt"
	"backend/pkg/validate"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var testEngine *gin.Engine

func setupTestServer() {
	if testEngine != nil {
		return
	}

	// 寻找配置文件
	confPath := "../etc/config.yaml"
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		confPath = filepath.Join("..", "etc", "config.yaml")
	}

	conf := config.LoadConfig(confPath)
	db.InitDb(conf.DbConf)
	jwt.InitJwt(conf.Auth)
	validate.InitGinValidate()

	gin.SetMode(gin.TestMode)
	engine := gin.Default()
	engine.Use(db.InjectQuery)
	engine.Use(jwt.CheckLogin)

	engine.GET("/api/ping", api.Ping)
	engine.GET("/api/models", api.ListModels)

	authGroup := engine.Group("/api/auth")
	{
		authGroup.POST("/login", api.UserLogin)
		authGroup.GET("/me", api.AuthMe)
		authGroup.POST("/register", api.UserRegister)
		authGroup.POST("/logout", api.UserLogout)
	}

	sessionGroup := engine.Group("/api/sessions")
	{
		sessionGroup.POST("", api.CreateSession)
		sessionGroup.GET("", api.ListSessions)
		sessionGroup.GET("/:id", api.GetSessionDetail)
		sessionGroup.PATCH("/:id", api.UpdateSession)
		sessionGroup.DELETE("/:id", api.DeleteSession)
		sessionGroup.PUT("/:id/skills", api.SetEnabledSkills)
		sessionGroup.POST("/:id/messages", api.SendMessageStream)
	}

	testEngine = engine
}

func TestPingAndModels(t *testing.T) {
	setupTestServer()

	// 1. Test Ping
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/ping", nil)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected ping 200, got %d", w.Code)
	}

	// 2. Test Models
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/models", nil)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected models 200, got %d", w.Code)
	}
	var models []api.ModelItem
	if err := json.Unmarshal(w.Body.Bytes(), &models); err != nil {
		t.Fatalf("failed to decode models: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one model in list")
	}

	// 验证模型列表中无重复 ID，且包含 MiniMax 与 GLM 相应模型，且不包含废弃模型
	seenIDs := make(map[string]bool)
	for _, m := range models {
		if seenIDs[m.ID] {
			t.Errorf("found duplicate model ID in response: %s", m.ID)
		}
		seenIDs[m.ID] = true

		if m.ID == "doubao-pro-32k" || m.ID == "deepseek-chat" || m.ID == "deepseek-reasoner" {
			t.Errorf("unexpected deprecated model found: %s", m.ID)
		}
	}

	expectedModels := []string{
		"MiniMax-Text-01",
		"MiniMax-M3",
		"GLM-5.3",
		"GLM-5.3-Flash",
		"GLM-5.2",
		"GLM-5.1",
		"GLM-5-Turbo",
		"GLM-4.7",
	}
	for _, exp := range expectedModels {
		if !seenIDs[exp] {
			t.Errorf("expected model %s to be present, but was missing", exp)
		}
	}
}

func TestUserIsolation(t *testing.T) {
	setupTestServer()

	// 生成 User 1 Token
	token1, err := jwt.GenAccessToken(jwt.AuthIdentity{
		UserID:    101,
		CompanyID: 1,
		Name:      "User 101",
		Username:  "user101",
		UserType:  constants.User,
	})
	if err != nil {
		t.Fatalf("failed to generate token1: %v", err)
	}

	// 生成 User 2 Token
	token2, err := jwt.GenAccessToken(jwt.AuthIdentity{
		UserID:    102,
		CompanyID: 1,
		Name:      "User 102",
		Username:  "user102",
		UserType:  constants.User,
	})
	if err != nil {
		t.Fatalf("failed to generate token2: %v", err)
	}

	// 1. User 1 创建一个会话
	createBody, _ := json.Marshal(map[string]string{
		"title": "User1专属机密会话",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("User 1 failed to create session: status=%d body=%s", w.Code, w.Body.String())
	}
	var s1 api.SessionOut
	if err := json.Unmarshal(w.Body.Bytes(), &s1); err != nil {
		t.Fatalf("failed to parse session: %v", err)
	}
	if s1.ID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// 插入一条对话消息，使会话成为包含实际发言的活跃会话
	_ = dao.CreateMessage(context.Background(), &model.Message{
		MessageID: uuid.New().String(),
		SessionID: s1.ID,
		Role:      "user",
		Content:   "测试用户发言",
	})

	// 2. User 1 查询会话列表，能够看到该会话
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/sessions", nil)
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("User 1 list sessions failed: %d", w.Code)
	}
	var list1 []api.SessionOut
	_ = json.Unmarshal(w.Body.Bytes(), &list1)
	found1 := false
	for _, s := range list1 {
		if s.ID == s1.ID {
			found1 = true
			break
		}
	}
	if !found1 {
		t.Fatalf("User 1 did not find own session %s", s1.ID)
	}

	// 3. User 2 查询会话列表，严格不可见 User 1 的会话
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/sessions", nil)
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("User 2 list sessions failed: %d", w.Code)
	}
	var list2 []api.SessionOut
	_ = json.Unmarshal(w.Body.Bytes(), &list2)
	for _, s := range list2 {
		if s.ID == s1.ID {
			t.Fatalf("Security breach: User 2 saw User 1's session %s in list", s1.ID)
		}
	}

	// 4. User 2 尝试直接探测或访问 User 1 的会话详情 -> 必须返回 404 (禁止泄露存在性)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/sessions/"+s1.ID, nil)
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Security breach: User 2 received status %d instead of 404 for User 1's session", w.Code)
	}

	// 5. User 2 尝试修改 User 1 的会话 -> 必须返回 404
	patchBody, _ := json.Marshal(map[string]string{"title": "恶意越权修改"})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/sessions/"+s1.ID, bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Security breach: User 2 received status %d instead of 404 when patching User 1's session", w.Code)
	}

	// 6. User 2 尝试删除 User 1 的会话 -> 必须返回 404
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/sessions/"+s1.ID, nil)
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Security breach: User 2 received status %d instead of 404 when deleting User 1's session", w.Code)
	}

	// 7. User 1 正常删除自己的会话
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/sessions/"+s1.ID, nil)
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("User 1 failed to delete own session: %d", w.Code)
	}
}

func TestAuthFlow(t *testing.T) {
	setupTestServer()

	// 1. 错误密码登录 -> 401
	badLogin, _ := json.Marshal(map[string]string{
		"account":  "admin",
		"password": "wrong_password",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(badLogin))
	req.Header.Set("Content-Type", "application/json")
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad password, got %d", w.Code)
	}

	// 2. 正确密码登录 -> 200，并返回 JWT
	goodLogin, _ := json.Marshal(map[string]string{
		"account":  "admin",
		"password": "your_password",
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/auth/login", bytes.NewReader(goodLogin))
	req.Header.Set("Content-Type", "application/json")
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for good login, got %d (body: %s)", w.Code, w.Body.String())
	}
	var loginRes api.LoginResp
	if err := json.Unmarshal(w.Body.Bytes(), &loginRes); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}
	if loginRes.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if loginRes.User.Username != "admin" {
		t.Fatalf("expected username 'admin', got '%s'", loginRes.User.Username)
	}

	// 验证 Cookie 设置
	cookies := w.Result().Cookies()
	hasAgentCookie := false
	for _, c := range cookies {
		if c.Name == "agents_token" && c.Value != "" {
			hasAgentCookie = true
			break
		}
	}
	if !hasAgentCookie {
		t.Fatal("expected agents_token cookie in login response")
	}

	// 3. /api/auth/me 校验登录态
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/auth/me", nil)
	req.Header.Set("x-token", loginRes.Token)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/auth/me, got %d", w.Code)
	}
	var meRes api.UserInfoResp
	if err := json.Unmarshal(w.Body.Bytes(), &meRes); err != nil {
		t.Fatalf("failed to decode auth me: %v", err)
	}
	if meRes.Username != "admin" {
		t.Fatalf("expected me.Username == 'admin', got '%s'", meRes.Username)
	}

	// 4. 用户注册
	registerBody, _ := json.Marshal(map[string]string{
		"username": "tester_dev",
		"password": "password123",
		"nickname": "测试工程师",
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/auth/register", bytes.NewReader(registerBody))
	req.Header.Set("Content-Type", "application/json")
	testEngine.ServeHTTP(w, req)
	// 允许 200 (首次创建) 或 409 (已被注册过)
	if w.Code != http.StatusOK && w.Code != http.StatusConflict {
		t.Fatalf("expected register 200 or 409, got %d", w.Code)
	}

	// 5. 退出登录
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/auth/logout", nil)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected logout 200, got %d", w.Code)
	}
}

