package dispatcher

import "time"

// FailureKind 任务失败分类标签
type FailureKind string

const (
	FailureKindParamInvalid FailureKind = "param_invalid" // 参数校验或格式不合法
	FailureKindScriptError  FailureKind = "script_error"  // 脚本语法或代码逻辑崩溃
	FailureKindTimeout      FailureKind = "timeout"       // 任务执行耗时超过上限强杀
	FailureKindSandbox      FailureKind = "sandbox"       // 沙箱容器启动/文件IO异常
	FailureKindRateLimited  FailureKind = "rate_limited"  // 上游 LLM 触发 429 速率限制
	FailureKindNetworkError FailureKind = "network_error" // 外部依赖网络抖动或丢包
)

// SkillRunAttempt 单次尝试的现场诊断快照（追加落库 attempts 字段）
type SkillRunAttempt struct {
	Attempt     int64       `json:"attempt"`
	FailureKind FailureKind `json:"failure_kind"`
	Error       string      `json:"error"`
	DurationMs  int64       `json:"duration_ms"`
	StartedAt   *time.Time  `json:"started_at"`
	FinishedAt  *time.Time  `json:"finished_at"`
	StdoutTail  string      `json:"stdout_tail"`
	StderrTail  string      `json:"stderr_tail"`
}

// ExecResult 任务执行器产生的综合结果
type ExecResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	Killed      bool
	DurationMs  int64
	ResultJSON  map[string]any
	Artifacts   []map[string]any
	Err         error
	FailureKind FailureKind
}
