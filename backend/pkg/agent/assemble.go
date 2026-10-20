// assemble.go 上下文组装分区核心：三档内容（blob 原文 / content 有界版 / stub
// 精简版）× 热冷两区（按 step 粒度）。
//
// 分区判定核心 zoneBackward 同时服务两个适配器：
//  1. 跨轮组装：buildContext 的历史行（读 messages 表治理列，冷区用 stub 列，
//     旧数据现场回退生成）；
//  2. 轮内 rezone：ReAct 循环预算超线时对内存 msgs 整体替换（同一套 stub 规则，
//     逐字节一致）。
//
// 判定规则（粘性边界，保证前缀稳定、缓存友好）：
//   - step = 一次上游调用（含 tool_calls 的 assistant 批次 + 其全部结果）；
//   - 从最新向最旧走：in-flight（最新 step）恒原文；热区双条件——按工具的
//     step 阈值 + 热区 token 预算（窗口 × HotBudgetRatio）——任一触顶后，
//     更旧条目全部进冷区；
//   - ctx_mode='stub' 条目无视分区恒冷（大参数/非法参数的原文永不进请求）；
//   - 叙事行（user / 纯文本 assistant / system / 悬空合成行）永远原文、不占预算。
package agent

import (
	"backend/pkg/ctxpolicy"
	"backend/pkg/provider"
)

// zoneItem 分区判定的统一条目（跨轮组装与轮内 rezone 的公共形状）
type zoneItem struct {
	role       string      // assistant | tool | other
	step       int         // 所属 step 序号（前向编号）；非 payload 为 -1
	calls      []savedCall // assistant 批次（阈值取各工具 max）
	toolName   string      // tool 行的工具名（阈值查表）
	tokensFull int32
	forceStub  bool
	isPayload  bool

	// applyCold 把该条目切换到冷区口径（stub 列 / 现场生成），由适配器实现
	applyCold func()
}

// hotThreshold 该条目的热区 step 阈值：assistant 批次取各调用工具的 max
// （同批含 read 与 write 时以 read 的宽阈值为准——stub 版里小参数本来原样，
// 宽阈值只影响 write 大载荷多保留一步，预算会兜底）；tool 行按自身工具。
func (it *zoneItem) hotThreshold() int {
	if it.role == "assistant" {
		maxStep := 0
		for _, c := range it.calls {
			if s := ctxpolicy.HotStepsFor(c.Function.Name); s > maxStep {
				maxStep = s
			}
		}
		if maxStep == 0 {
			return ctxpolicy.Current().DefaultHotSteps
		}
		return maxStep
	}
	if it.toolName != "" {
		return ctxpolicy.HotStepsFor(it.toolName)
	}
	return ctxpolicy.Current().DefaultHotSteps
}

// zoneBackward 就地分区：为每个 payload 条目决定热（原样）/冷（applyCold）。
// 返回热区 token 用量（观测/测试用）。粘性边界：触顶后更旧条目全部进冷区，
// 同样的条目序列 + 同样的策略 → 逐字节相同的结果（组装确定性）。
func zoneBackward(items []zoneItem, model string) int64 {
	// 前向编号：assistant 批次开启 step，其后 tool 行归属最近批次
	steps := 0
	lastStep := -1
	for i := range items {
		it := &items[i]
		switch {
		case it.isPayload && it.role == "assistant":
			it.step = steps
			lastStep = steps
			steps++
		case it.isPayload && it.role == "tool" && lastStep >= 0:
			it.step = lastStep
		default:
			it.step = -1
			it.isPayload = false // 叙事/悬空合成行不参与分区
		}
	}
	total := steps
	budget := int64(float64(provider.ContextWindow(model)) * ctxpolicy.Current().HotBudgetRatio)

	hotZone := true
	var used int64
	for i := len(items) - 1; i >= 0; i-- {
		it := &items[i]
		if !it.isPayload {
			continue
		}
		if it.forceStub {
			if it.applyCold != nil {
				it.applyCold()
			}
			continue
		}
		if total-1-it.step <= 0 {
			used += int64(it.tokensFull) // in-flight：恒原文（计入用量但不触发挤出）
			continue
		}
		if !hotZone || total-1-it.step >= it.hotThreshold() || used+int64(it.tokensFull) > budget {
			hotZone = false
			if it.applyCold != nil {
				it.applyCold()
			}
			continue
		}
		used += int64(it.tokensFull)
	}
	return used
}

// rezoneMsgs 轮内预算超线时对内存 msgs 整体分区替换（ReAct 循环 round 边界调用）。
// in-flight 最新 step 与叙事行不动；冷区用与落库同一套 stub 规则现场生成（幂等，
// 已是 stub 的内容原样保留）。批量一次、之后前缀只增不变（迟滞保缓存）。
func rezoneMsgs(msgs []provider.ChatMessage, model string) {
	// callID → 配对调用（tool 行冷区生成 stub 需要原参数）
	callByID := map[string]savedCall{}
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			if calls, ok := msgs[i].ToolCalls.([]savedCall); ok {
				for _, c := range calls {
					callByID[c.ID] = c
				}
			}
		}
	}

	items := make([]zoneItem, len(msgs))
	for i := range msgs {
		m := &msgs[i]
		it := &items[i]
		switch m.Role {
		case "assistant":
			calls, ok := m.ToolCalls.([]savedCall)
			if !ok || len(calls) == 0 {
				continue // 纯文本 assistant：叙事
			}
			if callsNeedStub(calls) {
				m.CtxMode = "stub"
			}
			it.role, it.isPayload, it.calls = "assistant", true, calls
			it.forceStub = m.CtxMode == "stub"
			it.tokensFull = estimateTokens(m.Content) + callsTokens(calls)
			idx := i
			stubbed := stubForToolCalls(calls)
			it.applyCold = func() { msgs[idx].ToolCalls = stubbed }
		case "tool":
			call, paired := callByID[m.ToolCallID]
			if !paired {
				continue // 悬空/合成行：按叙事处理
			}
			it.role, it.isPayload, it.toolName = "tool", true, call.Function.Name
			it.forceStub = m.CtxMode == "stub"
			it.tokensFull = estimateTokens(m.Content)
			idx := i
			name, args := call.Function.Name, call.Function.Arguments
			content := m.Content
			it.applyCold = func() {
				msgs[idx].Content = stubForToolResult(name, args, content)
			}
		}
	}
	zoneBackward(items, model)
}

// assistantContextMessage 构造内存回放副本；calls 与落库原文保持不变。
func assistantContextMessage(content string, calls []savedCall, extras *msgExtras) provider.ChatMessage {
	msg := provider.ChatMessage{Role: "assistant", Content: content, ToolCalls: callsOrNil(calls)}
	if extras != nil && extras.forceStub {
		msg.CtxMode = "stub"
		msg.ToolCalls = callsOrNil(stubForToolCalls(calls))
	}
	return msg
}

// callsTokens 批次参数体积估算
func callsTokens(calls []savedCall) int32 {
	n := 0
	for _, c := range calls {
		n += len(c.Function.Arguments)
	}
	return int32(n / 2)
}
