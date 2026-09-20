package provider

import (
	"strings"
	"sync"
)

var (
	providersMu sync.RWMutex
	providers   = make(map[string]Provider)
	modelRoutes = make(map[string]string)
)

func RegisterProvider(name string, p Provider) {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers[name] = p
}

func RegisterModelRoute(model, providerName string) {
	providersMu.Lock()
	defer providersMu.Unlock()
	modelRoutes[model] = providerName
}

func GetProviderForModel(model string) (Provider, bool) {
	providersMu.RLock()
	defer providersMu.RUnlock()

	trimmed := strings.TrimSpace(model)

	// 1. 精确匹配路由
	if pName, ok := modelRoutes[trimmed]; ok {
		if p, exists := providers[pName]; exists {
			return p, true
		}
	}

	// 2. 大小写不敏感匹配
	for m, pName := range modelRoutes {
		if strings.EqualFold(m, trimmed) {
			if p, exists := providers[pName]; exists {
				return p, true
			}
		}
	}

	// 3. 基于模型名称前缀智能推断
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "glm") {
		if p, exists := providers["glm"]; exists {
			return p, true
		}
	}
	if strings.HasPrefix(lower, "minimax") {
		if p, exists := providers["minimax"]; exists {
			return p, true
		}
	}

	// 4. 回退至首个已注册的 provider
	for _, p := range providers {
		return p, true
	}
	return nil, false
}

// ProviderKeyForModel 返回模型的计费归属 provider 名(供 token_usages.provider_key 分账)。
// 与 GetProviderForModel 的匹配链一致(精确→忽略大小写→前缀推断),但不做兜底:
// 计费归属准确性优先,匹配不到返回空串——已下线模型的用量仍要记账,归属留空即可
func ProviderKeyForModel(model string) string {
	providersMu.RLock()
	defer providersMu.RUnlock()

	trimmed := strings.TrimSpace(model)

	// 1. 精确匹配路由
	if pName, ok := modelRoutes[trimmed]; ok {
		return pName
	}

	// 2. 大小写不敏感匹配
	for m, pName := range modelRoutes {
		if strings.EqualFold(m, trimmed) {
			return pName
		}
	}

	// 3. 基于模型名称前缀智能推断
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "glm") {
		if _, exists := providers["glm"]; exists {
			return "glm"
		}
	}
	if strings.HasPrefix(lower, "minimax") {
		if _, exists := providers["minimax"]; exists {
			return "minimax"
		}
	}

	return ""
}
