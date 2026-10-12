// bridge.go MCP ↔ Function Calling 桥接。
//
// 工具定义转 OpenAI function schema（inputSchema 即 JSON Schema，与 function
// calling 的 parameters 同构，原样透传）；tools/call 结果转模型可读文本，
// 按规范双轨：isError=true 是工具执行错误（回喂模型自纠正），JSON-RPC error
// 是协议层错误（模型难以自修复）。文本超长做头尾截断。
package mcp

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	qualifiedPrefix = "mcp_"
	maxFuncNameLen  = 64 // OpenAI function 名长度上限
	maxResultBytes  = 32 * 1024
	resultTailKeep  = 2048
	// DefaultTTL 工具列表默认刷新间隔（服务器 ttlMs 更短则尊重服务器）
	DefaultTTL = 5 * time.Minute
)

// Result MCP 工具执行结果的桥接产物（agent 层包成 tools.Output 喂模型）
type Result struct {
	Text string
}

// sanitizeToolName 工具名归一：仅保留 [A-Za-z0-9_-]，其余（含 MCP 允许的 .）换 _
func sanitizeToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "tool"
	}
	return out
}

// QualifiedName 消歧函数名：mcp_<server>_<tool>。规范明确多服务器聚合必须
// 消歧且 serverInfo.name 不保证唯一——本项目以广场注册名（约束 [a-z0-9-]）
// 为前缀；超 64 字符截断并追加 md5 短哈希后缀防撞
func QualifiedName(server, tool string) string {
	q := qualifiedPrefix + server + "_" + sanitizeToolName(tool)
	if len(q) <= maxFuncNameLen {
		return q
	}
	sum := md5.Sum([]byte(q))
	return q[:maxFuncNameLen-7] + "_" + hex.EncodeToString(sum[:])[:6]
}

// ToolToSchema MCP 工具 → OpenAI function calling schema
func ToolToSchema(server string, t Tool) map[string]any {
	params := any(map[string]any{"type": "object"})
	if len(t.InputSchema) > 0 && json.Valid(t.InputSchema) {
		params = t.InputSchema // json.RawMessage 原样透传（序列化时即内联 JSON）
	}
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = strings.TrimSpace(t.Title)
	}
	if desc == "" {
		desc = "（MCP 服务器未提供该工具的描述）"
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        QualifiedName(server, t.Name),
			"description": "[MCP server:" + server + "] " + desc,
			"parameters":  params,
		},
	}
}

// BridgeCallResult tools/call 结果 → 模型可读文本
func BridgeCallResult(res *CallToolResult) string {
	if res.ResultType == "input_required" {
		// MRTR（2026-07-28）：服务器要求额外交互输入；v1 不支持人机交互，
		// 给模型可行动的退路文案（未来接阶段八 request_input 挂起机制）
		return "[MCP 工具等待输入] 该工具需要额外的交互输入（如登录授权、表单确认）才能继续，" +
			"当前系统暂不支持在对话中完成该交互。请不要原样重试，改用其他工具完成，" +
			"或向用户说明需要在其原生界面完成该操作。"
	}

	var parts []string
	var skipped []string
	for _, blk := range res.Content {
		if blk.Type == "text" {
			parts = append(parts, blk.Text)
			continue
		}
		// image / audio / resource / resource_link 等：v1 不做多模态回传
		name := blk.Type
		if name == "" {
			name = "unknown"
		}
		skipped = append(skipped, name)
	}
	if len(skipped) > 0 {
		parts = append(parts, fmt.Sprintf("[收到 %d 个非文本内容块（%s），当前版本暂不展示]",
			len(skipped), strings.Join(skipped, "、")))
	}
	if len(res.StructuredContent) > 0 {
		// 规范建议服务器同时给 text 兜底，这里双保险直接序列化追加
		parts = append(parts, string(res.StructuredContent))
	}

	text := strings.Join(parts, "\n")
	if res.IsError {
		// 工具执行错误：回喂模型自纠正（规范 SHOULD）
		return "[MCP 工具执行错误] " + truncateResult(text)
	}
	return truncateResult(text)
}

// BridgeProtocolError JSON-RPC 协议错误文案（模型难以自修复）
func BridgeProtocolError(err error) string {
	return "[MCP 调用失败] " + err.Error() +
		"（协议层错误，原样重试通常无效；若提示工具不存在，说明服务器工具清单已更新）"
}

// truncateResult 超长结果头尾截断（保头部为主、留尾部上下文，中段省略）
func truncateResult(s string) string {
	if len(s) <= maxResultBytes {
		return s
	}
	b := []byte(s)
	head := b[:maxResultBytes-resultTailKeep-96]
	tail := b[len(b)-resultTailKeep:]
	return string(head) +
		fmt.Sprintf("\n\n…[MCP] 结果超长已截断（原文 %d 字节，此处省略中段）…\n\n", len(b)) +
		string(tail)
}

// filterTools 白名单过滤（空白名单=全放行）
func filterTools(tools []Tool, allow []string) []Tool {
	if len(allow) == 0 {
		return tools
	}
	set := make(map[string]struct{}, len(allow))
	for _, a := range allow {
		set[a] = struct{}{}
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if _, ok := set[t.Name]; ok {
			out = append(out, t)
		}
	}
	return out
}
