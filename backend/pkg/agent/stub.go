// stub.go 写时生成精简版（stub）的规则函数族（三档内容中的第三档）。
//
// 同一批函数在三个消费点使用，保证逐字节一致：
//  1. 落库时生成 stub_content / stub_tool_calls（agent.go 写路径）；
//  2. 组装时对 stub 列为 NULL 的旧数据现场回退生成（assemble.go）；
//  3. ReAct 循环 rezone 时对内存 msgs 整体替换（agent.go 循环边界）。
//
// 规则要点：按工具类型生成、不按大小（write/edit 换骨架参数，read/bash 生成
// 摘要行）；错误结果与天然短小的内容 stub=原文（落库时写 NULL，组装直接用
// content）；所有函数幂等——对已是 stub 的输入原样返回。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"backend/pkg/sandbox"
	"backend/pkg/tools"
)

// stubNote 精简版参数的统一说明（模型可据此自行取回原文）
const stubNote = "内容已省略，可用 read_file 查看"

// estimateTokens 粗估 token：字符数/2。中文 token 密度高时偏高估，
// 组装预算宁可早裁——不追求精确，只求确定性可比。
func estimateTokens(parts ...string) int32 {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	return int32(n / 2)
}

// stubForToolCalls assistant 批次的精简版：结构不变（id/type/name 原样），
// 仅替换承载大载荷工具（write/edit）的 arguments；小参数工具原样保留。
func stubForToolCalls(calls []savedCall) []savedCall {
	out := make([]savedCall, len(calls))
	copy(out, calls)
	for i := range out {
		out[i].Function.Arguments = stubArguments(out[i].Function.Name, out[i].Function.Arguments)
	}
	return out
}

// stubArguments 单个调用的参数精简（幂等：带 _pruned/_rejected 标记的跳过）
func stubArguments(name, raw string) string {
	args, errMsg := tools.ParseArguments(raw)
	if errMsg != "" {
		return raw // 解析失败（含半截 JSON）原样返回，超限场景由 stubRejectedCalls 处理
	}
	if _, rejected := args["_rejected"]; rejected {
		return raw
	}
	switch name {
	case "write_file":
		if _, pruned := args["_pruned"]; pruned {
			return raw
		}
		content, _ := args["content"].(string)
		return marshalStub(map[string]any{
			"path":    argString(args, "path"),
			"bytes":   len(content),
			"lines":   strings.Count(content, "\n") + 1,
			"_pruned": true,
			"note":    stubNote,
		})
	case "edit_file":
		if _, pruned := args["_pruned"]; pruned {
			return raw
		}
		return marshalStub(map[string]any{
			"path":    argString(args, "path"),
			"_pruned": true,
			"note":    "改参已省略",
		})
	}
	return raw // read/bash 等：参数本来就小
}

// rejectStubArgs 超限被拒 / 截断半截 JSON 的单个调用：arguments 换成 stub 版。
// 原文由调用方整体落 blob；执行仍用原文，让 ParseArguments 产生带出路的自纠正提示。
func rejectStubArgs(raw string) string {
	stub := map[string]any{
		"_rejected": true,
		"note":      fmt.Sprintf("原始参数 %d 字节，超限未执行；请拆成多次小段写入（每次一小段）", len(raw)),
	}
	// 直接 Unmarshal 探测 path：超限参数过不了 ParseArguments 的长度闸门，
	// 但合法超限 JSON 的 path 仍应保留（模型与折叠键都靠它定位目标）
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err == nil {
		if p, _ := probe["path"].(string); p != "" {
			stub["path"] = p
		}
	}
	return marshalStub(stub)
}

// validCallArgs 参数是否为可回放的合法 JSON（半截 JSON 会在回放时炸上游）
func validCallArgs(raw string) bool {
	_, errMsg := tools.ParseArguments(raw)
	return errMsg == ""
}

