package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"backend/dao"
	"backend/pkg/dispatcher"
	"backend/pkg/errors"
	"backend/pkg/jwt"
)

// SkillRunSummaryOut 列表返回摘要
type SkillRunSummaryOut struct {
	ID         string  `json:"id"`
	Status     string  `json:"status"`
	DurationMs *int64  `json:"duration_ms"`
	Error      *string `json:"error"`
	CreatedAt  string  `json:"created_at"`
	FinishedAt *string `json:"finished_at"`
	UserID     int64   `json:"user_id"`
	APIKeyID   *int64  `json:"api_key_id"`
	Attempt    int64   `json:"attempt"`
}

// ListSkillRunsReq 历史列表入参
type ListSkillRunsReq struct {
	ID     int64  `uri:"id"`
	Days   int    `form:"days"`
	Limit  int    `form:"limit"`
	Status string `form:"status"`
}

// ListSkillRuns 处理技能历史调用查询 (GET /api/skills/:id/runs)
func ListSkillRuns(ctx context.Context, req *ListSkillRunsReq) ([]SkillRunSummaryOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := loadOwnedSkillRow(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	days := req.Days
	if days <= 0 {
		days = 7
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}

	runs, err := dao.ListSkillRuns(ctx, req.ID, days, limit, req.Status)
	if err != nil {
		return nil, err
	}

	out := make([]SkillRunSummaryOut, 0, len(runs))
	for _, r := range runs {
		var finishedAtStr *string
		if r.FinishedAt != nil {
			s := r.FinishedAt.UTC().Format(time.RFC3339)
			finishedAtStr = &s
		}

		out = append(out, SkillRunSummaryOut{
			ID:         r.RunID,
			Status:     r.Status,
			DurationMs: r.DurationMs,
			Error:      r.Error,
			CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
			FinishedAt: finishedAtStr,
			UserID:     r.UserID,
			APIKeyID:   r.APIKeyID,
			Attempt:    r.Attempt,
		})
	}

	return out, nil
}

// GetSkillRunStatsReq 指标统计入参
type GetSkillRunStatsReq struct {
	ID   int64 `uri:"id"`
	Days int   `form:"days"`
}

// GetSkillRunStats 处理指标统计 (GET /api/skills/:id/runs/stats)
func GetSkillRunStats(ctx context.Context, req *GetSkillRunStatsReq) (*dao.SkillRunStats, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := loadOwnedSkillRow(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	days := req.Days
	if days <= 0 {
		days = 7
	}

	return dao.GetSkillRunStats(ctx, req.ID, days)
}

// GetSkillRunDetailReq 单个详情入参
type GetSkillRunDetailReq struct {
	ID    int64  `uri:"id"`
	RunID string `uri:"run_id"`
}

// GetSkillRunDetail 处理任务详情查询 (GET /api/skills/:id/runs/:run_id)
func GetSkillRunDetail(ctx context.Context, req *GetSkillRunDetailReq) (any, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := loadOwnedSkillRow(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	run, err := dao.GetSkillRunByRunID(ctx, req.RunID)
	if err != nil {
		return nil, err
	}

	return formatRunDetail(run), nil
}

// RetrySkillRunReq 手动重试入参
type RetrySkillRunReq struct {
	ID    int64  `uri:"id"`
	RunID string `uri:"run_id"`
}

// RetrySkillRun 手动重试失败任务 (POST /api/skills/:id/runs/:run_id/retry)
func RetrySkillRun(ctx context.Context, req *RetrySkillRunReq) (any, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := loadOwnedSkillRow(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	run, err := dao.GetSkillRunByRunID(ctx, req.RunID)
	if err != nil {
		return nil, err
	}

	if run.Status != "failed" && run.Status != "timeout" && run.Status != "canceled" {
		return nil, errors.NewMsg(fmt.Sprintf("任务当前处于 %s 状态，无需重试", run.Status))
	}

	// 构造当前失败快照
	errStr := ""
	if run.Error != nil {
		errStr = *run.Error
	}
	fKind := dispatcher.FailureKindScriptError
	if run.FailureKind != nil && *run.FailureKind != "" {
		fKind = dispatcher.FailureKind(*run.FailureKind)
	}

	var dur int64
	if run.DurationMs != nil {
		dur = *run.DurationMs
	}

	stdoutTail := ""
	if run.StdoutTail != nil {
		stdoutTail = *run.StdoutTail
	}
	stderrTail := ""
	if run.StderrTail != nil {
		stderrTail = *run.StderrTail
	}

	snapshot := dispatcher.SkillRunAttempt{
		Attempt:     run.Attempt,
		FailureKind: fKind,
		Error:       errStr,
		DurationMs:  dur,
		StartedAt:   run.StartedAt,
		FinishedAt:  run.FinishedAt,
		StdoutTail:  stdoutTail,
		StderrTail:  stderrTail,
	}
	snapBytes, _ := json.Marshal(snapshot)

	// 重置任务为 pending 并清空本次错误现场
	if err := dao.ResetSkillRunForRetry(ctx, run.RunID, string(snapBytes), nil); err != nil {
		return nil, err
	}

	// 重新推入调度池
	dispatcher.Default().Submit(run.RunID)

	// 获取更新后的任务记录
	updated, err := dao.GetSkillRunByRunID(ctx, run.RunID)
	if err != nil {
		return nil, err
	}

	return formatRunDetail(updated), nil
}
