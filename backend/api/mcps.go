// mcps.go MCP 广场管理 API（阶段八）。
//
// 广场是共享生态：所有登录用户可浏览、添加（仅 http 型）与在会话中启用；
// 编辑/删除限所有者，system 种子（config 投影）网页不可删改——stdio 型
// server 只能由管理员经配置文件导入（安全边界见 specs/mcp/README.md §4）。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"backend/config"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"backend/pkg/mcp"
)

// ---------- 出参 ----------

type McpOut struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Transport   string `json:"transport"`
	URL         string `json:"url,omitempty"`
	// HeadersMasked 鉴权头只回键名，值打码（是否配置过可见，值不可见）
	HeadersMasked  map[string]string `json:"headers_masked"`
	ToolAllow      []string          `json:"tool_allow"`
	CallTimeoutSec int               `json:"call_timeout_sec"`
	Enabled        bool              `json:"enabled"`
	System         bool              `json:"system"`
	OwnerID        int64             `json:"owner_id"`
	OwnerName      string            `json:"owner_name"`
	IsOwner        bool              `json:"is_owner"`
	ToolCount      int               `json:"tool_count"`
	LastError      *string           `json:"last_error"`
	LastSyncedAt   *string           `json:"last_synced_at"`
	CreatedAt      string            `json:"created_at"`
}

func toMcpOut(s *model.McpServer, identity jwt.TokenClaims) McpOut {
	masked := map[string]string{}
	if m, err := parseHeadersOut(s.Headers); err == nil {
		for k := range m {
			masked[k] = "***"
		}
	}
	allow, _ := parseAllowOut(s.ToolAllow)
	out := McpOut{
		ID:             s.ID,
		Name:           s.Name,
		Description:    s.Description,
		Transport:      s.Transport,
		HeadersMasked:  masked,
		ToolAllow:      allow,
		CallTimeoutSec: int(s.CallTimeoutSec),
		Enabled:        s.Enabled,
		System:         s.System,
		OwnerID:        s.OwnerID,
		OwnerName:      s.OwnerName,
		IsOwner:        s.OwnerID != 0 && s.OwnerID == identity.UserID,
		ToolCount:      int(s.ToolCount),
		LastError:      s.LastError,
		CreatedAt:      s.CreatedAt.Format(time.RFC3339),
	}
	if s.URL != nil {
		out.URL = *s.URL
	}
	if s.LastSyncedAt != nil {
		t := s.LastSyncedAt.Format(time.RFC3339)
		out.LastSyncedAt = &t
	}
	return out
}

func parseHeadersOut(p *string) (map[string]string, error) {
	out := map[string]string{}
	if p == nil || *p == "" || *p == "null" {
		return out, nil
	}
	err := json.Unmarshal([]byte(*p), &out)
	return out, err
}

func parseAllowOut(p *string) ([]string, error) {
	out := []string{}
	if p == nil || *p == "" || *p == "null" {
		return out, nil
	}
	err := json.Unmarshal([]byte(*p), &out)
	return out, err
}

// ---------- 广场列表 ----------

type ListMcpsReq struct{}

// ListMcps 广场列表 (GET /api/mcps)：system 种子置顶；含缓存的工具数与
// 最近连接错误（卡片状态点）
func ListMcps(ctx context.Context, _ *ListMcpsReq) (*[]McpOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	rows, err := dao.ListMcpServers(ctx)
	if err != nil {
		return nil, err
	}
	res := make([]McpOut, 0, len(rows))
	for _, s := range rows {
		res = append(res, toMcpOut(s, identity))
	}
	return &res, nil
}

// ---------- 新增（仅 http） ----------

type CreateMcpReq struct {
	Name           string            `json:"name" binding:"required"`
	Description    string            `json:"description"`
	URL            string            `json:"url" binding:"required"`
	Headers        map[string]string `json:"headers"`
	ToolAllow      []string          `json:"tool_allow"`
	CallTimeoutSec int               `json:"call_timeout_sec"`
}

