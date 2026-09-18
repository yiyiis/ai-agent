package dao_test

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
)

var testOnce sync.Once

func setupTestDB(t *testing.T) context.Context {
	t.Helper()
	testOnce.Do(func() {
		confPath := "../etc/config.yaml"
		if _, err := os.Stat("../etc/config.local.yaml"); err == nil {
			confPath = "../etc/config.local.yaml"
		}
		if _, err := os.Stat(confPath); os.IsNotExist(err) {
			confPath = filepath.Join("..", "etc", "config.yaml")
		}
		conf := config.LoadConfig(confPath)
		db.InitDb(conf.DbConf)
	})

	ctx := context.Background()
	return db.WithContext(ctx)
}

func TestApiKeyDAO(t *testing.T) {
	ctx := setupTestDB(t)

	testKeyStr := fmt.Sprintf("sk_test_%d", time.Now().UnixNano())
	key := &model.ApiKey{
		APIKey:        testKeyStr,
		Name:          "自动化测试密钥",
		CompanyID:     888,
		IsGlobal:      false,
		MaxConcurrent: 5,
		IsActive:      true,
	}

	if err := dao.CreateApiKey(ctx, key); err != nil {
		t.Fatalf("CreateApiKey failed: %v", err)
	}

	// 查询 Key
	found, err := dao.GetApiKey(ctx, testKeyStr)
	if err != nil {
		t.Fatalf("GetApiKey failed: %v", err)
	}
	if found.CompanyID != 888 || found.MaxConcurrent != 5 {
		t.Fatalf("mismatched key content: %+v", found)
	}

	// 统计并发数（初始为 0）
	count, err := dao.CountActiveRunsByKey(ctx, found.ID)
	if err != nil {
		t.Fatalf("CountActiveRunsByKey failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected count 0, got %d", count)
	}

	// 列表查询
	list, err := dao.ListApiKeysByCompany(ctx, 888)
	if err != nil {
		t.Fatalf("ListApiKeysByCompany failed: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("expected at least 1 key for company 888")
	}
}

func TestSkillRunDAO(t *testing.T) {
	ctx := setupTestDB(t)

	runID := fmt.Sprintf("run_test_%d", time.Now().UnixNano())
	skillID := int64(9999)
	companyID := int64(888)

	run := &model.SkillRun{
		RunID:       runID,
		SkillName:   "test-skill",
		SkillID:     &skillID,
		CompanyID:   companyID,
		UserID:      1001,
		Status:      "pending",
		Attempt:     1,
		MaxAttempts: 3,
	}

	if err := dao.CreateSkillRun(ctx, run); err != nil {
		t.Fatalf("CreateSkillRun failed: %v", err)
	}

	// 查询
	fetched, err := dao.GetSkillRunByRunID(ctx, runID)
	if err != nil {
		t.Fatalf("GetSkillRunByRunID failed: %v", err)
	}
	if fetched.Status != "pending" || fetched.SkillName != "test-skill" {
		t.Fatalf("unexpected fetched run: %+v", fetched)
	}

	// 租户隔离查询
	_, err = dao.GetSkillRunByRunIDAndCompany(ctx, runID, 999)
	if err == nil {
		t.Fatalf("expected error for mismatched companyID 999, got nil")
	}

	fetchedComp, err := dao.GetSkillRunByRunIDAndCompany(ctx, runID, companyID)
	if err != nil || fetchedComp == nil {
		t.Fatalf("GetSkillRunByRunIDAndCompany failed: %v", err)
	}

	// 更新状态至 running
	startTime := time.Now()
	if err := dao.UpdateSkillRun(ctx, runID, map[string]any{
		"status":     "running",
		"started_at": &startTime,
	}); err != nil {
		t.Fatalf("UpdateSkillRun to running failed: %v", err)
	}

	// 更新至 failed 并重试
	finishTime := time.Now()
	dur := int64(1500)
	errMsg := "网络连接超时"
	failKind := "network_error"
	stderrTail := "Connection reset by peer"
	if err := dao.UpdateSkillRun(ctx, runID, map[string]any{
		"status":       "failed",
		"finished_at":  &finishTime,
		"duration_ms":  &dur,
		"error":        &errMsg,
		"failure_kind": &failKind,
		"stderr_tail":  &stderrTail,
	}); err != nil {
		t.Fatalf("UpdateSkillRun to failed failed: %v", err)
	}

	// 重试重置
	snapshot := fmt.Sprintf(`{"attempt":1,"error":"%s","failure_kind":"%s"}`, errMsg, failKind)
	retryTime := time.Now().Add(2 * time.Second)
	if err := dao.ResetSkillRunForRetry(ctx, runID, snapshot, &retryTime); err != nil {
		t.Fatalf("ResetSkillRunForRetry failed: %v", err)
	}

	afterRetry, err := dao.GetSkillRunByRunID(ctx, runID)
	if err != nil {
		t.Fatalf("GetSkillRunByRunID after retry failed: %v", err)
	}
	if afterRetry.Status != "pending" || afterRetry.Attempt != 2 {
		t.Fatalf("expected status pending and attempt 2, got status=%s attempt=%d", afterRetry.Status, afterRetry.Attempt)
	}
	if afterRetry.Error != nil || afterRetry.StderrTail != nil {
		t.Fatalf("expected error and stderr_tail cleared, got error=%v stderr=%v", afterRetry.Error, afterRetry.StderrTail)
	}
	if afterRetry.Attempts == nil || *afterRetry.Attempts == "" {
		t.Fatalf("expected attempts snapshot recorded")
	}

	// 测试聚合统计 Stats
	stats, err := dao.GetSkillRunStats(ctx, skillID, 7)
	if err != nil {
		t.Fatalf("GetSkillRunStats failed: %v", err)
	}
	if stats == nil || stats.Days != 7 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// 测试列表查询
	runs, err := dao.ListSkillRuns(ctx, skillID, 7, 10, "")
	if err != nil {
		t.Fatalf("ListSkillRuns failed: %v", err)
	}
	if len(runs) == 0 {
		t.Fatalf("expected at least 1 run in list")
	}
}
