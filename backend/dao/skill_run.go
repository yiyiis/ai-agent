package dao

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"

	"gorm.io/gorm"
)

// SkillRunDayCount 单日执行量与失败量统计
type SkillRunDayCount struct {
	Day    string `json:"day"`
	Total  int    `json:"total"`
	Failed int    `json:"failed"`
}

// SkillRunStats 聚合指标
type SkillRunStats struct {
	Days          int                `json:"days"`
	Total         int                `json:"total"`
	Succeeded     int                `json:"succeeded"`
	Failed        int                `json:"failed"`
	Running       int                `json:"running"`
	SuccessRate   *float64           `json:"success_rate"`
	P50Ms         *int64             `json:"p50_ms"`
	P95Ms         *int64             `json:"p95_ms"`
	LastFailureAt *string            `json:"last_failure_at"`
	Daily         []SkillRunDayCount `json:"daily"`
}

// CreateSkillRun 创建任务执行记录
func CreateSkillRun(ctx context.Context, run *model.SkillRun) error {
	if run == nil || strings.TrimSpace(run.RunID) == "" {
		return errors.NewMsg("任务 ID (run_id) 不能为空")
	}
	if strings.TrimSpace(run.Status) == "" {
		run.Status = "pending"
	}

	q := db.Ctx(ctx)
	if err := q.SkillRun.WithContext(ctx).Create(run); err != nil {
		return errors.Join(err, errors.New("创建任务记录失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// GetSkillRunByRunID 根据 run_id 查询单次任务
func GetSkillRunByRunID(ctx context.Context, runID string) (*model.SkillRun, error) {
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return nil, errors.NewMsg("run_id 不能为空")
	}

	q := db.Ctx(ctx)
	sr := q.SkillRun

	run, err := sr.WithContext(ctx).Where(sr.RunID.Eq(rid)).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewMsg("任务不存在")
		}
		return nil, errors.Join(err, errors.New("查询任务失败"), errors.NewMsg("系统异常"))
	}
	return run, nil
}

// GetSkillRunByRunIDAndCompany 带有租户企业隔离的 run_id 查询
func GetSkillRunByRunIDAndCompany(ctx context.Context, runID string, companyID int64) (*model.SkillRun, error) {
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return nil, errors.NewMsg("run_id 不能为空")
	}

	q := db.Ctx(ctx)
	sr := q.SkillRun

	do := sr.WithContext(ctx).Where(sr.RunID.Eq(rid))
	if companyID > 0 {
		do = do.Where(sr.CompanyID.Eq(companyID))
	}

	run, err := do.First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewMsg("任务不存在或无权访问")
		}
		return nil, errors.Join(err, errors.New("查询任务失败"), errors.NewMsg("系统异常"))
	}
	return run, nil
}

// UpdateSkillRun 原子更新任务状态与字段
func UpdateSkillRun(ctx context.Context, runID string, updates map[string]any) error {
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return errors.NewMsg("run_id 不能为空")
	}
	if len(updates) == 0 {
		return nil
	}

	q := db.Ctx(ctx)
	sr := q.SkillRun

	info, err := sr.WithContext(ctx).Where(sr.RunID.Eq(rid)).Updates(updates)
	if err != nil {
		return errors.Join(err, errors.New("更新任务状态失败"), errors.NewMsg("系统异常"))
	}
	if info.RowsAffected == 0 {
		return errors.NewMsg("任务不存在或未产生变更")
	}
	return nil
}

// ListSkillRuns 供 Web 控制台查询指定技能的历史调用记录
func ListSkillRuns(ctx context.Context, skillID int64, days int, limit int, status string) ([]*model.SkillRun, error) {
	q := db.Ctx(ctx)
	sr := q.SkillRun

	do := sr.WithContext(ctx).Where(sr.SkillID.Eq(skillID))

	if days > 0 {
		startTime := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		do = do.Where(sr.CreatedAt.Gte(startTime))
	}
	if status != "" {
		do = do.Where(sr.Status.Eq(status))
	}

	if limit <= 0 || limit > 500 {
		limit = 100
	}

	runs, err := do.Order(sr.CreatedAt.Desc()).Limit(limit).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询技能调用记录失败"), errors.NewMsg("系统异常"))
	}
	return runs, nil
}