// CreateMcp 广场添加 MCP Server (POST /api/mcps)。
// 安全线：transport 强制 http——stdio 型（后端宿主机起子进程）只能由管理员
// 经 config.yaml 种子导入，任意用户可配命令等于开放主机任意执行
func CreateMcp(ctx context.Context, req *CreateMcpReq) (*McpOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	name := strings.TrimSpace(req.Name)
	if !mcp.ValidServerName(name) {
		return nil, errors.NewMsg("名字需为 1-64 位小写字母、数字与连字符（[a-z0-9-]）")
	}
	rawURL := strings.TrimSpace(req.URL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.NewMsg("URL 必须是合法的 http/https 地址")
	}
	if existing, err := dao.GetMcpServerByName(ctx, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, errors.NewMsg("已存在同名 MCP 服务「" + name + "」")
	}

	row := &model.McpServer{
		Name:           name,
		Description:    strings.TrimSpace(req.Description),
		Transport:      "http",
		URL:            &rawURL,
		CallTimeoutSec: clampTimeout(req.CallTimeoutSec),
		Enabled:        true,
		OwnerID:        identity.UserID,
		OwnerName:      identity.Name,
	}
	if len(req.Headers) > 0 {
		row.Headers = strPtr(marshalJSONMust(req.Headers))
	}
	if len(req.ToolAllow) > 0 {
		row.ToolAllow = strPtr(marshalJSONMust(req.ToolAllow))
	}
	if err := dao.CreateMcpServer(ctx, row); err != nil {
		return nil, err
	}
	slog.Info("MCP 广场新增", "name", name, "url", rawURL, "uid", identity.UserID)

	// 异步预热一次连接（回写 tool_count / last_error，广场卡片立即可见状态）
	go testAndSyncRow(row)

	out := toMcpOut(row, identity)
	return &out, nil
}

func clampTimeout(sec int) int32 {
	if sec <= 0 {
		return 60
	}
	if sec > 600 {
		return 600
	}
	return int32(sec)
}

func marshalJSONMust(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------- 编辑 / 删除 ----------

type McpIDReq struct {
	ID int64 `uri:"id"`
}

type UpdateMcpReq struct {
	ID             int64              `uri:"id"`
	Description    *string            `json:"description"`
	URL            *string            `json:"url"`
	Headers        *map[string]string `json:"headers"`    // null=不改；传对象（可为空）=全量替换
	ToolAllow      *[]string          `json:"tool_allow"` // null=不改；传数组（可为空）=全量替换
	CallTimeoutSec *int               `json:"call_timeout_sec"`
	Enabled        *bool              `json:"enabled"`
}

// loadOwnedMcpRow 加载广场行并校验所有者（system 种子无人可改）
func loadOwnedMcpRow(ctx context.Context, identity jwt.TokenClaims, id int64) (*model.McpServer, error) {
	row, err := dao.GetMcpServerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("MCP 服务不存在")
	}
	if row.System {
		return nil, errors.NewMsg("系统内置 MCP 服务只能由管理员在配置文件中维护")
	}
	if row.OwnerID == 0 || row.OwnerID != identity.UserID {
		return nil, errors.NewMsg("仅创建者可执行该操作")
	}
	return row, nil
}

// UpdateMcp 编辑 MCP Server (PATCH /api/mcps/:id)。名字不可改（消歧前缀
// 稳定性）；URL 只接受 http/https；改动后引擎按新定义自动重连
func UpdateMcp(ctx context.Context, req *UpdateMcpReq) (*McpOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedMcpRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if req.Description != nil {
		updates["description"] = strings.TrimSpace(*req.Description)
		row.Description = strings.TrimSpace(*req.Description)
	}
	if req.URL != nil {
		rawURL := strings.TrimSpace(*req.URL)
		u, uerr := url.Parse(rawURL)
		if uerr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.NewMsg("URL 必须是合法的 http/https 地址")
		}
		updates["url"] = rawURL
		row.URL = &rawURL
	}
	if req.Headers != nil {
		if len(*req.Headers) == 0 {
			updates["headers"] = nil
			row.Headers = nil
		} else {
			h := marshalJSONMust(*req.Headers)
			updates["headers"] = h
			row.Headers = &h
		}
	}
	if req.ToolAllow != nil {
		if len(*req.ToolAllow) == 0 {
			updates["tool_allow"] = nil
			row.ToolAllow = nil
		} else {
			a := marshalJSONMust(*req.ToolAllow)
			updates["tool_allow"] = a
			row.ToolAllow = &a
		}
	}
	if req.CallTimeoutSec != nil {
		v := clampTimeout(*req.CallTimeoutSec)
		updates["call_timeout_sec"] = v
		row.CallTimeoutSec = v
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
		row.Enabled = *req.Enabled
	}
	if err := dao.UpdateMcpServerColumns(ctx, row.ID, updates); err != nil {
		return nil, err
	}

	// 定义变化：以新定义重连（未就绪的连接惰性重建）
	if def, derr := dao.McpDefFromRow(row); derr == nil {
		mcp.EnsureRegistered([]mcp.ServerDef{def})
	}
	out := toMcpOut(row, identity)
	return &out, nil
}

