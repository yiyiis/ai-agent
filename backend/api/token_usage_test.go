package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/api"
	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/jwt"
	"backend/pkg/provider"
)

// TestTokenUsageStatsRBACAndBilling 验证用量统计的三级 RBAC 分流与查询时计费换算
func TestTokenUsageStatsRBACAndBilling(t *testing.T) {
	setupTestServer()
	ctx := db.WithContext(context.Background())
	q := db.Ctx(ctx)

	nano := time.Now().UnixNano()
	// 唯一模型名/企业/用户隔离测试数据,保证断言不受库内真实数据干扰
	pricedModel := fmt.Sprintf("PricingModel-%d", nano)
	unknownModel := fmt.Sprintf("UnknownModel-%d", nano)
	cid := nano + 1
	uidA := nano + 2
	uidB := nano + 3

	// 测试内自设单价表(setupTestServer 不初始化 Provider,单价表默认为空)
	provider.SetModelPrices([]provider.ModelPrice{
		{Model: pricedModel, Input: 2.0, Output: 8.0, CachedInput: 0.2},
	})

	insert := func(row *model.TokenUsage) {
		row.CompanyID = cid
		if err := dao.CreateTokenUsage(ctx, row); err != nil {
			t.Fatalf("CreateTokenUsage failed: %v", err)
		}
	}
	insert(&model.TokenUsage{SessionID: fmt.Sprintf("s_%d_1", nano), UserID: uidA, Model: pricedModel, ProviderKey: "minimax",
		Purpose: "turn", RoundIndex: 0, PromptTokens: 1_000_000})
	insert(&model.TokenUsage{SessionID: fmt.Sprintf("s_%d_1", nano), UserID: uidA, Model: pricedModel, ProviderKey: "minimax",
		Purpose: "turn", RoundIndex: 1, PromptTokens: 300, CompletionTokens: 45, CachedTokens: 80, ReasoningTokens: 20})
	insert(&model.TokenUsage{SessionID: fmt.Sprintf("s_%d_2", nano), UserID: uidA, Model: unknownModel,
		Purpose: "turn", RoundIndex: 0, PromptTokens: 500})
	insert(&model.TokenUsage{SessionID: fmt.Sprintf("s_%d_3", nano), UserID: uidB, Model: pricedModel, ProviderKey: "minimax",
		Purpose: "turn", RoundIndex: 0, PromptTokens: 100})
	// 无头任务(user_id=0):仅企业及以上视角可见
	insert(&model.TokenUsage{SessionID: fmt.Sprintf("run_%d", nano), UserID: 0, Model: pricedModel, ProviderKey: "minimax",
		Purpose: "turn", RoundIndex: 0, PromptTokens: 200})

	t.Cleanup(func() {
		_, _ = q.TokenUsage.WithContext(ctx).Where(q.TokenUsage.CompanyID.Eq(cid)).Delete()
	})

	tokenOf := func(uid int64, utype constants.UserType) string {
		tok, err := jwt.GenAccessToken(jwt.TokenClaims{
			UserID: uid, CompanyID: cid, Name: "t", Username: fmt.Sprintf("u%d", uid), UserType: utype,
		})
		if err != nil {
			t.Fatalf("gen token failed: %v", err)
		}
		return tok
	}
	tokA := tokenOf(uidA, constants.User)
	tokB := tokenOf(uidB, constants.User)
	tokManager := tokenOf(nano+9, constants.Manager)
	tokSuper := tokenOf(nano+8, constants.SuperManager)

	fetch := func(token, query string) api.TokenUsageStatsOut {
		t.Helper()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/usage/stats"+query, nil)
		req.Header.Set("x-token", token)
		testEngine.ServeHTTP(w, req)
		env := decodeEnvelope(t, w)
		if env.Code != 0 {
			t.Fatalf("GET /api/usage/stats%s failed: code=%d msg=%s", query, env.Code, env.Msg)
		}
		var out api.TokenUsageStatsOut
		if err := json.Unmarshal(env.Data, &out); err != nil {
			t.Fatalf("decode stats failed: %v", err)
		}
		return out
	}

	// 1. 员工 A 默认视角:仅本人 3 行(含未知模型行)
	outA := fetch(tokA, "")
	if outA.Total.Calls != 3 || outA.Total.PromptTokens != 1_000_000+300+500 {
		t.Fatalf("employee A mismatched total: %+v", outA.Total)
	}

	// 2. 计费换算:配价模型按公式精算,未知模型金额为 0 但 token 照常
	//    (1e6 uncapped 输入 2.0 元 + 220/1e6*2 + 80/1e6*0.2 + 45/1e6*8 = 2.000816)
	if outA.Total.CostEstimate != 2.000816 {
		t.Fatalf("expected total cost 2.000816, got %v", outA.Total.CostEstimate)
	}
	var unknownPriced bool
	for _, m := range outA.ByModel {
		if m.Model == unknownModel {
			unknownPriced = true
			if m.CostEstimate != 0 || m.PromptTokens != 500 {
				t.Fatalf("unknown model should cost 0 with tokens intact: %+v", m)
			}
		}
	}
	if !unknownPriced {
		t.Fatalf("by_model missing unknown model: %+v", outA.ByModel)
	}

	// 3. 员工 scope=all 不提权:仍只看本人
	if out := fetch(tokA, "?scope=all"); out.Total.Calls != 3 {
		t.Fatalf("employee scope=all should stay personal, got %d calls", out.Total.Calls)
	}

	// 4. 员工 B:仅本人 1 行
	if out := fetch(tokB, ""); out.Total.Calls != 1 || out.Total.PromptTokens != 100 {
		t.Fatalf("employee B mismatched: %+v", out.Total)
	}

	// 5. 企管 scope=all:本企业全量 5 行(含无头任务 user_id=0 的账)
	if out := fetch(tokManager, "?scope=all"); out.Total.Calls != 5 {
		t.Fatalf("manager scope=all expected 5 calls, got %d", out.Total.Calls)
	}

	// 6. 超管 scope=all:全平台含无头任务的配价模型 4 行(A 两行 + B 一行 + 无头一行)
	if out := fetch(tokSuper, "?scope=all&model="+pricedModel); out.Total.Calls != 4 || out.Total.PromptTokens != 1_000_600 {
		t.Fatalf("super scope=all expected 4 priced-model calls, got %d (%+v)", out.Total.Calls, out.Total)
	}

	// 7. 未登录拦截:中间件直接回 401 + {"error":"unauthorized"}(非信封格式)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/usage/stats", nil)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestSessionTokenUsageAccess 验证会话级用量明细的读写隔离与逐行计价
func TestSessionTokenUsageAccess(t *testing.T) {
	setupTestServer()
	ctx := db.WithContext(context.Background())
	q := db.Ctx(ctx)

	nano := time.Now().UnixNano()
	sid := fmt.Sprintf("sess_usage_%d", nano)
	if err := dao.CreateSession(ctx, &model.Session{
		SessionID: sid, CompanyID: 1, UserID: 101, Title: "usage-test", Model: "MiniMax-Text-01",
	}); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := dao.CreateTokenUsage(ctx, &model.TokenUsage{
		SessionID: sid, CompanyID: 1, UserID: 101, Model: "MiniMax-Text-01", ProviderKey: "minimax",
		Purpose: "turn", RoundIndex: 1, PromptTokens: 300, CompletionTokens: 45, CachedTokens: 80, ReasoningTokens: 20,
	}); err != nil {
		t.Fatalf("CreateTokenUsage failed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = q.TokenUsage.WithContext(ctx).Where(q.TokenUsage.SessionID.Eq(sid)).Delete()
		_ = dao.DeleteSession(ctx, sid)
	})

	tokenOf := func(uid int64, utype constants.UserType) string {
		tok, err := jwt.GenAccessToken(jwt.TokenClaims{
			UserID: uid, CompanyID: 1, Name: "t", Username: fmt.Sprintf("u%d", uid), UserType: utype,
		})
		if err != nil {
			t.Fatalf("gen token failed: %v", err)
		}
		return tok
	}

	fetch := func(token string) (*httptest.ResponseRecorder, envelope) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/sessions/"+sid+"/usage", nil)
		req.Header.Set("x-token", token)
		testEngine.ServeHTTP(w, req)
		return w, decodeEnvelope(t, w)
	}

	// 本人可见,逐行计价与 token 明细完整
	_, env := fetch(tokenOf(101, constants.User))
	if env.Code != 0 {
		t.Fatalf("owner should read session usage: %s", env.Msg)
	}
	var items []api.TokenUsageItemOut
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("decode items failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.RoundIndex != 1 || it.Purpose != "turn" || it.ProviderKey != "minimax" ||
		it.PromptTokens != 300 || it.CompletionTokens != 45 || it.CachedTokens != 80 || it.TotalTokens != 345 {
		t.Fatalf("mismatched item: %+v", it)
	}

	// 他人会话统一"会话不存在",防存在性探测
	if _, env := fetch(tokenOf(102, constants.User)); env.Code == 0 || !strings.Contains(env.Msg, "会话不存在") {
		t.Fatalf("other user should get 会话不存在, got code=%d msg=%s", env.Code, env.Msg)
	}

	// 本企企管审计可见
	if _, env := fetch(tokenOf(999, constants.Manager)); env.Code != 0 {
		t.Fatalf("company manager should read session usage: %s", env.Msg)
	}
}
