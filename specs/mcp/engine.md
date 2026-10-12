# MCP 客户端引擎设计 (pkg/mcp)

> **版本**：v1.0.0
> **协议基线**：MCP `2026-07-28`（双模兼容 `2025-06-18` / `2025-11-25`）
> **代码实现**：[`backend/pkg/mcp/`](../../backend/pkg/mcp/)

---

## 1. 包结构与依赖方向

```text
pkg/mcp/
├── mcp.go             门面：Init/Shutdown/Upsert/Remove/SchemasFor/IsMCPTool/ExecuteTool/Status
├── config.go          配置段结构（项目惯例：配置结构体由所属包自持）
├── types.go           wire 类型：RPCRequest/RPCResponse、Tool、CallToolResult、ContentBlock
├── transport.go       Transport 接口：Request(ctx, req) (*RPCResponse, error) / Close()
├── transport_stdio.go 子进程 + 换行分隔 JSON-RPC；stderr→slog；崩溃退避重启
├── transport_http.go  Streamable HTTP：POST + 双响应解析；Mcp-Session-Id 兼容回带
├── client.go          双模协议客户端：server/discover 探测→initialize 回退；分页/调用
├── manager.go         连接注册表：惰性拨号、TTL 刷新、消歧命名映射、状态机
└── bridge.go          Tool→OpenAI function schema；CallToolResult→文本
```

依赖约束：**pkg/mcp 不 import tools / sandbox / dao**（引擎完全自包含，可独立单测；
与 agent 的对接在 agent 层完成——`agent → mcp` 与 `agent → tools` 平行）。

## 2. 传输层

### 2.1 Transport 接口

```go
type Transport interface {
    // Request 发送一个 JSON-RPC 请求并取回对应响应（按 id 匹配）。
    // 途中到达的通知（method 非 "" 的消息）经 notify 回调上抛（可能为 nil）。
    Request(ctx context.Context, req *RPCRequest, notify func(*RPCMessage)) (*RPCResponse, error)
    Close() error
}
```

### 2.2 stdio（`transport_stdio.go`）

- `os/exec` 启动子进程（Command + Args + Env），stdin/stdout 为**换行分隔**的 JSON-RPC 消息流。
- 读循环 goroutine 逐行 `json.Decoder` 解码；按 `id` 匹配 pending map（`sync.Map` +
  buffered channel）分发响应；通知类消息（无 id）走 notify 回调。
- stderr 接 `slog.Debug`（服务器日志惯例输出到 stderr）。
- 进程退出 → 清空 pending（全部返回连接错误）→ 退避重启（1s、2s、4s…封顶 30s），
  重启后由 Manager 触发协议层重新握手（旧版 initialize）。
- Close：先写 `notifications/cancelled`（尽力而为）再 kill 进程树。

### 2.3 Streamable HTTP（`transport_http.go`）

- 每请求 `POST <URL>`，`Content-Type: application/json`，
  `Accept: application/json, text/event-stream`（规范允许两种响应）。
- 必带头：`Mcp-Method`（请求方法名）、`Mcp-Name`（客户端名，2026-07-28 要求）；
  旧版会话：从响应 `Mcp-Session-Id` 头记录，后续请求回带。
- 响应解析双分支：
  - `Content-Type: application/json` → 单 JSON-RPC 消息；
  - `text/event-stream` → SSE 流，取**首个 data 帧即请求响应**；后续帧（如
    `notifications/tools/list_changed`）经 notify 回调上抛后关闭流（v1 不维持 GET 流）。
- 每请求独立 HTTP 连接（`net/http` 复用 Transport 级连接池），无跨请求会话状态——
  与 2026-07-28 无状态语义天然对齐。

## 3. 协议客户端（`client.go`，双模）

### 3.1 连接握手策略

```text
dial():
  1. 发 server/discover（2026-07-28 无状态探测）
     ├─ 成功 → mode=stateless，negotiated=响应支持的版本（取与客户端共同最高）
     └─ Method not found(-32601) / 超时 → mode=legacy
  2. legacy：initialize(protocolVersion=客户端支持的最高旧版) →
     记录 negotiated 版本与 serverInfo → notifications/initialized
     （HTTP 传输在此步捕获 Mcp-Session-Id）
```

### 3.2 请求构造

- `stateless` 模式：每个请求 `params._meta` 注入
  `io.modelcontextprotocol/protocolVersion` / `clientInfo` / `clientCapabilities`。
- `legacy` 模式：直接发请求（会话语义由传输层 Session-Id / 长连接承载）。

### 3.3 工具原语

- `listTools(ctx)`：分页循环（`nextCursor` → 下一页 `cursor`），聚合 `tools` 数组；
  记录响应 `ttlMs`（缺省 300000ms）。