// prepareAssistantCalls 决定 assistant 批次落库/进上下文的版本：流中断、
// finish=length 或参数超限（>40KB）时，半截 JSON / 超限的调用换 _rejected stub
// ——原文自此不进任何后续请求（由调用方落 blob 审计）。逐调用判定，同批次
// 正常调用不受牵连。执行仍用原始 calls：ParseArguments 对超限参数产生带出路的
// 自纠正提示，而非缺参误导。
func prepareAssistantCalls(calls []savedCall, streamInterrupted, finishLength bool) ([]savedCall, bool) {
	if len(calls) == 0 || !(streamInterrupted || finishLength || hasOversizedArgs(calls)) {
		return calls, false
	}
	stubbed := make([]savedCall, len(calls))
	copy(stubbed, calls)
	hit := false
	for i := range stubbed {
		raw := stubbed[i].Function.Arguments
		if tools.ArgsOverLimit(raw) || !validCallArgs(raw) {
			stubbed[i].Function.Arguments = rejectStubArgs(raw)
			hit = true
		}
	}
	if !hit {
		return calls, false
	}
	return stubbed, true
}

// stubForToolResult tool 结果的精简版（按工具类型）。错误结果与天然短小的
// 返回原文——调用方据此写 NULL，组装时直接用 content。
func stubForToolResult(callName, callArgs, content string) string {
	if tools.IsErrorResult(content) {
		return content // 失败是证据，保留全文
	}
	if strings.Contains(content, "已省略") {
		return content // 幂等：已是 stub
	}
	args, _ := tools.ParseArguments(callArgs)
	switch callName {
	case "read_file":
		offset := 1
		if args != nil {
			if v, ok := args["offset"].(float64); ok && int(v) > 1 {
				offset = int(v)
			}
		}
		p := ""
		if args != nil {
			p = argString(args, "path")
		}
		return fmt.Sprintf("[read_file %s L%d 起，约 %dKB；结果已省略，需要时重新 read_file]",
			p, offset, kbLen(content))
	case "bash", "python_exec":
		cmd := callArgs
		if args != nil {
			if c := argString(args, "command"); c != "" {
				cmd = c
			} else if c := argString(args, "code"); c != "" {
				cmd = c
			}
		}
		if r := []rune(cmd); len(r) > 200 {
			cmd = string(r[:200]) + "…"
		}
		return fmt.Sprintf("[%s] %s\n%s", callName, cmd, headTailLines(content, 3))
	case "write_file", "edit_file", "export_artifact":
		return content // 成功反馈本来就短
	}
	if len(content) > 512 {
		return fmt.Sprintf("[%s 结果已省略（约 %dKB），需要时重新调用]", callName, kbLen(content))
	}
	return content
}

// headTailLines 头尾各 n 行的摘要（结果进场时有界，行切片代价可忽略）
func headTailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= 2*n {
		return s
	}
	head := strings.Join(lines[:n], "\n")
	tail := strings.Join(lines[len(lines)-n:], "\n")
	return head + fmt.Sprintf("\n…（中间省略 %d 行）…\n", len(lines)-2*n) + tail
}

// callTarget 折叠键提取（写时落 tool_target/tool_op 列，冷区折叠用）：
// 文件类工具取 path；bash/python/load_skill 不参与折叠。同批次多个调用
// 目标不一致时返回空——保守不折，宁可少合并不可误合并。
func callTarget(calls []savedCall) (target, op string) {
	ops := map[string]string{
		"write_file":     "write",
		"edit_file":      "edit",
		"read_file":      "read",
		"export_artifact": "export",
	}
	for _, c := range calls {
		o, ok := ops[c.Function.Name]
		if !ok {
			return "", ""
		}
		args, errMsg := tools.ParseArguments(c.Function.Arguments)
		if errMsg != "" {
			return "", ""
		}
		p := argString(args, "path")
		if p == "" {
			return "", ""
		}
		if target == "" {
			target, op = p, o
			continue
		}
		if p != target {
			return "", ""
		}
	}
	return target, op
}

