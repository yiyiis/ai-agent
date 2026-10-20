// stub.go 写时生成精简版（stub）的规则函数族（三档内容中的第三档）。
//
// 同一批函数在三个消费点使用，保证逐字节一致：
//  1. 落库时生成 stub_content / stub_tool_calls（agent.go 写路径）；
//  2. 组装时对 stub 列为 NULL 的旧数据现场回退生成（assemble.go）；
//  3. ReAct 循环 rezone 时对内存 msgs 整体替换（agent.go 循环边界）。
//
// 规则要点：write/edit 换骨架参数，其余工具的大参数用通用摘要；非法参数只在
// 回放副本中标记为 _rejected，原文始终落库。错误结果与天然短小的结果不重复
// 存 stub；所有函数幂等，对已是 stub 的输入原样返回。
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"backend/pkg/sandbox"
	"backend/pkg/storage"
	"backend/pkg/tools"
)

// stubNote 精简版参数的统一说明（模型可据此自行取回原文）
const stubNote = "内容已省略，可用 read_file 查看"

// estimateTokens 粗估 token：字节数/2。中文 token 密度高时偏高估，
// 组装预算宁可早裁——不追求精确，只求确定性可比。
func estimateTokens(parts ...string) int32 {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	return int32(n / 2)
}

// stubForToolCalls 仅构造回放副本；落库和执行始终使用原始 calls。
// id/type/name 不变，write/edit 精简载荷，其他工具（含 MCP）的大参数走通用摘要。
func stubForToolCalls(calls []savedCall) []savedCall {
	out := make([]savedCall, len(calls))
	copy(out, calls)
	source := ""
	if callsNeedStub(calls) {
		if data, err := json.Marshal(calls); err == nil {
			source = callBlobPath(string(data))
		}
	}
	for i := range out {
		raw := out[i].Function.Arguments
		stub := stubArguments(out[i].Function.Name, raw)
		if source != "" && stub != raw {
			var args map[string]any
			if json.Unmarshal([]byte(stub), &args) == nil {
				args["_source"] = source
				stub = marshalStub(args)
			}
		}
		out[i].Function.Arguments = stub
	}
	return out
}

// stubArguments 单个调用的参数精简；非法 JSON 只在回放副本中标记为 _rejected。
func stubArguments(name, raw string) string {
	args, errMsg := tools.ParseArguments(raw)
	if errMsg != "" {
		return rejectStubArgs(raw)
	}
	// 原始 MCP 参数也可能有同名字段；大参数不能凭这些字段绕过精简。
	if !tools.ArgsNeedStub(raw) {
		if pruned, _ := args["_pruned"].(bool); pruned {
			return raw
		}
		if rejected, _ := args["_rejected"].(bool); rejected {
			return raw
		}
	}
	switch name {
	case "write_file":
		content, _ := args["content"].(string)
		return marshalStub(map[string]any{
			"path":    argumentPath(args),
			"bytes":   len(content),
			"lines":   strings.Count(content, "\n") + 1,
			"_pruned": true,
			"note":    stubNote,
		})
	case "edit_file":
		return marshalStub(map[string]any{
			"path":    argumentPath(args),
			"_pruned": true,
			"note":    "改参已省略",
		})
	}
	if tools.ArgsNeedStub(raw) {
		return genericArgumentStub(args, len(raw))
	}
	return raw
}

func argumentPath(args map[string]any) string {
	runes := []rune(argString(args, "path"))
	if len(runes) > 1024 {
		return string(runes[:1024]) + "…"
	}
	return string(runes)
}

// genericArgumentStub 不猜测 MCP/未知工具的参数契约，只记录有界的字段名、类型和体积。
// 同时限制字段数量与名称长度，避免大型数组/嵌套对象/大量字段把 stub 再次撑大。
func genericArgumentStub(args map[string]any, size int) string {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]map[string]any, 0, min(len(keys), 16))
	for _, key := range keys[:min(len(keys), 16)] {
		name := []rune(key)
		if len(name) > 64 {
			name = append(name[:64], '…')
		}
		field := map[string]any{"name": string(name)}
		switch value := args[key].(type) {
		case string:
			field["type"], field["bytes"] = "string", len(value)
		case []any:
			field["type"], field["items"] = "array", len(value)
		case map[string]any:
			field["type"], field["fields"] = "object", len(value)
		case json.Number, float64:
			field["type"] = "number"
		case bool:
			field["type"] = "boolean"
		default:
			field["type"] = "null"
		}
		fields = append(fields, field)
	}
	return marshalStub(map[string]any{
		"_pruned":     true,
		"bytes":       size,
		"field_count": len(keys),
		"fields":      fields,
		"note":        "历史调用参数已省略，完整原文保存在会话记录，可用 read_file 查看 _source 参数归档；此摘要仅用于回放，不能作为执行参数",
	})
}

