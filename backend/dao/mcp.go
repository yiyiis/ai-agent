// mcp.go MCP 广场注册表与会话 MCP 关联的数据访问。
//
// mcp_servers 是广场的源头表：用户经 /api/mcps 增删改（http 型），管理员经
// config.yaml 种子投影（system=1，可含 stdio 型）；session_mcp_servers 记录会话
// 启用的 server，PUT 整体替换语义与 session_skills 一致。
package dao

import (
	"context"
	"encoding/json"
	"fmt"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/mcp"

	"gorm.io/gorm"
)

// ListMcpServers 广场全量 server（system 置顶，其余按创建时间倒序）
func ListMcpServers(ctx context.Context) ([]*model.McpServer, error) {
	q := db.Ctx(ctx)
	m := q.McpServer
	rows, err := m.WithContext(ctx).
		Order(m.System.Desc()).
		Order(m.ID).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询 MCP 列表失败"), errors.NewMsg("系统异常"))
	}
	return rows, nil
}

// GetMcpServerByID 按主键查询（不存在返回 nil）
func GetMcpServerByID(ctx context.Context, id int64) (*model.McpServer, error) {
	q := db.Ctx(ctx)
	row, err := q.McpServer.WithContext(ctx).Where(q.McpServer.ID.Eq(id)).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询 MCP 失败"), errors.NewMsg("系统异常"))
	}
	return row, nil
}

// GetMcpServerByName 按短名查询（种子按 name 幂等投影、建重名校验用）
func GetMcpServerByName(ctx context.Context, name string) (*model.McpServer, error) {
	q := db.Ctx(ctx)
	row, err := q.McpServer.WithContext(ctx).Where(q.McpServer.Name.Eq(name)).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询 MCP 失败"), errors.NewMsg("系统异常"))
	}
	return row, nil
}

// GetMcpServersByIDs 按主键集合查询（会话启用存在性校验用）
func GetMcpServersByIDs(ctx context.Context, ids []int64) ([]*model.McpServer, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := db.Ctx(ctx)
	rows, err := q.McpServer.WithContext(ctx).Where(q.McpServer.ID.In(ids...)).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询 MCP 失败"), errors.NewMsg("系统异常"))
	}
	return rows, nil
}

// ---------- DB 行 → 引擎定义 ----------

// McpDefFromRow 广场注册行 → 引擎 ServerDef（JSON 列解析，坏数据返回错误由
// 调用方降级）。放在 dao 层供 api 与 agent 共用，避免两处重复解析逻辑
func McpDefFromRow(row *model.McpServer) (mcp.ServerDef, error) {
	def := mcp.ServerDef{
		ID:             row.ID,
		Name:           row.Name,
		Transport:      row.Transport,
		CallTimeoutSec: int(row.CallTimeoutSec),
	}
	if row.URL != nil {
		def.URL = *row.URL
	}
	headers, err := parseJSONMap(row.Headers)
	if err != nil {
		return def, fmt.Errorf("headers 列解析失败: %w", err)
	}
	def.Headers = headers
	env, err := parseJSONMap(row.Env)
	if err != nil {
		return def, fmt.Errorf("env 列解析失败: %w", err)
	}
	def.Env = env
	command, err := parseJSONSlice(row.Command)
	if err != nil {
		return def, fmt.Errorf("command 列解析失败: %w", err)
	}
	def.Command = command
	allow, err := parseJSONSlice(row.ToolAllow)
	if err != nil {
		return def, fmt.Errorf("tool_allow 列解析失败: %w", err)
	}
	def.ToolAllow = allow
	return def, nil
}

// parseJSONMap JSON 对象文本 → map（空/坏数据返回空 map + 错误）
func parseJSONMap(p *string) (map[string]string, error) {
	out := map[string]string{}
	if p == nil || *p == "" || *p == "null" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(*p), &out); err != nil {
		return map[string]string{}, err
	}
	return out, nil
}

