package dispatcher_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backend/dal/model"
	"backend/pkg/dispatcher"
	"backend/pkg/sandbox"
	"backend/pkg/skill"
)

func TestExecutorWithManifestAndRegexExtractor(t *testing.T) {
	// 1. 初始化本地沙箱
	tempSandbox := t.TempDir()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: tempSandbox})

	// 2. 准备带 manifest.json 的测试技能
	tempSkills := t.TempDir()
	skill.Init(skill.Config{Dir: tempSkills})

	skillDir := filepath.Join(tempSkills, "test-manifest-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}

	skillMD := `---
name: test-manifest-skill
description: manifest 测试技能
---
# 指引
`
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644)

	// manifest 配置：从 stdin 读 json 并写到 received.json，输出 [API_RESULT_BEGIN] 结果，同时写 report.txt
	manifestJSON := `{
		"api_enabled": true,
		"version": "1.0",
		"command": "cat - > {workdir}/received.json && echo 'some debug logs...' && echo '[API_RESULT_BEGIN]{\"echo\": \"antigravity\", \"code\": 200}[API_RESULT_END]' && echo 'hello artifact' > {workdir}/report.txt",
		"stdin": "params_json",
		"timeout_sec": 30,
		"input_schema": {
			"type": "object",
			"required": ["name"]
		},
		"result_extractors": [
			{
				"name": "data",
				"type": "regex_stdout",
				"pattern": "\\[API_RESULT_BEGIN\\]\\s*(.+?)\\s*\\[API_RESULT_END\\]",
				"group": 1,
				"dotall": true,
				"as_json": true
			},
			{
				"name": "report_content",
				"type": "file",
				"path": "{workdir}/report.txt",
				"upload": false
			}
		]
	}`
	_ = os.WriteFile(filepath.Join(skillDir, "manifest.json"), []byte(manifestJSON), 0o644)

	// 3. 执行任务
	executor := dispatcher.NewExecutor()
	paramsStr := `{"name": "antigravity"}`
	run := &model.SkillRun{
		RunID:     "test_run_manifest_001",
		SkillName: "test-manifest-skill",
		Params:    &paramsStr,
		Attempt:   1,
	}

	res := executor.Execute(context.Background(), run, nil)
	if res.Err != nil {
		t.Fatalf("executor failed: %v, stderr: %s", res.Err, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code non-zero: %d, stderr: %s", res.ExitCode, res.Stderr)
	}

	// 4. 校验结果抽取
	if res.ResultJSON == nil {
		t.Fatal("expected non-nil ResultJSON")
	}

	// 校验 regex_stdout + as_json
	dataObj, ok := res.ResultJSON["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected res.ResultJSON['data'] to be map, got %T: %+v", res.ResultJSON["data"], res.ResultJSON)
	}
	if dataObj["echo"] != "antigravity" || dataObj["code"] != float64(200) {
		t.Errorf("extracted data mismatch: %+v", dataObj)
	}

	// 校验 file extractor
	reportContent, ok := res.ResultJSON["report_content"].(string)
	if !ok || strings.TrimSpace(reportContent) != "hello artifact" {
		t.Errorf("report_content mismatch: got %q, expected 'hello artifact'", reportContent)
	}
}

func TestExecutorFallbackWithoutManifest(t *testing.T) {
	tempSandbox := t.TempDir()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: tempSandbox})

	tempSkills := t.TempDir()
	skill.Init(skill.Config{Dir: tempSkills})

	skillDir := filepath.Join(tempSkills, "test-fallback-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: test-fallback-skill\n---\n"), 0o644)

	executor := dispatcher.NewExecutor()
	execCtx := `{"command": "echo '{\"status\": \"fallback_ok\"}'"}`
	run := &model.SkillRun{
		RunID:       "test_run_fallback_002",
		SkillName:   "test-fallback-skill",
		ExecContext: &execCtx,
		Attempt:     1,
	}

	res := executor.Execute(context.Background(), run, nil)
	if res.Err != nil {
		t.Fatalf("executor failed: %v", res.Err)
	}

	if res.ResultJSON == nil || res.ResultJSON["status"] != "fallback_ok" {
		t.Errorf("expected fallback result status=fallback_ok, got %+v", res.ResultJSON)
	}
}