// GetSkillRunStats 聚合计算技能在最近指定天数内的统计指标
func GetSkillRunStats(ctx context.Context, skillID int64, days int) (*SkillRunStats, error) {
	if days <= 0 {
		days = 7
	}

	q := db.Ctx(ctx)
	sr := q.SkillRun

	startTime := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	runs, err := sr.WithContext(ctx).
		Where(sr.SkillID.Eq(skillID), sr.CreatedAt.Gte(startTime)).
		Order(sr.CreatedAt.Asc()).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("获取统计记录失败"), errors.NewMsg("系统异常"))
	}

	stats := &SkillRunStats{
		Days:  days,
		Daily: make([]SkillRunDayCount, 0, days),
	}

	// 初始化每日分布字典
	dayMap := make(map[string]*SkillRunDayCount, days)
	now := time.Now()
	for i := days - 1; i >= 0; i-- {
		dStr := now.Add(-time.Duration(i) * 24 * time.Hour).Format("2006-01-02")
		item := &SkillRunDayCount{Day: dStr}
		dayMap[dStr] = item
		stats.Daily = append(stats.Daily, *item)
	}

	var durations []int64
	var lastFailTime *time.Time

	for _, r := range runs {
		stats.Total++
		dStr := r.CreatedAt.Format("2006-01-02")
		if dItem, ok := dayMap[dStr]; ok {
			dItem.Total++
		}

		switch r.Status {
		case "success":
			stats.Succeeded++
			if r.DurationMs != nil && *r.DurationMs > 0 {
				durations = append(durations, *r.DurationMs)
			}
		case "failed", "timeout":
			stats.Failed++
			if dItem, ok := dayMap[dStr]; ok {
				dItem.Failed++
			}
			t := r.CreatedAt
			if r.FinishedAt != nil {
				t = *r.FinishedAt
			}
			if lastFailTime == nil || t.After(*lastFailTime) {
				lastFailTime = &t
			}
		case "pending", "running":
			stats.Running++
		}
	}

	// 重新同步回 daily 数组
	for idx, d := range stats.Daily {
		if updated, ok := dayMap[d.Day]; ok {
			stats.Daily[idx] = *updated
		}
	}

	// 计算成功率
	finished := stats.Succeeded + stats.Failed
	if finished > 0 {
		rate := math.Round((float64(stats.Succeeded)/float64(finished))*1000) / 1000
		stats.SuccessRate = &rate
	}

	// 计算 P50 / P95 耗时
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		p50Idx := int(math.Floor(float64(len(durations)) * 0.50))
		p95Idx := int(math.Floor(float64(len(durations)) * 0.95))
		if p50Idx >= len(durations) {
			p50Idx = len(durations) - 1
		}
		if p95Idx >= len(durations) {
			p95Idx = len(durations) - 1
		}
		p50 := durations[p50Idx]
		p95 := durations[p95Idx]
		stats.P50Ms = &p50
		stats.P95Ms = &p95
	}

	if lastFailTime != nil {
		iso := lastFailTime.UTC().Format(time.RFC3339)
		stats.LastFailureAt = &iso
	}

	return stats, nil
}

// ResetSkillRunForRetry 重置任务为重试状态（追加当前现场至 attempts 数组，自增 attempt，复用 run_id）
func ResetSkillRunForRetry(ctx context.Context, runID string, attemptSnapshot string, nextRetryAt *time.Time) error {
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return errors.NewMsg("run_id 不能为空")
	}

	run, err := GetSkillRunByRunID(ctx, rid)
	if err != nil {
		return err
	}

	// 归并历史 attempts JSON 数组
	var attemptsList []any
	if run.Attempts != nil && strings.TrimSpace(*run.Attempts) != "" {
		_ = json.Unmarshal([]byte(*run.Attempts), &attemptsList)
	}
	if attemptSnapshot != "" {
		var snapshotObj any
		if json.Unmarshal([]byte(attemptSnapshot), &snapshotObj) == nil {
			attemptsList = append(attemptsList, snapshotObj)
		}
	}
	newAttemptsBytes, _ := json.Marshal(attemptsList)
	newAttemptsStr := string(newAttemptsBytes)

	updates := map[string]any{
		"status":        "pending",
		"attempt":       run.Attempt + 1,
		"attempts":      newAttemptsStr,
		"error":         nil,
		"stderr_tail":   nil,
		"stdout_tail":   nil,
		"duration_ms":   nil,
		"failure_kind":  nil,
		"started_at":    nil,
		"finished_at":   nil,
		"next_retry_at": nextRetryAt,
	}

	return UpdateSkillRun(ctx, rid, updates)
}

// FindPendingRunsToDispatch 获取到达重试时间或刚入库处于 pending 状态的任务（供后台 Poller 补单调度）
func FindPendingRunsToDispatch(ctx context.Context, limit int) ([]*model.SkillRun, error) {
	if limit <= 0 {
		limit = 20
	}

	q := db.Ctx(ctx)
	sr := q.SkillRun

	now := time.Now()
	// pending 且 (next_retry_at IS NULL OR next_retry_at <= now)
	runs, err := sr.WithContext(ctx).
		Where(sr.Status.Eq("pending")).
		Where(sr.WithContext(ctx).Where(sr.NextRetryAt.IsNull()).Or(sr.NextRetryAt.Lte(now))).
		Order(sr.CreatedAt.Asc()).
		Limit(limit).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询待调度任务失败"), errors.NewMsg("系统异常"))
	}
	return runs, nil
}
