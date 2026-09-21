// Package ctxpolicy 上下文组装分区策略（config AgentPolicy 段的自持配置包）。
// config 装配它、agent 消费它——独立成叶子包避免 config → agent 的导入环
//（agent 测试要加载 config 连本地 MySQL）。main 启动时 Init 一次，之后只读。
package ctxpolicy

// ContextPolicy 上下文组装策略（零值字段保持默认）
type ContextPolicy struct {
	// HotSteps 按工具的热区 step 阈值：该工具的调用与结果在最近 N 个 step 内保留原文
	//（step = 一次上游调用：含 tool_calls 的 assistant 批次 + 其全部结果）
	HotSteps []HotStepRule `yaml:"HotSteps" mapstructure:"HotSteps"`
	// DefaultHotSteps 未列工具的默认阈值
	DefaultHotSteps int `yaml:"DefaultHotSteps" mapstructure:"DefaultHotSteps"`
	// HotBudgetRatio 热区 token 预算（占模型窗口比例）
	HotBudgetRatio float64 `yaml:"HotBudgetRatio" mapstructure:"HotBudgetRatio"`
	// RezoneRatio 轮内 rezone 触发线（上一轮实测 prompt_tokens 超窗口 × 此比例）
	RezoneRatio float64 `yaml:"RezoneRatio" mapstructure:"RezoneRatio"`
	// ImageKeepLast 历史图片保留张数（其余换文本占位）
	ImageKeepLast int `yaml:"ImageKeepLast" mapstructure:"ImageKeepLast"`
}

// HotStepRule 单工具的热区阈值
type HotStepRule struct {
	Tool  string `yaml:"Tool" mapstructure:"Tool"`
	Steps int    `yaml:"Steps" mapstructure:"Steps"`
}

var current = ContextPolicy{
	HotSteps: []HotStepRule{
		{Tool: "read_file", Steps: 2},
		{Tool: "write_file", Steps: 1},
		{Tool: "edit_file", Steps: 1},
		{Tool: "bash", Steps: 3},
		{Tool: "python_exec", Steps: 3},
	},
	DefaultHotSteps: 8,
	HotBudgetRatio:  0.25,
	RezoneRatio:     0.65,
	ImageKeepLast:   2,
}

// Init 装载配置策略（零值字段保持默认，兼容缺段的 config.local.yaml）
func Init(p ContextPolicy) {
	if len(p.HotSteps) > 0 {
		current.HotSteps = p.HotSteps
	}
	if p.DefaultHotSteps > 0 {
		current.DefaultHotSteps = p.DefaultHotSteps
	}
	if p.HotBudgetRatio > 0 {
		current.HotBudgetRatio = p.HotBudgetRatio
	}
	if p.RezoneRatio > 0 {
		current.RezoneRatio = p.RezoneRatio
	}
	if p.ImageKeepLast > 0 {
		current.ImageKeepLast = p.ImageKeepLast
	}
}

// Current 当前生效策略快照（值拷贝，启动后只读）
func Current() ContextPolicy { return current }

// HotStepsFor 工具的热区 step 阈值
func HotStepsFor(tool string) int {
	for _, r := range current.HotSteps {
		if r.Tool == tool && r.Steps > 0 {
			return r.Steps
		}
	}
	return current.DefaultHotSteps
}
