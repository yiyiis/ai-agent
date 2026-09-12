package api

import (
	"context"

	"backend/config"
)

type ModelItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ListModelsReq struct{}

// ListModels 返回支持的模型矩阵列表 (GET /api/models)
func ListModels(ctx context.Context, _ *ListModelsReq) (*[]ModelItem, error) {
	conf := config.GetConfig()
	defaultModel := conf.LLM.DefaultModel

	models := make([]ModelItem, 0)
	seen := make(map[string]bool)

	// 1. 如果配置了默认模型，优先置顶
	if defaultModel != "" {
		models = append(models, ModelItem{ID: defaultModel, Name: defaultModel})
		seen[defaultModel] = true
	}

	// 2. 从配置的各 Provider 中载入模型
	for _, p := range conf.LLM.Providers {
		for _, m := range p.Models {
			if m != "" && !seen[m] {
				models = append(models, ModelItem{ID: m, Name: m})
				seen[m] = true
			}
		}
	}

	// 3. 兜底保障：若未配置则提供 MiniMax 与 GLM 默认模型
	if len(models) == 0 {
		defaults := []string{
			"MiniMax-Text-01",
			"MiniMax-M3",
			"GLM-5.3",
			"GLM-5.3-Flash",
			"GLM-5.2",
			"GLM-5.1",
			"GLM-5-Turbo",
			"GLM-4.7",
		}
		for _, m := range defaults {
			if !seen[m] {
				models = append(models, ModelItem{ID: m, Name: m})
				seen[m] = true
			}
		}
	}

	return &models, nil
}
