// Package mcp 实现 MCP（Model Context Protocol）客户端引擎（阶段八）。
//
// 纯手写协议客户端（零外部依赖，与 provider 层手写 OpenAI 兼容客户端同一
// 自研风格），双模兼容 2026-07-28 无状态规范与 2025-06-18/2025-11-25 存量
// 服务器；传输层支持 Streamable HTTP 与 stdio。只消费 Tools 原语，把远端
// 工具转成 OpenAI function calling 规格交给 ReAct 循环调度（README 5.4）。
//
// 引擎自包含：不依赖 tools / sandbox / dao，与 agent 的对接（schema 注入与
// 执行路由）在 agent 层完成。广场管理平面（mcp_servers 表 + /api/mcps）见
// specs/mcp/；本包门面只暴露连接管理与桥接能力。
package mcp

import (
	"context"
	"log/slog"
	"regexp"
)

var std = NewManager()

// nameRe 广场短名约束：消歧前缀必须落在 function 名字符集 [a-zA-Z0-9_-] 内
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidServerName 广场短名校验
func ValidServerName(s string) bool { return nameRe.MatchString(s) }

// Init 引擎初始化：目前配置段只有广场种子（经 DB 投影消费），这里做种子
// 名字合法性预检并保留与其它包 Init 的对称性
func Init(cfg Config) {
	for _, s := range cfg.Servers {
		if !ValidServerName(s.Name) {
			slog.Warn("MCP 种子名字不合法（要求 [a-z0-9-]，1-64 字符），该种子将被跳过", "name", s.Name)
		}
	}
}

// EnsureRegistered 登记会话启用的 server 定义（DB 行组装，定义变化自动重连）
func EnsureRegistered(defs []ServerDef) { std.EnsureRegistered(defs) }

// SchemasFor 会话启用 server 的 function schema 集合（未就绪的跳过本轮）
func SchemasFor(defs []ServerDef) []any { return std.SchemasFor(defs) }

// IsTool 是否为 MCP 消歧工具名（执行路由用）
func IsTool(name string) bool { return std.IsTool(name) }

// ExecuteTool 执行 MCP 工具调用，返回桥接后的模型可读文本
func ExecuteTool(ctx context.Context, qualified string, args map[string]any) Result {
	return std.ExecuteTool(ctx, qualified, args)
}

// RemoveServer 广场删除时回收连接
func RemoveServer(id int64) { std.Remove(id) }

// TestServer 测试连接（广场"测试连接"按钮）
func TestServer(ctx context.Context, def ServerDef) TestInfo { return std.Test(ctx, def) }

// ToolsOfServer 读取工具清单（广场详情）
func ToolsOfServer(ctx context.Context, def ServerDef) ([]ToolInfo, error) {
	return std.ToolsOf(ctx, def)
}
