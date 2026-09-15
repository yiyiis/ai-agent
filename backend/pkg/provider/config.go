package provider

import "fmt"

// Config LLM 配置段（包自持配置、由 config 包组合，yaml 键与 etc/config.yaml 对齐）
type Config struct {
	DefaultModel string           `yaml:"DefaultModel" mapstructure:"DefaultModel"`
	Providers    []ProviderConfig `yaml:"Providers" mapstructure:"Providers"`
}

// ProviderConfig 单个模型服务接入点
type ProviderConfig struct {
	Name    string   `yaml:"Name" mapstructure:"Name"`
	BaseURL string   `yaml:"BaseURL" mapstructure:"BaseURL"`
	APIKey  string   `yaml:"ApiKey" mapstructure:"ApiKey"`
	Models  []string `yaml:"Models" mapstructure:"Models"`
}

// InitProviders 按配置批量注册 Provider 与模型路由（main 启动时调用一次）
func InitProviders(conf Config) {
	for _, p := range conf.Providers {
		RegisterProvider(p.Name, NewOpenAICompatProvider(p.BaseURL, p.APIKey))
		for _, m := range p.Models {
			RegisterModelRoute(m, p.Name)
		}
		fmt.Printf("[INFO] 成功注册模型 Provider: %s (BaseURL: %s, 模型数: %d)\n", p.Name, p.BaseURL, len(p.Models))
	}
}
