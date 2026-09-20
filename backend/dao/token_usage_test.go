package dao_test

import (
	"fmt"
	"testing"
	"time"

	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
)

func cleanupTokenUsages(t *testing.T, sessionIDs ...string) {
	t.Helper()
	ctx := setupTestDB(t)
	q := db.Ctx(ctx)
	for _, sid := range sessionIDs {
		_, _ = q.TokenUsage.WithContext(ctx).Where(q.TokenUsage.SessionID.Eq(sid)).Delete()
	}
}

func TestTokenUsageCreateAndAttribution(t *testing.T) {
	ctx := setupTestDB(t)

	sid := fmt.Sprintf("sess_tu_%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanupTokenUsages(t, sid) })

	row := &model.TokenUsage{
		SessionID:        sid,
		CompanyID:        42,
		UserID:           4242,
		Model:            "MiniMax-Text-01",
		ProviderKey:      "minimax",
		RoundIndex:       1,
		PromptTokens:     300,
		CompletionTokens: 45,
		CachedTokens:     80,
		ReasoningTokens:  20,
	}
	if err := dao.CreateTokenUsage(ctx, row); err != nil {
		t.Fatalf("CreateTokenUsage failed: %v", err)
	}

	// purpose 缺省自动补 turn
	if row.Purpose != string(constants.UsagePurposeTurn) {
		t.Fatalf("expected default purpose turn, got %q", row.Purpose)
	}

	// 归属冗余字段逐行自解释
	rows, err := dao.ListTokenUsagesBySession(ctx, sid, 10)
	if err != nil {
		t.Fatalf("ListTokenUsagesBySession failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.CompanyID != 42 || got.UserID != 4242 || got.Model != "MiniMax-Text-01" ||
		got.ProviderKey != "minimax" || got.RoundIndex != 1 ||
		got.PromptTokens != 300 || got.CompletionTokens != 45 ||
		got.CachedTokens != 80 || got.ReasoningTokens != 20 {
		t.Fatalf("mismatched usage row: %+v", got)
	}

	// 缺会话归属的账不许入库
	if err := dao.CreateTokenUsage(ctx, &model.TokenUsage{Model: "GLM-5.3"}); err == nil {
		t.Fatal("expected error for missing session attribution")
	}
}

func TestTokenUsageListOrderAndClamp(t *testing.T) {
	ctx := setupTestDB(t)

	sid := fmt.Sprintf("sess_tul_%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanupTokenUsages(t, sid) })

	for i := 0; i < 3; i++ {
		if err := dao.CreateTokenUsage(ctx, &model.TokenUsage{
			SessionID:    sid,
			Model:        "MiniMax-Text-01",
			RoundIndex:   int64(i),
			PromptTokens: 100,
		}); err != nil {
			t.Fatalf("CreateTokenUsage #%d failed: %v", i, err)
		}
	}

	// 最新在前(id 倒序)
	rows, err := dao.ListTokenUsagesBySession(ctx, sid, 2)
	if err != nil {
		t.Fatalf("ListTokenUsagesBySession failed: %v", err)
	}
	if len(rows) != 2 || rows[0].RoundIndex != 2 || rows[1].RoundIndex != 1 {
		t.Fatalf("expected newest-first 2 rows [2,1], got %+v", rows)
	}

	// limit<=0 走默认值,不丢数据
	all, err := dao.ListTokenUsagesBySession(ctx, sid, 0)
	if err != nil {
		t.Fatalf("ListTokenUsagesBySession failed: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows with default limit, got %d", len(all))
	}
}

func TestTokenUsageStatsAggregation(t *testing.T) {
	ctx := setupTestDB(t)

	sid := fmt.Sprintf("sess_tus_%d", time.Now().UnixNano())
	uid := time.Now().UnixNano() % 900000000 // 唯一用户归属,隔离库内其他数据
	cid := uid + 1
	t.Cleanup(func() { cleanupTokenUsages(t, sid) })

	insert := func(row *model.TokenUsage) {
		row.SessionID = sid
		row.CompanyID = cid
		row.UserID = uid
		if err := dao.CreateTokenUsage(ctx, row); err != nil {
			t.Fatalf("CreateTokenUsage failed: %v", err)
		}
	}

	today := time.Now()
	insert(&model.TokenUsage{Model: "MiniMax-Text-01", ProviderKey: "minimax", Purpose: string(constants.UsagePurposeTurn),
		PromptTokens: 100, CompletionTokens: 20})
	insert(&model.TokenUsage{Model: "MiniMax-Text-01", ProviderKey: "minimax", Purpose: string(constants.UsagePurposeTurn),
		PromptTokens: 300, CompletionTokens: 45, CachedTokens: 80, ReasoningTokens: 20})
	insert(&model.TokenUsage{Model: "GLM-5.3", ProviderKey: "glm", Purpose: string(constants.UsagePurposeCompact),
		PromptTokens: 500, CreatedAt: today.AddDate(0, 0, -1)})

	stats, err := dao.GetTokenUsageStats(ctx, dao.TokenUsageStatsFilter{UserID: uid, Days: 7})
	if err != nil {
		t.Fatalf("GetTokenUsageStats failed: %v", err)
	}

	// 总计:cached ⊂ prompt、reasoning ⊂ completion,total = prompt + completion 不双计
	if stats.Total.Calls != 3 || stats.Total.PromptTokens != 900 || stats.Total.CompletionTokens != 65 ||
		stats.Total.CachedTokens != 80 || stats.Total.ReasoningTokens != 20 || stats.Total.TotalTokens != 965 {
		t.Fatalf("mismatched total: %+v", stats.Total)
	}

	// by_model:按消耗规模降序,GLM(500) 应排在 MiniMax(465) 前
	if len(stats.ByModel) != 2 {
		t.Fatalf("expected 2 model buckets, got %d", len(stats.ByModel))
	}
	if stats.ByModel[0].Model != "GLM-5.3" || stats.ByModel[0].ProviderKey != "glm" ||
		stats.ByModel[0].Calls != 1 || stats.ByModel[0].TotalTokens != 500 {
		t.Fatalf("mismatched first model bucket: %+v", stats.ByModel[0])
	}
	if stats.ByModel[1].Model != "MiniMax-Text-01" || stats.ByModel[1].ProviderKey != "minimax" ||
		stats.ByModel[1].Calls != 2 || stats.ByModel[1].TotalTokens != 465 ||
		stats.ByModel[1].CachedTokens != 80 || stats.ByModel[1].ReasoningTokens != 20 {
		t.Fatalf("mismatched second model bucket: %+v", stats.ByModel[1])
	}

	// by_purpose:turn(2 次调用)在前,compact(1 次)在后
	if len(stats.ByPurpose) != 2 ||
		stats.ByPurpose[0].Purpose != string(constants.UsagePurposeTurn) || stats.ByPurpose[0].Calls != 2 ||
		stats.ByPurpose[1].Purpose != string(constants.UsagePurposeCompact) || stats.ByPurpose[1].Calls != 1 {
		t.Fatalf("mismatched purpose buckets: %+v", stats.ByPurpose)
	}

	// daily:今天 2 行、昨天 1 行,其余天补零
	todayStr := today.Format("2006-01-02")
	yesterdayStr := today.AddDate(0, 0, -1).Format("2006-01-02")
	dayCalls := map[string]int64{}
	for _, d := range stats.Daily {
		dayCalls[d.Date] = d.Calls
	}
	if dayCalls[todayStr] != 2 || dayCalls[yesterdayStr] != 1 {
		t.Fatalf("mismatched daily distribution: %+v", stats.Daily)
	}
	if len(stats.Daily) != 7 {
		t.Fatalf("expected 7 daily buckets, got %d", len(stats.Daily))
	}

	// 维度过滤:purpose / model / company 各自生效
	byPurpose, _ := dao.GetTokenUsageStats(ctx, dao.TokenUsageStatsFilter{UserID: uid, Purpose: string(constants.UsagePurposeCompact)})
	if byPurpose.Total.Calls != 1 || byPurpose.Total.PromptTokens != 500 {
		t.Fatalf("purpose filter mismatched: %+v", byPurpose.Total)
	}
	byModel, _ := dao.GetTokenUsageStats(ctx, dao.TokenUsageStatsFilter{UserID: uid, Model: "MiniMax-Text-01"})
	if byModel.Total.Calls != 2 || byModel.Total.PromptTokens != 400 {
		t.Fatalf("model filter mismatched: %+v", byModel.Total)
	}
	byCompany, _ := dao.GetTokenUsageStats(ctx, dao.TokenUsageStatsFilter{CompanyID: cid})
	if byCompany.Total.Calls != 3 {
		t.Fatalf("company filter mismatched: %+v", byCompany.Total)
	}
	byOther, _ := dao.GetTokenUsageStats(ctx, dao.TokenUsageStatsFilter{UserID: uid + 99999})
	if byOther.Total.Calls != 0 {
		t.Fatalf("expected 0 rows for other user, got %d", byOther.Total.Calls)
	}

	// days 钳制:非法值兜底 7,超限收敛 90
	if s, _ := dao.GetTokenUsageStats(ctx, dao.TokenUsageStatsFilter{UserID: uid}); s.Days != 7 {
		t.Fatalf("expected default days 7, got %d", s.Days)
	}
	if s, _ := dao.GetTokenUsageStats(ctx, dao.TokenUsageStatsFilter{UserID: uid, Days: 500}); s.Days != 90 {
		t.Fatalf("expected clamped days 90, got %d", s.Days)
	}
}