func marshalStub(stub map[string]any) string {
	b, err := json.Marshal(stub)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func kbLen(s string) int { return (len(s) + 1023) / 1024 }

// argString 从已解析的参数里取字符串（agent 包内避免与 tools 包私有函数重名）
func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if s, ok := args[key].(string); ok {
		return s
	}
	return ""
}

// ---------------------------------------------------------------------------
// 落库装配：把 stub 规则的结果填进 messages 表的上下文治理列
// ---------------------------------------------------------------------------

// msgExtras 上下文治理列的装配载体（nil=全部默认值，兼容旧行为）
type msgExtras struct {
	stubContent   string // stub_content；空=不写（组装按"stub=content"规则处理）
	stubToolCalls string // stub_tool_calls JSON；空=不写
	tokensFull    int32  // tokens_full
	stubTokens    int32  // stub_tokens
	blobRef       string // blob_ref
	forceStub     bool   // ctx_mode='stub'（恒用精简版）
	toolTarget    string // tool_target（冷区折叠键）
	toolOp        string // tool_op
}

// hasOversizedArgs 批次里是否有超限参数（40KB，与 tools.ParseArguments 同一把尺）
func hasOversizedArgs(calls []savedCall) bool {
	for _, c := range calls {
		if tools.ArgsOverLimit(c.Function.Arguments) {
			return true
		}
	}
	return false
}

// writeRejectedBlob 超限/截断原文落工作区 blob（审计取证，配合 logArgAnomaly），
// 失败仅告警不阻塞对话
func writeRejectedBlob(ctx context.Context, sessionID, data string) string {
	if data == "" {
		return ""
	}
	name := fmt.Sprintf(".tool-outputs/rejected-%s-%s.json", time.Now().Format("0102-150405"), sigHash(data))
	if err := sandbox.WriteFile(ctx, sessionID, name, []byte(data)); err != nil {
		slog.Warn("超限参数原文落盘失败", "session", sessionID, "err", err)
		return ""
	}
	return name
}

// assistantExtras assistant 行的治理列装配（stub 版工具调用、token 估算、折叠键）
func assistantExtras(ctx context.Context, sessionID, content string, persistCalls, origCalls []savedCall, rejected bool) *msgExtras {
	ex := &msgExtras{tokensFull: estimateTokens(content)}
	if len(persistCalls) > 0 {
		if b, err := json.Marshal(persistCalls); err == nil {
			ex.tokensFull = estimateTokens(content, string(b))
		}
		stub := stubForToolCalls(persistCalls)
		if b, err := json.Marshal(stub); err == nil {
			ex.stubToolCalls = string(b)
			ex.stubTokens = estimateTokens(content, ex.stubToolCalls)
		} else {
			ex.stubTokens = ex.tokensFull
		}
		ex.toolTarget, ex.toolOp = callTarget(persistCalls)
	} else {
		ex.stubTokens = ex.tokensFull
	}
	if rejected {
		ex.forceStub = true
		if b, err := json.Marshal(origCalls); err == nil {
			ex.blobRef = writeRejectedBlob(ctx, sessionID, string(b))
		}
	}
	return ex
}

// toolExtras tool 行的治理列装配（结果 stub、blob 引用、token、折叠键）
func toolExtras(tc savedCall, out tools.Output) *msgExtras {
	stub := stubForToolResult(tc.Function.Name, tc.Function.Arguments, out.Text)
	ex := &msgExtras{
		tokensFull: estimateTokens(out.Text),
		blobRef:    out.BlobRef,
	}
	if stub != out.Text {
		ex.stubContent = stub
		ex.stubTokens = estimateTokens(stub)
	} else {
		ex.stubTokens = ex.tokensFull // stub=content：列写 NULL，组装直接用 content
	}
	ex.toolTarget, ex.toolOp = callTarget([]savedCall{tc})
	return ex
}