// rejectStubArgs 仅用于非法/不完整参数的回放。原文仍保存在 tool_calls，执行时按原文校验。
func rejectStubArgs(raw string) string {
	stub := map[string]any{
		"_rejected": true,
		"note":      fmt.Sprintf("原始参数 %d 字节，JSON 非法或不完整，未执行；原文已保留，请重新输出完整 JSON 对象", len(raw)),
	}
	// JSON 类型不符合参数契约时，仍尽可能保留有界的 path 定位信息。
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err == nil {
		if p := argumentPath(probe); p != "" {
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

// callsNeedStub 大参数与非法参数都强制精简回放，但仅非法参数阻止执行。
func callsNeedStub(calls []savedCall) bool {
	for _, call := range calls {
		if tools.ArgsNeedStub(call.Function.Arguments) || !validCallArgs(call.Function.Arguments) {
			return true
		}
	}
	return false
}

// isResultStub 内容是否已是结果 stub——幂等守卫用精确的固定前缀/标记判定，
// 不能用"已省略"模糊匹配：工具层预览自带的"…[中间省略 N 字符]…"也含这四个字，
// 会把超限 bash 的 stub 生成误拦成"已是 stub"（v1.0.0 已修复的缺陷）。
func isResultStub(s string) bool {
	return strings.HasPrefix(s, "[read_file ") ||
		strings.HasPrefix(s, "[bash] ") ||
		strings.HasPrefix(s, "[python_exec] ") ||
		strings.Contains(s, "结果已省略")
}

// stubForToolResult tool 结果的精简版（按工具类型）。错误结果与天然短小的
// 返回原文——调用方据此写 NULL，组装时直接用 content。
func stubForToolResult(callName, callArgs, content string) string {
	if tools.IsErrorResult(content) {
		return content // 失败是证据，保留全文
	}
	if isResultStub(content) {
		return content // 幂等：已是 stub
	}
	args, _ := tools.ParseArguments(callArgs)
	switch callName {
	case "read_file":
		offset := 1
		if args != nil {
			if v, ok := args["offset"].(json.Number); ok {
				if n, err := v.Float64(); err == nil && int(n) > 1 {
					offset = int(n)
				}
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
		"write_file":      "write",
		"edit_file":       "edit",
		"read_file":       "read",
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

// blobRef 持久化定位符：沙箱路径（模型侧身份）+ 对象存储归档 URL
// （沙箱回收后重物化与前端查看全文的凭据）。
type blobRef struct {
	Path string `json:"path"`
	URL  string `json:"url,omitempty"`
}

func encodeBlobRef(path, url string) string {
	b, err := json.Marshal(blobRef{Path: path, URL: url})
	if err != nil {
		return path
	}
	return string(b)
}

// decodeBlobRef 兼容 v1.0.0 的裸路径行（无 URL：沙箱回收后无从恢复，只能用现状）
func decodeBlobRef(s string) (path, url string) {
	var r blobRef
	if json.Unmarshal([]byte(s), &r) == nil && r.Path != "" {
		return r.Path, r.URL
	}
	return s, ""
}

// writeCallBlob 大参数/非法参数原文双写：工作区 .tool-outputs/（当场可查）
// + 对象存储归档（沙箱回收后凭 URL 恢复），两种失败均降级（warn 不阻塞对话）
func writeCallBlob(ctx context.Context, sessionID, data string) (string, string) {
	if data == "" {
		return "", ""
	}
	name := callBlobPath(data)
	base := strings.TrimPrefix(name, ".tool-outputs/")
	ref := ""
	if err := sandbox.WriteFile(ctx, sessionID, name, []byte(data)); err != nil {
		slog.Warn("工具参数原文落盘失败", "session", sessionID, "err", err)
	} else {
		ref = name
	}
	archiveURL := ""
	if obj, err := storage.PutArchive(ctx, []byte(data), base, "application/json"); err != nil {
		slog.Warn("工具参数原文归档失败", "session", sessionID, "err", err)
	} else {
		archiveURL = obj.URL
	}
	return ref, archiveURL
}

// 确定性路径让落库、历史回放和轮内重建的 stub 都能指向同一份完整参数。
func callBlobPath(data string) string {
	return fmt.Sprintf(".tool-outputs/arguments-%x.json", sha256.Sum256([]byte(data)))
}

// assistantExtras assistant 行的治理列装配（stub 版工具调用、token 估算、折叠键）
func assistantExtras(ctx context.Context, sessionID, content string, calls []savedCall) *msgExtras {
	ex := &msgExtras{tokensFull: estimateTokens(content), forceStub: callsNeedStub(calls)}
	if len(calls) > 0 {
		if b, err := json.Marshal(calls); err == nil {
			ex.tokensFull = estimateTokens(content, string(b))
		}
		stub := stubForToolCalls(calls)
		if b, err := json.Marshal(stub); err == nil {
			ex.stubToolCalls = string(b)
			ex.stubTokens = estimateTokens(content, ex.stubToolCalls)
		} else {
			ex.stubTokens = ex.tokensFull
		}
		ex.toolTarget, ex.toolOp = callTarget(calls)
	} else {
		ex.stubTokens = ex.tokensFull
	}
	if ex.forceStub {
		if b, err := json.Marshal(calls); err == nil {
			path, url := writeCallBlob(ctx, sessionID, string(b))
			if path != "" || url != "" {
				ex.blobRef = encodeBlobRef(path, url)
			}
		}
	}
	return ex
}

// toolExtras tool 行的治理列装配（结果 stub、blob 引用、token、折叠键）
func toolExtras(tc savedCall, out tools.Output) *msgExtras {
	stub := stubForToolResult(tc.Function.Name, tc.Function.Arguments, out.Text)
	ex := &msgExtras{
		tokensFull: estimateTokens(out.Text),
	}
	if out.BlobRef != "" || out.ArchiveURL != "" {
		ex.blobRef = encodeBlobRef(out.BlobRef, out.ArchiveURL)
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