- `callTool(ctx, name, args)`：`tools/call`；响应按 `resultType` 分流：
  - `complete`（或旧版缺省）→ 正常结果；
  - `input_required` → 返回占位结果（MRTR v1 不交互，文案告知模型该工具需要
    额外交互输入、暂不支持、建议换工具或告知用户）。

## 4. 连接管理（`manager.go`）

### 4.1 状态机

```text
idle ──拨号(惰性触发)──→ connecting ──握手+首次 tools/list──→ ready
                              │                                │
                              └──────── 失败 ──────────────────┘
                                        │ 记录 lastError，进入退避
                                        ▼
                              backoff(1s→30s) ──到期重试──→ connecting
删除(Remove)/关闭：任意状态 → closed，stdio 杀进程，HTTP 释放
```

- **惰性拨号**：`Upsert` 只登记定义不连接；首次 `SchemasFor`/`ExecuteTool` 命中或
  `Test` 调用时拨号（单飞 singleflight 语义，`sync.Once` 按代际重置）。
- **同步等待窗口**：`ensureReady` 等待连接完成最多 **3s**——超时本轮跳过该 server
  （工具不注入/调用返回"连接中"文案），下轮 turn 重试；广场"测试连接"按钮无此限制。
- **工具列表缓存**：`ready` 后缓存 tools + 消费时间戳；TTL = min(自身默认 5min,
  服务器 ttlMs)；过期后下一次访问懒刷新。执行遇 `-32602 Unknown tool` → 即时强制刷新
  一次并重试一次。
- **list_changed 通知**：HTTP SSE 后续帧 / stdio 通知上抛 → 标记缓存失效（下次访问刷新）。

### 4.2 消歧命名

```text
qualified = "mcp_" + <server name，配置约束 [a-z0-9]-> + "_" + sanitize(<tool name>)
sanitize: 非法字符(非 [A-Za-z0-9_-]) → '_'；整体超 64 字符 → 截断 + "_" + md5[:6]
```

Manager 维护 `qualified → (serverID, 原始 tool name)` 双向映射，schema 注入与执行
路由均查表，不做字符串拆解；同名冲突（截断哈希后仍撞）以后注册者丢弃并告警。

## 5. 桥接（`bridge.go`）

### 5.1 Tool → OpenAI function schema

```json
{
  "type": "function",
  "function": {
    "name": "mcp_weather_get_weather",
    "description": "[MCP server:weather] Get current weather …",
    "parameters": { <inputSchema 原样透传，JSON Schema 2020-12 子集与 function
                     calling 参数同构；缺省补 {"type":"object"} > }
  }
}
```

description 追加 `[MCP server:<name>]` 来源标注；`tool_allow` 白名单在此层过滤。

### 5.2 CallToolResult → 文本（喂模型）

| 结果成分 | 处理 |
|---|---|
| `content[].type=text` | 按序拼接 |
| `content[].type=image/audio/resource/resource_link` | `[收到 N 个 <type> 内容块，v1 暂不展示]` 占位（不做多模态回传） |
| `structuredContent` | 存在时序列化 JSON 追加（服务器按规范应同时给 text 兜底，这里双保险） |
| `isError=true` | `[MCP 工具执行错误] <文本>`——回喂模型自纠正（规范 SHOULD） |
| JSON-RPC error | `[MCP 调用失败] <code> <message>`——基础设施错，提示勿原样重试 |
| 超长 | 32KB 头尾截断 + `[MCP] 结果超长已截断（原文 X 字节）`（v1 不落盘 blob） |
| 超时 | `[MCP 超时] 工具 <name> 执行超过 <N>s 未返回`（对齐 tools 层超时文案风格） |

## 6. 超时与并发

- `tools/call` 超时 = server 定义 `call_timeout_sec`（默认 60s），context 传播取消。
- Manager 各 server 连接相互独立；同一 server 的并发 callTool 复用一条连接
  （stdio 长连接 / HTTP 连接池），JSON-RPC id 自增保证并发安全。
- 拨号超时 10s（TCP+HTTP 或进程启动+握手）；握手探测（server/discover）超时 3s。

## 7. 测试策略

- **HTTP 假服务器**：`httptest.Server` 实现 Streamable HTTP 双模式（新版无状态直答 /
  旧版 initialize + Session-Id），覆盖：探测回退、分页 tools/list、SSE 响应解析、
  Mcp-Session-Id 回带、isError/协议错误文案、input_required 占位、超时、TTL 刷新。
- **stdio 假服务器**：测试辅助子进程（`go test` 内 `os/exec` 自引用 test binary），
  覆盖：行分隔编解码、崩溃重启、Close 清理。
- **桥接单测**：命名消歧（非法字符/超长/冲突）、schema 透传、结果拼接与截断。
