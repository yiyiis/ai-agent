package api

import (
	"context"
	"math"
	"time"

	"backend/constants"
	"backend/dao"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"backend/pkg/provider"
)

// ---- Token 用量统计查询（阶段七·模块一）----
// DAO 层只出 token 计数；金额一律在本层按 LLM.Billing 单价表现算。
// cost_estimate 为查询时估算值（非账单），调价只影响之后的查询，历史计数不受影响。

// TokenUsageBucketOut 单聚合单元的四路 Token 计数与估算成本
type TokenUsageBucketOut struct {
	Calls            int64   `json:"calls"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostEstimate     float64 `json:"cost_estimate"`
}

type TokenUsageDayOut struct {
	Date string `json:"date"`
	TokenUsageBucketOut
}

type TokenUsageModelOut struct {
	Model       string `json:"model"`
	ProviderKey string `json:"provider_key"`
	TokenUsageBucketOut
}

// TokenUsagePurposeOut 按用途聚合:purpose 与单价正交,金额看模型维度即可
type TokenUsagePurposeOut struct {
	Purpose          string `json:"purpose"`
	Calls            int64  `json:"calls"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CachedTokens     int64  `json:"cached_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

type TokenUsageStatsOut struct {
	Days      int                    `json:"days"`
	Total     TokenUsageBucketOut    `json:"total"`
	Daily     []TokenUsageDayOut     `json:"daily"`
	ByModel   []TokenUsageModelOut   `json:"by_model"`
	ByPurpose []TokenUsagePurposeOut `json:"by_purpose"`
}

type GetTokenUsageStatsReq struct {
	Days    int    `form:"days"`
	Model   string `form:"model"`
	Purpose string `form:"purpose"`
	Scope   string `form:"scope"`
}

// GetTokenUsageStats 处理用量统计查询 (GET /api/usage/stats)。
// 三级分流与 ListSessions 同款:普通员工始终只看本人;企管 scope=all 看本企业;
// 超管 scope=all 看全平台。无头任务(user_id=0)的账只在企业及以上视角可见。
func GetTokenUsageStats(ctx context.Context, req *GetTokenUsageStatsReq) (*TokenUsageStatsOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	filter := dao.TokenUsageStatsFilter{
		UserID:  identity.UserID,
		Model:   req.Model,
		Purpose: req.Purpose,
		Days:    req.Days,
	}
	if req != nil && req.Scope == "all" {
		if identity.UserType == constants.SuperManager {
			filter.UserID = 0
		} else if identity.UserType == constants.Manager {
			filter.UserID = 0
			filter.CompanyID = identity.CompanyID
		}
	}

	stats, err := dao.GetTokenUsageStats(ctx, filter)
	if err != nil {
		return nil, err
	}
	return buildTokenUsageStatsOut(stats), nil
}

// bucketOut 按模型单价换算估算成本(未配价模型金额为 0,token 照常展示)
func bucketOut(b dao.TokenUsageBucket, model string) TokenUsageBucketOut {
	return TokenUsageBucketOut{
		Calls:            b.Calls,
		PromptTokens:     b.PromptTokens,
		CompletionTokens: b.CompletionTokens,
		CachedTokens:     b.CachedTokens,
		ReasoningTokens:  b.ReasoningTokens,
		TotalTokens:      b.TotalTokens,
		CostEstimate:     provider.Cost(model, b.PromptTokens, b.CachedTokens, b.CompletionTokens),
	}
}

func buildTokenUsageStatsOut(stats *dao.TokenUsageStats) *TokenUsageStatsOut {
	out := &TokenUsageStatsOut{
		Days:      stats.Days,
		Daily:     make([]TokenUsageDayOut, 0, len(stats.Daily)),
		ByModel:   make([]TokenUsageModelOut, 0, len(stats.ByModel)),
		ByPurpose: make([]TokenUsagePurposeOut, 0, len(stats.ByPurpose)),
	}

	// 模型维度:逐模型精确换算
	modelCost := 0.0
	for _, m := range stats.ByModel {
		b := bucketOut(m.TokenUsageBucket, m.Model)
		modelCost += b.CostEstimate
		out.ByModel = append(out.ByModel, TokenUsageModelOut{
			Model:               m.Model,
			ProviderKey:         m.ProviderKey,
			TokenUsageBucketOut: b,
		})
	}

	// 总计金额 = 各模型金额之和(跨模型桶不能直接套单一单价)
	out.Total = TokenUsageBucketOut{
		Calls:            stats.Total.Calls,
		PromptTokens:     stats.Total.PromptTokens,
		CompletionTokens: stats.Total.CompletionTokens,
		CachedTokens:     stats.Total.CachedTokens,
		ReasoningTokens:  stats.Total.ReasoningTokens,
		TotalTokens:      stats.Total.TotalTokens,
		CostEstimate:     roundCost(modelCost),
	}

	// 每日金额 = 当日各模型明细金额之和(DailyByModel 专供精确换算)
	dayCost := make(map[string]float64, len(stats.DailyByModel))
	for _, dm := range stats.DailyByModel {
		dayCost[dm.Date] += provider.Cost(dm.Model, dm.PromptTokens, dm.CachedTokens, dm.CompletionTokens)
	}
	for _, d := range stats.Daily {
		day := TokenUsageDayOut{Date: d.Date}
		day.Calls = d.Calls
		day.PromptTokens = d.PromptTokens
		day.CompletionTokens = d.CompletionTokens
		day.CachedTokens = d.CachedTokens
		day.ReasoningTokens = d.ReasoningTokens
		day.TotalTokens = d.TotalTokens
		day.CostEstimate = roundCost(dayCost[d.Date])
		out.Daily = append(out.Daily, day)
	}

	for _, p := range stats.ByPurpose {
		out.ByPurpose = append(out.ByPurpose, TokenUsagePurposeOut{
			Purpose:          p.Purpose,
			Calls:            p.Calls,
			PromptTokens:     p.PromptTokens,
			CompletionTokens: p.CompletionTokens,
			CachedTokens:     p.CachedTokens,
			ReasoningTokens:  p.ReasoningTokens,
			TotalTokens:      p.TotalTokens,
		})
	}

	return out
}

func roundCost(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

type GetSessionTokenUsageReq struct {
	ID    string `uri:"id"`
	Limit int    `form:"limit"`
}

// TokenUsageItemOut 单次 LLM 调用的用量明细(一行一账)
type TokenUsageItemOut struct {
	ID               int64   `json:"id"`
	SessionID        string  `json:"session_id"`
	Model            string  `json:"model"`
	ProviderKey      string  `json:"provider_key"`
	RoundIndex       int64   `json:"round_index"`
	Purpose          string  `json:"purpose"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostEstimate     float64 `json:"cost_estimate"`
	CreatedAt        string  `json:"created_at"`
}

// GetSessionTokenUsage 查询指定会话的用量明细 (GET /api/sessions/:id/usage)。
// 复用会话级读权限:本人/本企企管/超管可见,不存在与他人会话统一报"会话不存在"
func GetSessionTokenUsage(ctx context.Context, req *GetSessionTokenUsageReq) (*[]TokenUsageItemOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	if _, err := loadOwnedSession(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	rows, err := dao.ListTokenUsagesBySession(ctx, req.ID, req.Limit)
	if err != nil {
		return nil, err
	}

	out := make([]TokenUsageItemOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, TokenUsageItemOut{
			ID:               r.ID,
			SessionID:        r.SessionID,
			Model:            r.Model,
			ProviderKey:      r.ProviderKey,
			RoundIndex:       r.RoundIndex,
			Purpose:          r.Purpose,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			CachedTokens:     r.CachedTokens,
			ReasoningTokens:  r.ReasoningTokens,
			TotalTokens:      r.PromptTokens + r.CompletionTokens,
			CostEstimate:     provider.Cost(r.Model, r.PromptTokens, r.CachedTokens, r.CompletionTokens),
			CreatedAt:        r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return &out, nil
}
