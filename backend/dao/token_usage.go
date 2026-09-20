package dao

import (
	"context"
	"sort"
	"strings"
	"time"

	"backend/constants"
	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"
)

// TokenUsageBucket 一个聚合维度的四路 Token 计数
// (cached ⊂ prompt_tokens、reasoning ⊂ completion_tokens,子集语义防双计)
type TokenUsageBucket struct {
	Calls            int64 `json:"calls"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// add 累加一次 LLM 调用的用量
func (b *TokenUsageBucket) add(r *model.TokenUsage) {
	b.Calls++
	b.PromptTokens += r.PromptTokens
	b.CompletionTokens += r.CompletionTokens
	b.CachedTokens += r.CachedTokens
	b.ReasoningTokens += r.ReasoningTokens
	b.TotalTokens += r.PromptTokens + r.CompletionTokens
}

// TokenUsageDayCount 单日聚合(窗口内无数据的天补零占位)
type TokenUsageDayCount struct {
	Date string `json:"date"`
	TokenUsageBucket
}

// TokenUsageModelCount 按模型聚合(携带 provider_key 便于分账视角)
type TokenUsageModelCount struct {
	Model       string `json:"model"`
	ProviderKey string `json:"provider_key"`
	TokenUsageBucket
}

// TokenUsagePurposeCount 按用途聚合(对话推理/压缩/记忆抽取的成本结构拆分)
type TokenUsagePurposeCount struct {
	Purpose string `json:"purpose"`
	TokenUsageBucket
}

// TokenUsageDayModelCount (日期,模型) 粒度明细:自身不出接口,专供 API 层
// 精确换算每日成本(跨模型混合的日桶无法直接套单价)
type TokenUsageDayModelCount struct {
	Date  string `json:"-"`
	Model string `json:"-"`
	TokenUsageBucket
}

// TokenUsageStats 用量聚合结果:纯 token 计数,金额由 API 层按单价表现算(账与价分离)
type TokenUsageStats struct {
	Days         int                       `json:"days"`
	Total        TokenUsageBucket          `json:"total"`
	Daily        []TokenUsageDayCount      `json:"daily"`
	ByModel      []TokenUsageModelCount    `json:"by_model"`
	ByPurpose    []TokenUsagePurposeCount  `json:"by_purpose"`
	DailyByModel []TokenUsageDayModelCount `json:"-"`
}

// TokenUsageStatsFilter 统计过滤条件(CompanyID/UserID > 0 时生效,三级 RBAC 分流由调用方决定)
type TokenUsageStatsFilter struct {
	CompanyID int64
	UserID    int64
	Model     string
	Purpose   string
	Days      int
}

// CreateTokenUsage 插入一条 LLM 调用用量记录。
// 写入唯一入口为 agent 记账旁路 recordTokenUsage,业务代码不得绕过直接写账
func CreateTokenUsage(ctx context.Context, row *model.TokenUsage) error {
	if row == nil || strings.TrimSpace(row.SessionID) == "" {
		return errors.NewMsg("用量记录缺少会话归属")
	}
	if strings.TrimSpace(row.Purpose) == "" {
		row.Purpose = string(constants.UsagePurposeTurn)
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now()
	}
	if err := db.Ctx(ctx).TokenUsage.WithContext(ctx).Create(row); err != nil {
		return errors.Join(err, errors.New("记录 Token 用量失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// ListTokenUsagesBySession 按会话查询用量明细(最新在前,limit 钳制 1~500,默认 200)
func ListTokenUsagesBySession(ctx context.Context, sessionID string, limit int) ([]*model.TokenUsage, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.NewMsg("会话不能为空")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	tu := db.Ctx(ctx).TokenUsage
	rows, err := tu.WithContext(ctx).
		Where(tu.SessionID.Eq(sessionID)).
		Order(tu.ID.Desc()).
		Limit(limit).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询会话用量明细失败"), errors.NewMsg("系统异常"))
	}
	return rows, nil
}

// GetTokenUsageStats 聚合计算时间窗内的 Token 用量指标(仿 GetSkillRunStats:
// 拉全量行后在内存聚合,SQL 不做聚合)
func GetTokenUsageStats(ctx context.Context, f TokenUsageStatsFilter) (*TokenUsageStats, error) {
	if f.Days <= 0 {
		f.Days = 7
	}
	if f.Days > 90 {
		f.Days = 90
	}

	tu := db.Ctx(ctx).TokenUsage
	startTime := time.Now().Add(-time.Duration(f.Days) * 24 * time.Hour)

	do := tu.WithContext(ctx).Where(tu.CreatedAt.Gte(startTime))
	if f.CompanyID > 0 {
		do = do.Where(tu.CompanyID.Eq(f.CompanyID))
	}
	if f.UserID > 0 {
		do = do.Where(tu.UserID.Eq(f.UserID))
	}
	if f.Model = strings.TrimSpace(f.Model); f.Model != "" {
		do = do.Where(tu.Model.Eq(f.Model))
	}
	if f.Purpose = strings.TrimSpace(f.Purpose); f.Purpose != "" {
		do = do.Where(tu.Purpose.Eq(f.Purpose))
	}

	rows, err := do.Order(tu.CreatedAt.Asc()).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("获取用量统计记录失败"), errors.NewMsg("系统异常"))
	}

	stats := &TokenUsageStats{
		Days:  f.Days,
		Daily: make([]TokenUsageDayCount, 0, f.Days),
	}

	// 初始化每日分布字典(窗口内无数据的天补零占位,前端可直接绘曲线)
	dayMap := make(map[string]*TokenUsageDayCount, f.Days)
	now := time.Now()
	for i := f.Days - 1; i >= 0; i-- {
		dStr := now.Add(-time.Duration(i) * 24 * time.Hour).Format("2006-01-02")
		item := &TokenUsageDayCount{Date: dStr}
		dayMap[dStr] = item
		stats.Daily = append(stats.Daily, *item)
	}

	modelMap := make(map[string]*TokenUsageModelCount)
	purposeMap := make(map[string]*TokenUsagePurposeCount)
	dayModelMap := make(map[string]*TokenUsageDayModelCount) // key: date + "\x00" + model

	for _, r := range rows {
		stats.Total.add(r)

		dStr := r.CreatedAt.Format("2006-01-02")
		if dItem, ok := dayMap[dStr]; ok {
			dItem.add(r)
		}

		mi, ok := modelMap[r.Model]
		if !ok {
			mi = &TokenUsageModelCount{Model: r.Model}
			modelMap[r.Model] = mi
		}
		mi.add(r)
		if r.ProviderKey != "" {
			mi.ProviderKey = r.ProviderKey // 行按时间升序,最后一个非空值即最新归属
		}

		dmKey := dStr + "\x00" + r.Model
		dm, ok := dayModelMap[dmKey]
		if !ok {
			dm = &TokenUsageDayModelCount{Date: dStr, Model: r.Model}
			dayModelMap[dmKey] = dm
		}
		dm.add(r)

		pi, ok := purposeMap[r.Purpose]
		if !ok {
			pi = &TokenUsagePurposeCount{Purpose: r.Purpose}
			purposeMap[r.Purpose] = pi
		}
		pi.add(r)
	}

	// 重新同步回 daily 数组
	for idx, d := range stats.Daily {
		if updated, ok := dayMap[d.Date]; ok {
			stats.Daily[idx] = *updated
		}
	}

	// 模型/用途维度按消耗规模降序,主力成本项排前;同规模按名称序保证稳定输出
	for _, m := range modelMap {
		stats.ByModel = append(stats.ByModel, *m)
	}
	sort.Slice(stats.ByModel, func(i, j int) bool {
		if stats.ByModel[i].TotalTokens != stats.ByModel[j].TotalTokens {
			return stats.ByModel[i].TotalTokens > stats.ByModel[j].TotalTokens
		}
		return stats.ByModel[i].Model < stats.ByModel[j].Model
	})
	for _, p := range purposeMap {
		stats.ByPurpose = append(stats.ByPurpose, *p)
	}
	sort.Slice(stats.ByPurpose, func(i, j int) bool {
		if stats.ByPurpose[i].Calls != stats.ByPurpose[j].Calls {
			return stats.ByPurpose[i].Calls > stats.ByPurpose[j].Calls
		}
		return stats.ByPurpose[i].Purpose < stats.ByPurpose[j].Purpose
	})

	for _, dm := range dayModelMap {
		stats.DailyByModel = append(stats.DailyByModel, *dm)
	}

	return stats, nil
}
