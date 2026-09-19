package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResultExtractorConf 产物抽取器配置
type ResultExtractorConf struct {
	Name    string `json:"name"`
	Type    string `json:"type"`    // "regex_stdout" | "regex_all_stdout" | "file"
	Pattern string `json:"pattern"` // 正则表达式（regex_stdout）
	Group   int    `json:"group"`   // 捕获组索引，默认 1
	DotAll  bool   `json:"dotall"`  // 是否开启跨行
	AsJSON  bool   `json:"as_json"` // 是否将提取的文本解析为 JSON 对象
	Path    string `json:"path"`    // 文件路径或 glob 模板（file 模式），如 "{workdir}/*.xlsx"
	Upload  bool   `json:"upload"`  // 是否自动上传对象存储
}

// RetryConf 任务自动重试策略
type RetryConf struct {
	MaxAttempts int      `json:"max_attempts"`
	On          []string `json:"on"` // "sandbox", "timeout", "script_error"
	BackoffSec  int      `json:"backoff_sec"`
}

// Manifest 技能对外 API 契约与执行声明 (manifest.json)
type Manifest struct {
	APIEnabled        bool                  `json:"api_enabled"`
	Version           string                `json:"version"`
	Description       string                `json:"description"`
	Command           string                `json:"command"`
	Stdin             string                `json:"stdin"` // "params_json" | "none"
	TimeoutSec        int                   `json:"timeout_sec"`
	DefaultMode       string                `json:"default_mode"` // "sync" | "async" | "llm"
	InputSchema       map[string]any        `json:"input_schema"`
	ResultExtractors  []ResultExtractorConf `json:"result_extractors"`
	LLMEnabled        bool                  `json:"llm_enabled"`
	LLMPromptTemplate string                `json:"llm_prompt_template"`
	Retry             *RetryConf            `json:"retry"`
}

// ReadManifest 读取并解析指定技能目录下的 manifest.json。
// 若文件不存在，返回 (nil, nil)；若 JSON 损坏则返回详细错误。
func ReadManifest(dirName string) (*Manifest, error) {
	manifestPath := filepath.Join(rootDir, dirName, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 manifest.json 失败: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 manifest.json 格式错误: %w", err)
	}

	// 默认兜底
	if m.TimeoutSec <= 0 {
		m.TimeoutSec = 60
	}
	if m.Stdin == "" {
		m.Stdin = "params_json"
	}
	if m.DefaultMode == "" {
		m.DefaultMode = "sync"
	}

	return &m, nil
}

// ValidateParams 校验入参是否符合 input_schema 定义的约束（支持 required 与常见类型）
func ValidateParams(params map[string]any, schema map[string]any) error {
	if schema == nil || len(schema) == 0 {
		return nil
	}

	if params == nil {
		params = make(map[string]any)
	}

	// 1. 校验必填项 required
	if reqFields, ok := schema["required"].([]any); ok {
		for _, f := range reqFields {
			fieldName, _ := f.(string)
			if fieldName == "" {
				continue
			}
			val, exists := params[fieldName]
			if !exists || val == nil {
				return fmt.Errorf("缺少必填参数: %s", fieldName)
			}
			if strVal, ok := val.(string); ok && strings.TrimSpace(strVal) == "" {
				return fmt.Errorf("必填参数 '%s' 不能为空字符串", fieldName)
			}
		}
	} else if reqFieldsStr, ok := schema["required"].([]string); ok {
		for _, fieldName := range reqFieldsStr {
			if fieldName == "" {
				continue
			}
			val, exists := params[fieldName]
			if !exists || val == nil {
				return fmt.Errorf("缺少必填参数: %s", fieldName)
			}
			if strVal, ok := val.(string); ok && strings.TrimSpace(strVal) == "" {
				return fmt.Errorf("必填参数 '%s' 不能为空字符串", fieldName)
			}
		}
	}

	// 2. 校验字段类型 properties
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil
	}

	for key, val := range params {
		propRaw, hasProp := props[key]
		if !hasProp || propRaw == nil {
			continue
		}
		propDef, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		expectedType, _ := propDef["type"].(string)
		if expectedType == "" {
			continue
		}

		if val == nil {
			continue
		}

		if err := validateType(key, val, expectedType); err != nil {
			return err
		}
	}

	return nil
}

func validateType(key string, val any, expectedType string) error {
	switch expectedType {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("参数 '%s' 类型不匹配: 期望 string, 实际为 %T", key, val)
		}
	case "number":
		switch val.(type) {
		case float64, float32, int, int64, int32, int16, int8:
		default:
			return fmt.Errorf("参数 '%s' 类型不匹配: 期望 number, 实际为 %T", key, val)
		}
	case "integer":
		switch v := val.(type) {
		case int, int64, int32, int16, int8:
		case float64:
			if v != float64(int64(v)) {
				return fmt.Errorf("参数 '%s' 类型不匹配: 期望 integer 整数, 实际为浮点数 %v", key, v)
			}
		default:
			return fmt.Errorf("参数 '%s' 类型不匹配: 期望 integer, 实际为 %T", key, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("参数 '%s' 类型不匹配: 期望 boolean, 实际为 %T", key, val)
		}
	case "array":
		switch val.(type) {
		case []any, []string, []int, []float64, []map[string]any:
		default:
			return fmt.Errorf("参数 '%s' 类型不匹配: 期望 array 列表, 实际为 %T", key, val)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("参数 '%s' 类型不匹配: 期望 object 对象, 实际为 %T", key, val)
		}
	}
	return nil
}

// RenderTemplate 替换模板字符串中的占位符，例如 {dir}, {workdir}, {run_id}, {params_json}
func RenderTemplate(tpl string, vars map[string]string) string {
	res := tpl
	for k, v := range vars {
		res = strings.ReplaceAll(res, "{"+k+"}", v)
	}
	return res
}
