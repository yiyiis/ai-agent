package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadManifest(t *testing.T) {
	tempDir := t.TempDir()
	oldRoot := rootDir
	rootDir = tempDir
	defer func() { rootDir = oldRoot }()

	// 1. 无 manifest 场景 -> 返回 nil, nil
	m, err := ReadManifest("non-exist")
	if err != nil {
		t.Fatalf("expected nil error for missing manifest, got %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil manifest, got %+v", m)
	}

	// 2. 正常读取 manifest.json
	skillDir := filepath.Join(tempDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{
		"api_enabled": true,
		"version": "1.0",
		"description": "测试技能",
		"command": "python3 /skills/{dir}/run.py",
		"stdin": "params_json",
		"timeout_sec": 45,
		"input_schema": {
			"type": "object",
			"required": ["url"],
			"properties": {
				"url": {"type": "string"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(skillDir, "manifest.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err = ReadManifest("my-skill")
	if err != nil {
		t.Fatalf("ReadManifest failed: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil manifest")
	}
	if !m.APIEnabled || m.Version != "1.0" || m.TimeoutSec != 45 || m.Stdin != "params_json" {
		t.Errorf("manifest fields mismatch: %+v", m)
	}
}

func TestValidateParams(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"url", "limit"},
		"properties": map[string]any{
			"url":     map[string]any{"type": "string"},
			"limit":   map[string]any{"type": "integer"},
			"score":   map[string]any{"type": "number"},
			"verbose": map[string]any{"type": "boolean"},
			"tags":    map[string]any{"type": "array"},
			"extra":   map[string]any{"type": "object"},
		},
	}

	// 缺少必填项
	err := ValidateParams(map[string]any{"url": "https://example.com"}, schema)
	if err == nil {
		t.Error("expected error for missing required field limit")
	}

	// 空字符串
	err = ValidateParams(map[string]any{"url": "   ", "limit": 10}, schema)
	if err == nil {
		t.Error("expected error for empty string required field")
	}

	// 类型错误: limit 应该是 integer 但传入 string
	err = ValidateParams(map[string]any{"url": "https://example.com", "limit": "ten"}, schema)
	if err == nil {
		t.Error("expected error for wrong type")
	}

	// 类型错误: limit 传入小数
	err = ValidateParams(map[string]any{"url": "https://example.com", "limit": 10.5}, schema)
	if err == nil {
		t.Error("expected error for float when expecting integer")
	}

	// 全部合法
	err = ValidateParams(map[string]any{
		"url":     "https://example.com",
		"limit":   10,
		"score":   98.5,
		"verbose": true,
		"tags":    []any{"a", "b"},
		"extra":   map[string]any{"k": "v"},
	}, schema)
	if err != nil {
		t.Errorf("expected no error for valid params, got %v", err)
	}
}

func TestRenderTemplate(t *testing.T) {
	tpl := "python3 /skills/{dir}/main.py --workdir {workdir} --id {run_id}"
	vars := map[string]string{
		"dir":     "my_skill",
		"workdir": "/tmp/runs/run1",
		"run_id":  "run1",
	}
	got := RenderTemplate(tpl, vars)
	expected := "python3 /skills/my_skill/main.py --workdir /tmp/runs/run1 --id run1"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
