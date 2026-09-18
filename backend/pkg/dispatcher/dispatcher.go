package dispatcher

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/errors"
)

var (
	defaultDispatcher *Dispatcher
	dispOnce          sync.Once
)

// Dispatcher 分布式/异步任务调度器
type Dispatcher struct {
	taskChan      chan string
	workerSlots   chan struct{}
	activeCancels sync.Map // run_id -> context.CancelFunc
	executor      *RunExecutor
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	runningRuns   sync.Map // run_id -> struct{} 防止重复调度
}

// Init 初始化全局调度器单例
func Init(workers int) *Dispatcher {
	dispOnce.Do(func() {
		if workers <= 0 {
			workers = 10
		}
		defaultDispatcher = &Dispatcher{
			taskChan:    make(chan string, 500),
			workerSlots: make(chan struct{}, workers),
			executor:    NewExecutor(),
		}
	})
	return defaultDispatcher
}

// Default 获取调度器单例
func Default() *Dispatcher {
	if defaultDispatcher == nil {
		return Init(10)
	}
	return defaultDispatcher
}

// Start 启动后台调度循环与补偿轮询器
func (d *Dispatcher) Start(ctx context.Context) {
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.wg.Add(2)

	// 1. 实时投递监听通道
	go d.dispatchLoop()

	// 2. 10 秒定时补偿轮询器（捞取超时未调度或到达重试时间的任务）
	go d.pollLoop()

	slog.Info("异步任务调度器 (Dispatcher) 已启动")
}

// Stop 优雅关闭调度器并等待正在执行的任务完成
func (d *Dispatcher) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	slog.Info("异步任务调度器 (Dispatcher) 已安全停止")
}

// Submit 投递新任务进入待执行队列
func (d *Dispatcher) Submit(runID string) {
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return
	}
	select {
	case d.taskChan <- rid:
	default:
		// 通道满时降级依赖 10s 轮询补偿器
		slog.Warn("调度队列已满，任务将由补偿轮询器拾取", "run_id", rid)
	}
}

// Cancel 强杀/取消正在执行或排队中的任务
func (d *Dispatcher) Cancel(ctx context.Context, runID string) error {
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return errors.NewMsg("run_id 不能为空")
	}

	// 1. 若任务正在执行，触发 CancelFunc 中断沙箱
	if cancelVal, ok := d.activeCancels.Load(rid); ok {
		if cancelFn, ok := cancelVal.(context.CancelFunc); ok {
			cancelFn()
		}
		d.activeCancels.Delete(rid)
	}

	// 2. 更新数据库状态为 canceled
	now := time.Now()
	errSummary := "任务已被客户端主动取消"
	updates := map[string]any{
		"status":      "canceled",
		"finished_at": &now,
		"error":       &errSummary,
	}
	return dao.UpdateSkillRun(ctx, rid, updates)
}

func (d *Dispatcher) dispatchLoop() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case rid := <-d.taskChan:
			d.acquireAndSpawn(rid)
		}
	}
}

func (d *Dispatcher) pollLoop() {
	defer d.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.reapPendingRuns()
		}
	}
}

func (d *Dispatcher) reapPendingRuns() {
	ctx := db.WithContext(d.ctx)
	runs, err := dao.FindPendingRunsToDispatch(ctx, 20)
	if err != nil || len(runs) == 0 {
		return
	}
	for _, r := range runs {
		if _, processing := d.runningRuns.Load(r.RunID); !processing {
			d.Submit(r.RunID)
		}
	}
}

func (d *Dispatcher) acquireAndSpawn(runID string) {
	// 防止同一个任务多次重复进入 Worker 执行
	if _, loaded := d.runningRuns.LoadOrStore(runID, struct{}{}); loaded {
		return
	}

	select {
	case <-d.ctx.Done():
		d.runningRuns.Delete(runID)
		return
	case d.workerSlots <- struct{}{}:
		d.wg.Add(1)
		go d.runWorker(runID)
	}
}

