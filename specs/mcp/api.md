# MCP 广场接口契约与通信协议规范 (API Specification)

> **版本**：v1.0.0
> **协议**：HTTP/1.1、HTTP/2
> **认证方式**：`Authorization: Bearer <JWT_TOKEN>`（复用全局 jwt.CheckLogin 中间件）
> **核心代码实现**：[`backend/api/mcps.go`](../../backend/api/mcps.go)、[`backend/api/sessions.go`](../../backend/api/sessions.go)
> **默认数据响应封套**：`{"code": 0, "data": ..., "msg": "success"}`

---

## 1. 广场管理 API（/api/mcps）

### 1.1 列表 `GET /api/mcps`

全部登录用户可见（共享广场）。system 种子置顶排序。响应示例：

```json
{
  "code": 0,
  "data": [{
    "id": 1, "name": "weather", "description": "天气查询服务",
    "transport": "http", "url": "https://mcp.example.com/mcp",
    "headers_masked": {"Authorization": "***"},
    "tool_allow": ["get_weather"],
    "enabled": true, "system": true,
    "owner_id": 0, "owner_name": "System", "is_owner": false,
    "tool_count": 3, "last_error": null,
    "last_synced_at": "2026-10-08T10:00:00Z", "created_at": "2026-10-08T09:00:00Z"
  }]
}
```

- `headers_masked`：鉴权头**只回键名，值打码 `***`**（是否配置过可见，值不可见）。
- `last_error` 非空即卡片红点；`tool_count` 为缓存值（未测过为 0）。

### 1.2 新增 `POST /api/mcps`

```json
{
  "name": "weather",
  "description": "天气查询服务",
  "url": "https://mcp.example.com/mcp",
  "headers": {"Authorization": "Bearer xxx"},
  "tool_allow": [],
  "call_timeout_sec": 60
}
```

校验：name 匹配 `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$` 且全局唯一；transport 固定
`http`（**stdio 仅 config 种子，API 不接受**——任意用户可配命令等于后端主机任意执行）；
url 必填（http/https）。创建后异步触发一次连接测试（回写 tool_count/last_error）。

### 1.3 编辑 `PATCH /api/mcps/:id`

指针字段三态（nil=不改）。名字不可改（消歧前缀稳定性）。`headers` 与 `tool_allow`
为**全量替换语义**：传 `null`/缺省=不改；传对象或数组（可为空）=整体替换——编辑弹窗中
鉴权头值打码展示，留空表示"不提交该字段"（保持现有值），填了任意新值则本次提交的键值
集合就是新的全量。system 种子拒绝网页编辑。

### 1.4 删除 `DELETE /api/mcps/:id`

连带清理 session_mcp_servers 关联与引擎连接。system 种子拒删（错误
`"系统内置 MCP 服务只能由管理员在配置文件中维护"`）。

### 1.5 测试连接 `POST /api/mcps/:id/test`

同步执行：拨号 → 握手 → tools/list 分页聚合；成功回写 tool_count / last_synced_at /
清空 last_error，失败回写 last_error。响应（成功）：

```json
{
  "code": 0,
  "data": {
    "ok": true, "protocol_version": "2026-07-28", "server_name": "weather-server",
    "tool_count": 3,
    "tools": [{"name": "get_weather", "description": "Get current weather"}]
  }
}
```

失败时 `ok=false`、`error` 为可直接展示的失败原因（同样回写库）。

### 1.6 工具清单 `GET /api/mcps/:id/tools`

返回引擎缓存的工具清单（name / description / 消歧后的 function 名）；
未连接过则现场触发一次连接（等同 test，但只读不回写 last_error）。

## 2. 会话启用 API

### 2.1 替换启用集合 `PUT /api/sessions/:id/mcps`

```json
{"mcp_server_ids": [1, 3]}
```

对齐 SetEnabledSkills：RBAC 会话归属校验 → 去重 → 存在性与 enabled 校验 →
事务内整体替换 session_mcp_servers。响应 `{"status": "ok"}`。

### 2.2 详情回显

`GET /api/sessions/:id` 的 SessionDetailOut 新增：

```json
"enabled_mcp_ids": [1, 3],
"enabled_mcps": [{"mcp_server_id": 1, "name": "weather", "tool_count": 3}]
```

## 3. config.yaml 种子段

```yaml
# MCP 广场种子（阶段八）：启动时按 name 投影进 mcp_servers 表（system=1，
# 广场置顶、网页不可删改）。stdio 型仅此处允许——广场 API 只收 http。
MCP:
  Servers:
    - Name: "weather"            # 短名 [a-z0-9-]，工具消歧前缀
      Description: "官方天气查询 MCP Server"
      Transport: "http"          # http | stdio
      URL: "https://mcp.example.com/mcp"
      Headers:                   # 静态鉴权头
        Authorization: "Bearer xxx"
      Command: []                # stdio: ["npx","-y","@some/mcp-server"]
      Args: []
      Env: []
      ToolAllow: []              # 工具白名单（空=全部）
      CallTimeoutSec: 60
```

## 4. SSE 事件

MCP 工具调用复用既有 `tool_call_start` / `tool_call_result` / `tool_call_error`
事件（name 即 `mcp_<server>_<tool>` 消歧名），前端工具调用卡片零改动；
不新增事件类型。
