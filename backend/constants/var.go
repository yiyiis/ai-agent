package constants

type UserType int32

const (
	SuperManager UserType = 1 // 超级管理员
	Manager      UserType = 2 // 管理员
	User         UserType = 3 // 普通用户
)

// TokenUsagePurpose Token 消耗用途(token_usages.purpose,计费归因的一级维度)
type TokenUsagePurpose string

const (
	UsagePurposeTurn    TokenUsagePurpose = "turn"    // 对话/任务推理(交互聊天与开放 API 无头任务共用)
	UsagePurposeCompact TokenUsagePurpose = "compact" // 历史压缩摘要生成(阶段七模块二预留)
	UsagePurposeExtract TokenUsagePurpose = "extract" // 长期记忆后台抽取(阶段七模块三预留)
)
