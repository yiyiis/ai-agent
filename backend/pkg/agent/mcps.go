// mcps.go 阶段八：turn 级 MCP 装配。
//
// 会话启用的 MCP Server（session_mcp_servers）在 turn 开始时读出 DB 行、
// 组装引擎定义并触发 schema 注入（mcp.SchemasFor：惰性拨号 + 工具发现，
// 未就绪的 server 本轮跳过）。以 DB 为现值源头逐 turn 重喂，广场侧增删改
// 无需通知引擎（定义指纹变化自动重连）。
package agent

import (
	"context"
	"log/slog"

	"backend/dao"
	"backend/pkg/errors"
	"backend/pkg/mcp"
)

// loadTurnMcps 读会话启用的 MCP Server 并组装引擎定义（任何失败降级为
// 无 MCP 工具，不阻塞对话——MCP 是增强能力而非对话前提）
func loadTurnMcps(ctx context.Context, sessionID string) []mcp.ServerDef {
	rows, err := dao.ListSessionMcps(ctx, sessionID)
	if err != nil {
		slog.Warn("加载会话启用 MCP 失败，本轮降级为无 MCP 工具", "session", sessionID, "err", errors.Stack(err))
		return nil
	}
	var defs []mcp.ServerDef
	for i := range rows {
		row := &rows[i].Server
		if !row.Enabled {
			continue // 停用：关联保留但工具不注入（重新启用后自然恢复）
		}
		def, err := dao.McpDefFromRow(row)
		if err != nil {
			slog.Warn("MCP 行定义解析失败，本轮跳过该 server", "session", sessionID, "server", row.Name, "err", err.Error())
			continue
		}
		defs = append(defs, def)
	}
	return defs
}
