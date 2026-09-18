package dispatcher_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"backend/config"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/dispatcher"
	"backend/pkg/sandbox"
	"backend/pkg/skill"
)

var testDBOnce sync.Once

func setupTestEnv(t *testing.T) context.Context {
	t.Helper()
	testDBOnce.Do(func() {
		confPath := "../../etc/config.yaml"
		if _, err := os.Stat("../../etc/config.local.yaml"); err == nil {
			confPath = "../../etc/config.local.yaml"
		}
		if _, err := os.Stat(confPath); os.IsNotExist(err) {
			confPath = filepath.Join("..", "..", "etc", "config.yaml")
		}
		conf := config.LoadConfig(confPath)
		db.InitDb(conf.DbConf)
		sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: t.TempDir()})

		skillDir := "../../skills"
		if abs, err := filepath.Abs("../../skills"); err == nil {
			skillDir = abs
		}
		skill.Init(skill.Config{Dir: skillDir})
	})

	ctx := context.Background()
	return db.WithContext(ctx)
}

func TestDispatcherSubmitAndSuccess(t *testing.T) {
	ctx := setupTestEnv(t)

	disp := dispatcher.Init(5)
	disp.Start(ctx)
	defer disp.Stop()

	runID := fmt.Sprintf("run_disp_succ_%d", time.Now().UnixNano())
	cmdContext := `{"command": "echo '{\"message\": \"ok\"}'"}`

	run := &model.SkillRun{
		RunID:       runID,
		SkillName:   "fetch-url",
		CompanyID:   1,
		UserID:      1001,
		Status:      "pending",
		Attempt:     1,
		MaxAttempts: 1,
		ExecContext: &cmdContext,
	}

	if err := dao.CreateSkillRun(ctx, run); err != nil {
		t.Fatalf("CreateSkillRun failed: %v", err)
	}

	disp.Submit(runID)

	// 轮询等待任务完成
	deadline := time.Now().Add(10 * time.Second)
	var finalRun *model.SkillRun
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		r, err := dao.GetSkillRunByRunID(ctx, runID)
		if err == nil && (r.Status == "success" || r.Status == "failed") {
			finalRun = r
			break
		}
	}

	if finalRun == nil {
		t.Fatalf("任务在 10s 内未完成")
	}
	if finalRun.Status != "success" {
		t.Fatalf("expected status success, got %s, stderr: %v, error: %v", finalRun.Status, finalRun.StderrTail, finalRun.Error)
	}
	if finalRun.Result == nil || *finalRun.Result == "" {
		t.Fatalf("expected non-empty result")
	}
}

func TestDispatcherCancel(t *testing.T) {
	ctx := setupTestEnv(t)

	disp := dispatcher.Init(5)
	disp.Start(ctx)
	defer disp.Stop()

	runID := fmt.Sprintf("run_disp_cancel_%d", time.Now().UnixNano())
	cmdContext := `{"command": "sleep 10"}`

	run := &model.SkillRun{
		RunID:       runID,
		SkillName:   "fetch-url",
		CompanyID:   1,
		UserID:      1001,
		Status:      "pending",
		Attempt:     1,
		MaxAttempts: 1,
		ExecContext: &cmdContext,
	}

	if err := dao.CreateSkillRun(ctx, run); err != nil {
		t.Fatalf("CreateSkillRun failed: %v", err)
	}

	disp.Submit(runID)

	// 等待进入 running 状态
	time.Sleep(300 * time.Millisecond)

	// 取消任务
	if err := disp.Cancel(ctx, runID); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	r, err := dao.GetSkillRunByRunID(ctx, runID)
	if err != nil {
		t.Fatalf("GetSkillRunByRunID failed: %v", err)
	}
	if r.Status != "canceled" {
		t.Fatalf("expected status canceled, got %s", r.Status)
	}
}
