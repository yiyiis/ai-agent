// context_window.go 模型上下文窗口表（上下文组装分区的预算基准）。
// 配置经 LLM.ContextWindows 装载（Billing 同款"yaml 切片 → 运行时 map"先例），
// 匹配次序与 registry.GetProviderForModel 保持一致：精确 → 忽略大小写 →
// glm/minimax 前缀 → 默认值。
package provider

import (
	"strings"
	"sync"
)

// DefaultContextWindow 未配置模型的兜底窗口（GLM 系当前通用 128K）
const DefaultContextWindow = 131072

var (
	contextWindowsMu sync.RWMutex
	contextWindows   = map[string]int{}
)

// ContextWindowEntry 单模型窗口配置项
type ContextWindowEntry struct {
	Model  string `yaml:"Model" mapstructure:"Model"`
	Window int    `yaml:"Window" mapstructure:"Window"`
}

// SetContextWindows 装载模型窗口表（InitProviders 调用，空项忽略）
func SetContextWindows(entries []ContextWindowEntry) {
	m := make(map[string]int, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.Model) == "" || e.Window <= 0 {
			continue
		}
		m[strings.ToLower(strings.TrimSpace(e.Model))] = e.Window
	}
	contextWindowsMu.Lock()
	contextWindows = m
	contextWindowsMu.Unlock()
}

// ContextWindow 模型的上下文窗口 token 数
func ContextWindow(model string) int {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return DefaultContextWindow
	}
	lower := strings.ToLower(trimmed)

	contextWindowsMu.RLock()
	defer contextWindowsMu.RUnlock()
	if w, ok := contextWindows[lower]; ok {
		return w
	}
	// 前缀推断：同家族窗口通常一致（glm-5.3-x 未显式配置时吃 glm-5.3 的配置）
	for m, w := range contextWindows {
		if strings.HasPrefix(lower, m) || strings.HasPrefix(m, lower) {
			return w
		}
	}
	return DefaultContextWindow
}
