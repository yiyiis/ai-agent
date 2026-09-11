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
