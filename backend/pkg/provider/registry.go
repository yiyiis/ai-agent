package provider

import (
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

	if pName, ok := modelRoutes[model]; ok {
		if p, exists := providers[pName]; exists {
			return p, true
		}
	}

	// 回退至首个已注册的 provider
	for _, p := range providers {
		return p, true
	}
	return nil, false
}
