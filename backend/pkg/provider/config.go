package provider

import "fmt"

// Config LLM 配置段（包自持配置、由 config 包组合，yaml 键与 etc/config.yaml 对齐）
type Config struct {
	DefaultModel string           `yaml:"DefaultModel" mapstructure:"DefaultModel"`
	Providers    []ProviderConfig `yaml:"Providers" mapstructure:"Providers"`
	// Billing 计费单价表（每百万 token，元）：金额仅在查询时估算，不落库，
	// 未配置的模型金额记 0、token 计数照常统计
	Billing []ModelPrice `yaml:"Billing" mapstructure:"Billing"`
}

// ProviderConfig 单个模型服务接入点
type ProviderConfig struct {
	Name    string   `yaml:"Name" mapstructure:"Name"`
	BaseURL string   `yaml:"BaseURL" mapstructure:"BaseURL"`
	APIKey  string   `yaml:"ApiKey" mapstructure:"ApiKey"`
	Models  []string `yaml:"Models" mapstructure:"Models"`
}

// ModelPrice 单模型计费单价（每百万 token，元）
type ModelPrice struct {
	Model       string  `yaml:"Model" mapstructure:"Model"`
	Input       float64 `yaml:"Input" mapstructure:"Input"`             // 未命中缓存的输入单价
	Output      float64 `yaml:"Output" mapstructure:"Output"`           // 输出单价（reasoning 含在输出内，不另计）
	CachedInput float64 `yaml:"CachedInput" mapstructure:"CachedInput"` // 缓存命中输入单价（缺省按 Input 计）
}

// InitProviders 按配置批量注册 Provider 与模型路由（main 启动时调用一次）
func InitProviders(conf Config) {
	SetModelPrices(conf.Billing)
	for _, p := range conf.Providers {
		RegisterProvider(p.Name, NewOpenAICompatProvider(p.BaseURL, p.APIKey))
		for _, m := range p.Models {
			RegisterModelRoute(m, p.Name)
		}
		fmt.Printf("[INFO] 成功注册模型 Provider: %s (BaseURL: %s, 模型数: %d)\n", p.Name, p.BaseURL, len(p.Models))
	}
}