func (d *Dispatcher) runWorker(runID string) {
	defer func() {
		<-d.workerSlots
		d.runningRuns.Delete(runID)
		d.wg.Done()
	}()

	ctx := db.WithContext(d.ctx)
	run, err := dao.GetSkillRunByRunID(ctx, runID)
	if err != nil || run == nil {
		return
	}
	// 仅 pending 任务可调度
	if run.Status != "pending" {
		return
	}

	// 任务执行上下文绑定与取消注册
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.activeCancels.Store(runID, cancel)
	defer d.activeCancels.Delete(runID)

	// 更新状态为 running
	startTime := time.Now()
	_ = dao.UpdateSkillRun(ctx, runID, map[string]any{
		"status":     "running",
		"started_at": &startTime,
	})

	// 获取技能配置
	var targetSkill *model.Skill
	q := db.Ctx(ctx)
	sk, _ := q.Skill.WithContext(ctx).Where(q.Skill.Name.Eq(run.SkillName)).First()
	if sk != nil {
		targetSkill = sk
	}

	// 执行任务
	res := d.executor.Execute(runCtx, run, targetSkill)

	// 若在执行期间被主动取消，跳过后续状态覆盖
	if runCtx.Err() != nil {
		return
	}

	// 结果收敛与状态收敛
	d.handleExecutionResult(ctx, run, res, startTime)
}

func (d *Dispatcher) handleExecutionResult(ctx context.Context, run *model.SkillRun, res *ExecResult, startTime time.Time) {
	finishTime := time.Now()

	// 失败场景处理
	if res.Err != nil || res.ExitCode != 0 || res.Killed {
		fKind := string(res.FailureKind)
		errText := ""
		if res.Err != nil {
			errText = res.Err.Error()
		} else if res.Stderr != "" {
			errText = res.Stderr
		}

		stdoutTail := tailStr(res.Stdout, 4000)
		stderrTail := tailStr(res.Stderr, 4000)

		// 判定是否可重试
		if run.Attempt < run.MaxAttempts && IsRetryable(res.FailureKind) {
			snapshot := SkillRunAttempt{
				Attempt:     run.Attempt,
				FailureKind: res.FailureKind,
				Error:       errText,
				DurationMs:  res.DurationMs,
				StartedAt:   &startTime,
				FinishedAt:  &finishTime,
				StdoutTail:  stdoutTail,
				StderrTail:  stderrTail,
			}
			snapBytes, _ := json.Marshal(snapshot)

			delay := CalculateBackoff(run.Attempt)
			nextRetry := time.Now().Add(delay)

			slog.Info("任务失败，进入指数退避等待重试",
				"run_id", run.RunID,
				"attempt", run.Attempt,
				"delay_sec", delay.Seconds(),
				"failure_kind", fKind,
			)

			_ = dao.ResetSkillRunForRetry(ctx, run.RunID, string(snapBytes), &nextRetry)
			return
		}

		// 终态失败
		_ = dao.UpdateSkillRun(ctx, run.RunID, map[string]any{
			"status":       "failed",
			"finished_at":  &finishTime,
			"duration_ms":  res.DurationMs,
			"error":        &errText,
			"failure_kind": &fKind,
			"stdout_tail":  &stdoutTail,
			"stderr_tail":  &stderrTail,
		})
		return
	}

	// 成功场景处理
	var resultJSONStr *string
	if res.ResultJSON != nil {
		if b, err := json.Marshal(res.ResultJSON); err == nil {
			s := string(b)
			resultJSONStr = &s
		}
	}
	var artifactsJSONStr *string
	if len(res.Artifacts) > 0 {
		if b, err := json.Marshal(res.Artifacts); err == nil {
			s := string(b)
			artifactsJSONStr = &s
		}
	}

	stdoutTail := tailStr(res.Stdout, 4000)
	stderrTail := tailStr(res.Stderr, 4000)

	_ = dao.UpdateSkillRun(ctx, run.RunID, map[string]any{
		"status":      "success",
		"finished_at": &finishTime,
		"duration_ms": res.DurationMs,
		"result":      resultJSONStr,
		"artifacts":   artifactsJSONStr,
		"stdout_tail": &stdoutTail,
		"stderr_tail": &stderrTail,
		"error":       nil,
	})
}

func tailStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[len(s)-maxLen:]
}