// parseJSONSlice JSON 数组文本 → slice（空/坏数据返回空 slice + 错误）
func parseJSONSlice(p *string) ([]string, error) {
	out := []string{}
	if p == nil || *p == "" || *p == "null" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(*p), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateMcpServer 新建广场条目（带归属）
func CreateMcpServer(ctx context.Context, row *model.McpServer) error {
	q := db.Ctx(ctx)
	if err := q.McpServer.WithContext(ctx).Create(row); err != nil {
		return errors.Join(err, errors.New("写入 MCP 失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// UpdateMcpServerColumns 局部更新（PATCH / 引擎回写 tool_count、last_error）
func UpdateMcpServerColumns(ctx context.Context, id int64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	q := db.Ctx(ctx)
	if _, err := q.McpServer.WithContext(ctx).Where(q.McpServer.ID.Eq(id)).UpdateColumns(updates); err != nil {
		return errors.Join(err, errors.New("更新 MCP 失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// DeleteMcpServerCascade 删除广场条目并连带清理会话关联（事务）
func DeleteMcpServerCascade(ctx context.Context, id int64) error {
	return db.Transition(ctx, func(txCtx context.Context) error {
		q := db.Ctx(txCtx)
		sm := q.SessionMcpServer
		if _, err := sm.WithContext(txCtx).Where(sm.McpServerID.Eq(id)).Delete(); err != nil {
			return errors.Join(err, errors.New("清理 MCP 会话关联失败"), errors.NewMsg("系统异常"))
		}
		if _, err := q.McpServer.WithContext(txCtx).Where(q.McpServer.ID.Eq(id)).Delete(); err != nil {
			return errors.Join(err, errors.New("删除 MCP 失败"), errors.NewMsg("系统异常"))
		}
		return nil
	})
}

// McpSeedItem config 种子的一项（引擎定义 + 展示字段）
type McpSeedItem struct {
	Name           string
	Description    string
	Transport      string
	URL            *string
	HeadersJSON    *string
	CommandJSON    *string
	EnvJSON        *string
	CallTimeoutSec int32
	ToolAllowJSON  *string
}

// SyncMcpSeeds 把 config.yaml 的 MCP.Servers 投影进 mcp_servers（system=1）：
// 按 name 匹配——不存在则插入；已存在（无论来源）则刷新种子字段。
// 配置里删掉的种子不反向清理 DB（避免管理员误删配置把广场条目连带删光，
// 网页端可手工删除多余的 system 条目之外的数据，system 行清理走 SQL）。
func SyncMcpSeeds(ctx context.Context, seeds []McpSeedItem) error {
	for _, s := range seeds {
		existing, err := GetMcpServerByName(ctx, s.Name)
		if err != nil {
			return err
		}
		if existing == nil {
			if err := CreateMcpServer(ctx, &model.McpServer{
				Name:           s.Name,
				Description:    s.Description,
				Transport:      s.Transport,
				URL:            s.URL,
				Headers:        s.HeadersJSON,
				Command:        s.CommandJSON,
				Env:            s.EnvJSON,
				CallTimeoutSec: s.CallTimeoutSec,
				ToolAllow:      s.ToolAllowJSON,
				Enabled:        true,
				System:         true,
			}); err != nil {
				return err
			}
			continue
		}
		if err := UpdateMcpServerColumns(ctx, existing.ID, map[string]interface{}{
			"description":      s.Description,
			"transport":        s.Transport,
			"url":              s.URL,
			"headers":          s.HeadersJSON,
			"command":          s.CommandJSON,
			"env":              s.EnvJSON,
			"call_timeout_sec": s.CallTimeoutSec,
			"tool_allow":       s.ToolAllowJSON,
			"system":           true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 会话启用关联 ----------

// SessionMcpRow 会话启用的一项 MCP Server
type SessionMcpRow struct {
	Server model.McpServer
}

// ListSessionMcps 查询会话启用的全部 MCP Server（按关联顺序）
func ListSessionMcps(ctx context.Context, sessionID string) ([]SessionMcpRow, error) {
	q := db.Ctx(ctx)
	sm := q.SessionMcpServer
	rows, err := sm.WithContext(ctx).
		Where(sm.SessionID.Eq(sessionID)).
		Order(sm.ID).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询会话 MCP 失败"), errors.NewMsg("系统异常"))
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.McpServerID)
	}
	servers, err := q.McpServer.WithContext(ctx).Where(q.McpServer.ID.In(ids...)).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询会话 MCP 失败"), errors.NewMsg("系统异常"))
	}
	byID := make(map[int64]*model.McpServer, len(servers))
	for _, s := range servers {
		byID[s.ID] = s
	}
	out := make([]SessionMcpRow, 0, len(rows))
	for _, r := range rows {
		s, ok := byID[r.McpServerID]
		if !ok {
			continue // 注册行被并发清理：跳过悬空关联
		}
		out = append(out, SessionMcpRow{Server: *s})
	}
	return out, nil
}

// ReplaceSessionMcps 整体替换会话启用的 MCP 集合（PUT 语义，事务内删旧插新）
func ReplaceSessionMcps(ctx context.Context, sessionID string, serverIDs []int64) error {
	return db.Transition(ctx, func(txCtx context.Context) error {
		q := db.Ctx(txCtx)
		sm := q.SessionMcpServer
		if _, err := sm.WithContext(txCtx).Where(sm.SessionID.Eq(sessionID)).Delete(); err != nil {
			return errors.Join(err, errors.New("更新会话 MCP 失败"), errors.NewMsg("系统异常"))
		}
		if len(serverIDs) == 0 {
			return nil
		}
		rows := make([]*model.SessionMcpServer, 0, len(serverIDs))
		for _, id := range serverIDs {
			rows = append(rows, &model.SessionMcpServer{SessionID: sessionID, McpServerID: id})
		}
		if err := sm.WithContext(txCtx).Create(rows...); err != nil {
			return errors.Join(err, errors.New("更新会话 MCP 失败"), errors.NewMsg("系统异常"))
		}
		return nil
	})
}
