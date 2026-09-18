package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/sandbox"
)

func TestSkillRunsWebManagement(t *testing.T) {
	setupTestServer()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: t.TempDir()})
	ctx := db.WithContext(context.Background())

	uid := int64(701)
	token := manageToken(t, uid, "运维主管")

	// 创建测试用 Skill
	skName := "web-panel-skill"
	q := db.Ctx(ctx)
	newSkill := &model.Skill{
		Name:        skName,
		Description: "Web面板测试技能",
		DirName:     skName,
		OwnerID:     uid,
	}
	if err := q.Skill.WithContext(ctx).Create(newSkill); err != nil {
		t.Fatalf("create skill failed: %v", err)
	}
	skillID := newSkill.ID
	t.Cleanup(func() {
		_, _ = q.SkillRun.WithContext(ctx).Where(q.SkillRun.SkillID.Eq(skillID)).Delete()
		_, _ = q.Skill.WithContext(ctx).Where(q.Skill.ID.Eq(skillID)).Delete()
	})

	// 插入历史运行数据
	now := time.Now()
	dur := int64(1200)
	errMsg := "外部接口超时"
	failKind := "network_error"
	runFail := &model.SkillRun{
		RunID:       fmt.Sprintf("run_fail_%d", now.UnixNano()),
		SkillName:   skName,
		SkillID:     &skillID,
		CompanyID:   1,
		UserID:      uid,
		Status:      "failed",
		DurationMs:  &dur,
		Error:       &errMsg,
		FailureKind: &failKind,
		Attempt:     1,
		MaxAttempts: 3,
		CreatedAt:   now,
	}
	_ = dao.CreateSkillRun(ctx, runFail)

	durSucc := int64(800)
	runSucc := &model.SkillRun{
		RunID:      fmt.Sprintf("run_succ_%d", now.UnixNano()),
		SkillName:  skName,
		SkillID:    &skillID,
		CompanyID:  1,
		UserID:     uid,
		Status:     "success",
		DurationMs: &durSucc,
		Attempt:    1,
		CreatedAt:  now,
	}
	_ = dao.CreateSkillRun(ctx, runSucc)

	// 1. 测试 GET /api/skills/:id/runs 列表
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", fmt.Sprintf("/api/skills/%d/runs?days=7", skillID), nil)
	req.Header.Set("x-token", token)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for list runs, got %d", w.Code)
	}
	env := decodeEnvelope(t, w)
	if env.Code != 0 {
		t.Fatalf("expected code 0, got %d msg=%s", env.Code, env.Msg)
	}
	var runsList []map[string]any
	_ = json.Unmarshal(env.Data, &runsList)
	if len(runsList) < 2 {
		t.Fatalf("expected at least 2 runs in list, got %d", len(runsList))
	}

	// 2. 测试 GET /api/skills/:id/runs/stats 统计
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", fmt.Sprintf("/api/skills/%d/runs/stats?days=7", skillID), nil)
	req.Header.Set("x-token", token)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for stats, got %d", w.Code)
	}
	env = decodeEnvelope(t, w)
	var stats map[string]any
	_ = json.Unmarshal(env.Data, &stats)
	if total, ok := stats["total"].(float64); !ok || int(total) < 2 {
		t.Fatalf("expected stats total >= 2, got %v", stats["total"])
	}

	// 3. 测试 GET /api/skills/:id/runs/:run_id 详情
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", fmt.Sprintf("/api/skills/%d/runs/%s", skillID, runFail.RunID), nil)
	req.Header.Set("x-token", token)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for detail, got %d", w.Code)
	}
	env = decodeEnvelope(t, w)
	var detail map[string]any
	_ = json.Unmarshal(env.Data, &detail)
	if detail["status"] != "failed" {
		t.Fatalf("expected status failed, got %v", detail["status"])
	}
	if retryable, ok := detail["retryable"].(bool); !ok || !retryable {
		t.Fatalf("expected failed task to be retryable=true, got %v", detail["retryable"])
	}

	// 4. 测试 POST /api/skills/:id/runs/:run_id/retry 手动重试
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", fmt.Sprintf("/api/skills/%d/runs/%s/retry", skillID, runFail.RunID), nil)
	req.Header.Set("x-token", token)
	testEngine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for retry, got %d", w.Code)
	}
	env = decodeEnvelope(t, w)
	var retryDetail map[string]any
	_ = json.Unmarshal(env.Data, &retryDetail)
	if retryDetail["id"] != runFail.RunID {
		t.Fatalf("expected runId to be preserved on retry, got %v", retryDetail["id"])
	}
	if attempt, ok := retryDetail["attempt"].(float64); !ok || int(attempt) != 2 {
		t.Fatalf("expected attempt 2 after retry, got %v", retryDetail["attempt"])
	}
	attemptsList, ok := retryDetail["attempts"].([]any)
	if !ok || len(attemptsList) == 0 {
		t.Fatalf("expected history attempts recorded in retry detail, got %v", retryDetail["attempts"])
	}
}