// DeleteMcp 删除 MCP Server (DELETE /api/mcps/:id)：连带清理会话关联与
// 引擎连接（stdio 子进程即时回收）
func DeleteMcp(ctx context.Context, req *McpIDReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedMcpRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := dao.DeleteMcpServerCascade(ctx, row.ID); err != nil {
		return nil, err
	}
	mcp.RemoveServer(row.ID)
	slog.Info("MCP 广场删除", "name", row.Name, "uid", identity.UserID)
	return &StatusOKResp{Status: "ok"}, nil
}

// ---------- 测试连接 / 工具清单 ----------

type McpTestOut struct {
	OK              bool           `json:"ok"`
	ProtocolVersion string         `json:"protocol_version,omitempty"`
	ServerName      string         `json:"server_name,omitempty"`
	ToolCount       int            `json:"tool_count"`
	Tools           []mcp.ToolInfo `json:"tools"`
	Error           string         `json:"error,omitempty"`
}

// TestMcp 测试连接 (POST /api/mcps/:id/test)：真连 + 握手 + 全量工具发现，
// 回写 tool_count / last_error / last_synced_at（广场卡片状态缓存）
func TestMcp(ctx context.Context, req *McpIDReq) (*McpTestOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := dao.GetMcpServerByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("MCP 服务不存在")
	}
	def, err := dao.McpDefFromRow(row)
	if err != nil {
		return nil, errors.NewMsg("配置数据异常: " + err.Error())
	}
	info := mcp.TestServer(ctx, def)
	syncTestResult(row.ID, info)
	return &McpTestOut{
		OK:              info.OK,
		ProtocolVersion: info.ProtocolVersion,
		ServerName:      info.ServerName,
		ToolCount:       info.ToolCount,
		Tools:           info.Tools,
		Error:           info.Err,
	}, nil
}

