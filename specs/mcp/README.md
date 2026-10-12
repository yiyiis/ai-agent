# MCP 广场（Model Context Protocol Plaza）模块总览

> **版本**：v1.0.0
> **协议基线**：MCP 规范 `2026-07-28`（向下兼容 `2025-06-18` / `2025-11-25` 存量服务器）
> **核心代码实现**：[`backend/pkg/mcp/`](../../backend/pkg/mcp/)、[`backend/api/mcps.go`](../../backend/api/mcps.go)、[`backend/dao/mcp.go`](../../backend/dao/mcp.go)、[`frontend/src/components/McpsPlaza.tsx`](../../frontend/src/components/McpsPlaza.tsx)
> **相关规范**：[engine.md](engine.md)（客户端引擎）、[database.md](database.md)（表设计）、[api.md](api.md)（接口契约）

---

## 1. 模块定位

系统原生接入开放标准 **Model Context Protocol (MCP)**，以纯客户端身份消费外部 MCP Server 暴露的
Tools 原语，并平滑转换为 Agent 内置的 Function Calling 规格参与 ReAct 调度（README 5.4）。

管理平面为 **MCP 广场**：对标技能广场的共享生态——所有登录用户可浏览、添加（HTTP 型）、启用
MCP Server；管理员可经 `config.yaml` 种子导入官方/内置 Server（唯一允许 stdio 的入口）。

```text
config.yaml(MCP 官方种子, 可含 stdio) ──启动投影(system=1)──┐
                                                           ▼
前端 McpsPlaza(广场页) ⇄ /api/mcps CRUD+测试 ⇄ mcp_servers 表（源头）
前端 McpPanel(会话抽屉) ⇄ PUT /api/sessions/:id/mcps ⇄ session_mcp_servers 表
                                                           │
agent.Run ──loadTurnMcps 读会话启用──→ pkg/mcp 引擎(Manager) ──HTTP/stdio──→ 外部 MCP Server
```

## 2. 协议调研结论（截至 2026-10，最新规范 2026-07-28）

### 2.1 协议核心演进

MCP 是 Anthropic 2024-11 发起的开放标准，用于 AI 应用与外部工具/数据源之间的标准化连接。
协议基座为 **JSON-RPC 2.0**；`2026-07-28` 版是发布以来最大重构（无状态化）：

| 主题 | 2025-06-18 / 2025-11-25（存量） | 2026-07-28（最新） |
|---|---|---|
| 会话 | `initialize` 握手 + `notifications/initialized`，HTTP 维护 `Mcp-Session-Id` | **无握手**：每请求 `_meta` 携带 `io.modelcontextprotocol/protocolVersion`、`clientInfo`、`clientCapabilities` |
| 版本发现 | initialize 响应协商 | `server/discover` RPC（客户端可作兼容性探测） |
| 传输 | stdio / Streamable HTTP / WebSocket(2025-11-25+) | 同左，另要求 `Mcp-Method`/`Mcp-Name` 请求头 |
| 变更通知 | HTTP GET SSE 流 + `resources/subscribe` | `subscriptions/listen` 单一长流，按类型订阅 |
| 工具列表 | `tools/list` + `listChanged` 通知 | 同左 + `ttlMs`/`cacheScope` 缓存提示、确定性排序建议 |
| 结果结构 | `content[]` + `structuredContent` + `isError` | 同左 + `resultType`（`complete` / `input_required`） |
| 服务端反向请求 | `sampling/createMessage`、`elicitation/create`、`roots/list` | 改为 **MRTR**（多轮往返）：返回 `input_required` 中间结果，客户端带 `inputResponses` 重试 |

**已正式废弃**（新实现不应采用）：Roots、Sampling、Logging 特性；HTTP+SSE 传输；
JSON-RPC batching；SSE 断线重放（`Last-Event-ID`）。

### 2.2 客户端必须双模兼容

存量生态服务器大多实现 `2025-06-18` / `2025-11-25`（需要 initialize 握手与
`Mcp-Session-Id` 会话），因此引擎的连接策略为：

1. 先发 `server/discover` 探测（新版无状态服务器直接可用）；
2. 方法不存在/超时 → 回退 `initialize` 握手（旧版），HTTP 记录并回带 `Mcp-Session-Id`；
3. 协商出的协议版本存于连接上，后续请求按所属模式构造 `_meta`。

