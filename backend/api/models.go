package api

import (
	"context"

	"backend/config"
)

type ListModelsReq struct{}

type ModelItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ListModelsResp struct {
	Models []ModelItem `json:"models"`
}

func ListModels(ctx context.Context, req *ListModelsReq) (*ListModelsResp, error) {
	defaultModel := config.GetConfig().LLM.DefaultModel
	if defaultModel == "" {
		defaultModel = "MiniMax-Text-01"
	}

	models := []ModelItem{
		{ID: defaultModel, Name: defaultModel},
		{ID: "MiniMax-Text-01", Name: "MiniMax-Text-01"},
		{ID: "doubao-pro-32k", Name: "Doubao-pro-32k"},
		{ID: "deepseek-chat", Name: "DeepSeek-V3"},
		{ID: "deepseek-reasoner", Name: "DeepSeek-R1 (Reasoning)"},
	}

	return &ListModelsResp{
		Models: models,
	}, nil
}
