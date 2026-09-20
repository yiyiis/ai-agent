package provider

import (
	"math"
	"strings"
	"sync"
)

var (
	pricesMu    sync.RWMutex
	modelPrices = make(map[string]ModelPrice) // 模型名 → 单价（启动时一次载入，运行期只读）
)

// SetModelPrices 载入计费单价表（main 启动时随 InitProviders 调用一次）
func SetModelPrices(prices []ModelPrice) {
	pricesMu.Lock()
	defer pricesMu.Unlock()
	modelPrices = make(map[string]ModelPrice, len(prices))
	for _, p := range prices {
		name := strings.TrimSpace(p.Model)
		if name != "" {
			modelPrices[name] = p
		}
	}
}

// lookupPrice 查模型单价：精确匹配 → 大小写不敏感（与 registry 匹配风格一致）
func lookupPrice(model string) (ModelPrice, bool) {
	pricesMu.RLock()
	defer pricesMu.RUnlock()

	trimmed := strings.TrimSpace(model)
	if p, ok := modelPrices[trimmed]; ok {
		return p, true
	}
	for name, p := range modelPrices {
		if strings.EqualFold(name, trimmed) {
			return p, true
		}
	}
	return ModelPrice{}, false
}

// Cost 估算一次聚合的成本（元）：cached ⊂ prompt 扣减后计价，reasoning ⊂ completion 不重复计；
// 未配置单价的模型返回 0。金额仅在查询时换算，不落库——调价不回改历史账单
func Cost(model string, prompt, cached, completion int64) float64 {
	price, ok := lookupPrice(model)
	if !ok {
		return 0
	}
	uncached := prompt - cached
	if uncached < 0 {
		uncached = 0
	}
	cachedPrice := price.CachedInput
	if cachedPrice <= 0 {
		cachedPrice = price.Input
	}
	cost := float64(uncached)/1e6*price.Input +
		float64(cached)/1e6*cachedPrice +
		float64(completion)/1e6*price.Output
	return math.Round(cost*1e6) / 1e6
}
