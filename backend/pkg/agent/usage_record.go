package agent

import (
	"context"
	"log/slog"

	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/provider"
)

// recordTokenUsage 记账是旁路：上游没回 usage 或全零不写空行；落库失败仅告警，
// 绝不打断对话（与 saveMessage 同款 WithoutCancel 模式，断连/中止仍落账）。
// 全系统 token_usages 写入的唯一入口，业务代码不得绕过直接写账。
func recordTokenUsage(ctx context.Context, session *model.Session, round int, usage *provider.Usage, purpose constants.TokenUsagePurpose) {
	if usage == nil || (usage.PromptTokens == 0 && usage.CompletionTokens == 0) {
		return
	}
	row := &model.TokenUsage{
		SessionID:        session.SessionID, // 交互聊天为会话 UUID；无头任务为 run_<run_id>，可 join 回 skill_runs
		CompanyID:        session.CompanyID, // 冗余归属，每行自解释
		UserID:           session.UserID,
		Model:            session.Model,
		ProviderKey:      provider.ProviderKeyForModel(session.Model), // 未匹配记空串，账不能因模型下线而丢
		RoundIndex:       int64(round),
		Purpose:          string(purpose),
		PromptTokens:     int64(usage.PromptTokens),     // cached 为其子集
		CompletionTokens: int64(usage.CompletionTokens), // reasoning 为其子集
		CachedTokens:     int64(usage.CachedTokens),
		ReasoningTokens:  int64(usage.ReasoningTokens),
	}
	if err := dao.CreateTokenUsage(context.WithoutCancel(ctx), row); err != nil {
		slog.Warn("记录 Token 用量失败（不影响对话）",
			"session_id", session.SessionID, "model", session.Model, "round", round, "err", err)
	}
}

// sumUsage 整轮累计（done 事件携带各轮求和后的真实整轮消耗）。
// ContextTokens 是水位读数而非计数器：取最近一次真实值，不累计
func sumUsage(acc, cur *provider.Usage) *provider.Usage {
	if cur == nil {
		return acc
	}
	if acc == nil {
		clone := *cur
		return &clone
	}
	acc.PromptTokens += cur.PromptTokens
	acc.CompletionTokens += cur.CompletionTokens
	acc.CachedTokens += cur.CachedTokens
	acc.ReasoningTokens += cur.ReasoningTokens
	acc.ContextTokens = cur.ContextTokens
	return acc
}