### 2.3 v1 协议范围（明确边界）

**做**：Tools 原语（`tools/list` 分页 + `tools/call`）、stdio 与 Streamable HTTP 双传输、
静态 Headers 鉴权、`ttlMs` 感知的工具列表缓存。

**不做**：Resources / Prompts 原语（下版本评估）；OAuth 2.1 授权流（静态 Bearer 头承载）；
MRTR `input_required` 人机交互（返回占位文案，未来与阶段八 `request_input` 挂起机制一起做）；
`subscriptions/listen` 实时变更流（TTL 定时刷新替代）；已废弃的 Roots/Sampling/Logging。

## 3. 关键设计决策

- **手写协议客户端**：与项目零 SDK 依赖的自研风格一致（provider 层同为手写 OpenAI 兼容
  客户端）。`pkg/mcp` 仅实现客户端子集，纯标准库（`net/http` / `os/exec`），自包含、
  不依赖 tools/sandbox 包，可独立单测。
- **工具命名消歧**：`mcp_<server>_<tool>`（规范明确多服务器聚合必须消歧，且
  `serverInfo.name` 不保证唯一——本项目以广场注册的 server name 为前缀，配置约束
  `[a-z0-9-]`；工具名中 `.` 与非法字符替换为 `_`，超 64 字符截断并追加短哈希后缀）。
  执行时经 Manager 的 映射表反解，不做字符串拆解。
- **错误双轨**（规范要求）：JSON-RPC 协议错误（unknown tool / 畸形请求）→ `[MCP 调用失败]`
  前缀，提示模型勿原样重试；`isError: true` 工具执行错误 → `[MCP 工具执行错误]` 前缀，
  原文回喂模型供自纠正。
- **会话级启用**：镜像 session_skills 模式，`session_mcp_servers` 记录会话启用的 server，
  启用即把该 server 全部（白名单过滤后）工具 schema 注入 turn 工具列表。MCP 无 skill
  那种"挂载成本"，故为简单开关，无三态。
- **复用既有治理**：MCP 工具调用与内置工具同一并发池执行，Loop Guard 防震荡、
  tool_call_start/result SSE 事件、tool 消息落库回填、token 记账、上下文冷热分区
  （DefaultHotSteps 兜底）全部自动生效，agent 层仅新增约 30 行接线。

## 4. 安全边界（v1）

| 边界 | 决策 |
|---|---|
| stdio 传输 | **仅 config 种子**（管理员）可用；广场 API 强制 `transport=http`——任意用户可配 stdio 命令等于开放后端主机任意命令执行（RCE） |
| 沙箱 | MCP 工具**不经过会话沙箱**：远端 HTTP 天然在外；stdio 子进程跑在后端宿主机（种子由管理员负责） |
| 提示词注入 | 工具 description 来自外部服务器，视为不可信输入面；`annotations`（readOnlyHint 等）按规范视为不可信，不做权限决策依据 |
| 鉴权材料 | headers 明文存库（v1），API 出参打码（`***`），编辑留空=不改，日志脱敏 |
| 权限隔离 | v1 无租户隔离：所有用户共享广场、均可增删改（system 种子仅管理员经配置文件维护，广场不可删改） |

## 5. 端到端链路

```text
1. 管理员 config.yaml 配 MCP.Servers（可含 stdio）──启动──→ mcp.Init + 种子投影(system=1)
2. 用户广场添加 HTTP Server ──POST /api/mcps──→ mcp_servers 表 + Manager.Upsert
3. 用户点"测试连接" ──POST /api/mcps/:id/test──→ 惰性拨号 + tools/list，
   回写 tool_count / last_error，返回工具清单
4. 会话抽屉开关启用 ──PUT /api/sessions/:id/mcps──→ session_mcp_servers
5. 用户发消息 → agent.Run → loadTurnMcps 读启用集合 → mcp.SchemasFor(ids)
   注入 toolSchemas；turn 开始异步预热拨号（惰性拨号 3s 等待，超时本轮跳过下轮重试）
6. 模型发起 mcp_<server>_<tool> 调用 → 执行循环路由到 mcp.ExecuteTool →
   tools/call → 结果桥接（文本拼接 / structuredContent 序列化 / 32KB 截断）→
   tool 消息落库回填 → 下一轮推理
```