// syncTestResult 测试结果回写广场缓存列
func syncTestResult(serverID int64, info mcp.TestInfo) {
	bg := db.WithContext(context.Background())
	var updates map[string]interface{}
	if info.OK {
		updates = map[string]interface{}{
			"tool_count":     info.ToolCount,
			"last_error":     nil,
			"last_synced_at": time.Now(),
		}
	} else {
		errMsg := info.Err
		if len(errMsg) > 512 {
			errMsg = errMsg[:512]
		}
		updates = map[string]interface{}{"last_error": errMsg}
	}
	if err := dao.UpdateMcpServerColumns(bg, serverID, updates); err != nil {
		slog.Warn("回写 MCP 测试结果失败", "server", serverID, "err", errors.Stack(err))
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// testAndSyncRow 后台预热连接并回写状态（新增后异步触发）
func testAndSyncRow(row *model.McpServer) {
	def, err := dao.McpDefFromRow(row)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	info := mcp.TestServer(ctx, def)
	syncTestResult(row.ID, info)
	if info.OK {
		slog.Info("MCP 广场新增预热成功", "name", row.Name, "tools", info.ToolCount)
	}
}

type McpToolsReq struct {
	ID int64 `uri:"id"`
}

// GetMcpTools 工具清单 (GET /api/mcps/:id/tools)：引擎缓存，未连接则现场触发
func GetMcpTools(ctx context.Context, req *McpToolsReq) (*[]mcp.ToolInfo, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := dao.GetMcpServerByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("MCP 服务不存在")
	}
	def, err := dao.McpDefFromRow(row)
	if err != nil {
		return nil, errors.NewMsg("配置数据异常: " + err.Error())
	}
	tools, err := mcp.ToolsOfServer(ctx, def)
	if err != nil {
		return nil, errors.NewMsg("连接失败: " + err.Error())
	}
	if tools == nil {
		tools = []mcp.ToolInfo{}
	}
	return &tools, nil
}

// ---------- 会话启用 ----------

type SetEnabledMcpsReq struct {
	ID           string  `uri:"id"`
	McpServerIDs []int64 `json:"mcp_server_ids"`
}

// SetEnabledMcps 替换会话启用的 MCP 集合 (PUT /api/sessions/:id/mcps)。
// 停用状态的 server 拒绝启用；PUT 整体替换语义与技能启用一致
func SetEnabledMcps(ctx context.Context, req *SetEnabledMcpsReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	if _, err := loadOwnedSession(ctx, identity, req.ID); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(req.McpServerIDs))
	seen := map[int64]struct{}{}
	for _, id := range req.McpServerIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	rows, err := dao.GetMcpServersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, errors.NewMsg("存在无效的 MCP 服务（可能已被删除）")
	}
	for _, r := range rows {
		if !r.Enabled {
			return nil, errors.NewMsg("MCP 服务「" + r.Name + "」已被停用")
		}
	}
	if err := dao.ReplaceSessionMcps(ctx, req.ID, ids); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}

// ---------- 种子投影（启动） ----------

// InitMcpSeeds 启动时把 config.yaml 的 MCP.Servers 投影进广场（system=1），
// 并把全部启用行喂给引擎（惰性连接，stdio 子进程等到首次使用才拉起）
func InitMcpSeeds() {
	ctx := db.WithContext(context.Background())
	cfg := config.GetConfig().MCP
	seeds := make([]dao.McpSeedItem, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if !mcp.ValidServerName(s.Name) {
			slog.Warn("MCP 种子名字不合法已跳过", "name", s.Name)
			continue
		}
		transport := s.Transport
		if transport != "stdio" {
			transport = "http" // 缺省 http；stdio 只认显式声明
		}
		item := dao.McpSeedItem{
			Name:           s.Name,
			Description:    s.Description,
			Transport:      transport,
			CallTimeoutSec: int32(clampTimeout(s.CallTimeoutSec)),
		}
		if s.URL != "" {
			item.URL = &s.URL
		}
		if len(s.Headers) > 0 {
			item.HeadersJSON = strPtr(marshalJSONMust(s.Headers))
		}
		if len(s.ToolAllow) > 0 {
			item.ToolAllowJSON = strPtr(marshalJSONMust(s.ToolAllow))
		}
		if transport == "stdio" {
			cmd := append(append([]string{}, s.Command...), s.Args...)
			if len(cmd) > 0 {
				item.CommandJSON = strPtr(marshalJSONMust(cmd))
			}
			if len(s.Env) > 0 {
				item.EnvJSON = strPtr(marshalJSONMust(s.Env))
			}
		}
		seeds = append(seeds, item)
	}
	if len(seeds) > 0 {
		if err := dao.SyncMcpSeeds(ctx, seeds); err != nil {
			slog.Warn("MCP 种子投影失败（不影响启动）", "err", errors.Stack(err))
		}
	}

	// 全部启用行登记进引擎（不拨号；turn 侧也会按 DB 现值重新喂）
	rows, err := dao.ListMcpServers(ctx)
	if err != nil {
		slog.Warn("加载 MCP 广场失败（不影响启动）", "err", errors.Stack(err))
		return
	}
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		if def, derr := dao.McpDefFromRow(r); derr == nil {
			mcp.EnsureRegistered([]mcp.ServerDef{def})
		} else {
			slog.Warn("MCP 行定义解析失败", "name", r.Name, "err", derr.Error())
		}
	}
}
