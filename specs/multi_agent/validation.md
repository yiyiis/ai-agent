# 多 Agent 实施与验收记录

验收日期：2026-10-23，Asia/Shanghai。状态：本期设计范围已实现，并通过本机测试。

## 已实现

- [x] `agent_runs`、messages/token_usages 的 agent_id、DB-first 增量迁移及 GORM GEN。
- [x] 根会话保留原消息身份；子任务独立历史、同根 workspace，不创建隐藏 session。
- [x] delegate_agent 同步结果返回、3 并发/12 累计/子16轮/5分钟、取消排队、幂等重放。
- [x] read_only 执行侧白名单、write 继承父权限，禁止子任务再委派/人工输入/动态加载技能。
- [x] 重启中断状态、根停止、删除等待与禁止已删除会话被旧请求重启。
- [x] 子事件隔离、持久详情 API、前端协作卡与消息详情、刷新恢复。
- [x] Token 按根会话汇总，明细保留 AgentID；输入恢复不混算子上下文水位。
- [x] 工作区工具互斥、工具取消 join、Docker/E2B 进程组清理与失败隔离、Docker 根租约。

## 自动化检查

| 命令/检查 | 实际结果 |
| --- | --- |
| `cd backend; go test ./...` | 全部通过 |
| `cd backend; go vet ./...` | 通过 |
| `cd backend; go build .`（实际指定 `.zcode` 输出） | 通过 |
| `cd frontend; npm run test:stream` | 39 项通过，含 Agent 归属及子终态事件测试 |
| `cd frontend; npm run build` | 通过；现有大 bundle 提示仍存在 |
| `git diff --check` | 通过 |
| 增量 SQL 重复执行 | 成功，验证幂等 |
| Docker 实测：`SANDBOX_LIVE_DOCKER=1 go test ./pkg/sandbox -run '^TestRemoteProcessLiveDockerCancellation$' -count=1 -v` | 通过，测试容器已清理 |
| `go test -race` | 未能运行：本机 CGO 关闭且无 GCC；不能把普通测试等同于 race 验证 |

新增测试覆盖消息隔离、身份越界拒绝、同根工作区、伪造写工具拦截、子任务成功/错误/取消/panic/未发完成事件、队列及累计限制、数据库与内存幂等、实际主 ReAct 委派两子任务再汇总、根/子账单、删除等待、重启中断、SSE 子事件不结束根流，以及冷区委派结果不建议重新执行。

真实 Docker 测试另外覆盖：取消先于远端启动、150ms deadline 后无普通子进程迟到写、同 sandbox 其他进程组未被误杀、监督器在 Popen 后登记 pgid 前崩溃不执行命令、普通后台子进程随工具结束清理。E2B 使用协议 mock 验证，未连接真实 E2B 云端服务；主动脱离进程组的后代限制见设计文档。

整库检查发现的 Windows MIME 差异也已修正测试：`text/markdown; charset=utf-8` 应按媒体类型解析，不要求字符串完全等于 `text/markdown`。生产 MIME 行为未改。

## 真实 HTTP / 模型验收

使用本机配置中的 MiniMax-M3，执行以下任务：主 Agent 创建 `shared.txt`，内容 `MULTI_AGENT_SHARED_OK`；同轮委派 reader_a、reader_b 两个 read_only 子 Agent，要求实际调用 read_file；主 Agent 汇总结果。

| 验证项 | 结果 |
| --- | --- |
| 根 SSE done / error | 1 / 0 |
| 子任务数量 / 终态 | 2 / 两者 completed |
| 子任务 workspace_id | 两者均等于根 session_id |
| 子任务读取标记 | 两者均返回 MULTI_AGENT_SHARED_OK |
| 根 user 消息数 | 1，未混入子任务指令 |
| 每个子任务独立消息数 | 各4条，包含实际 read_file 调用与结果 |
| 用量明细 | 7条：根3条、两个子任务各2条，全部归同根会话且标有各自 AgentID |
| 重启后的状态 API | 仍为2条 completed；根 ping=ok |
| 浏览器核验 | 刷新可恢复协作卡，展开可见独立工具历史和结果 |

测试聊天标题：`[多Agent验收] 共享工作区`。验收会话和临时验收账号保留在本地，浏览器已打开该会话；凭证不写入版本库。原始报告、SSE、截图和服务日志存放在忽略目录 `.zcode/`。

## 本机服务

- 前端：<http://127.0.0.1:5173>，Node/Vite PID `32628`。
- 后端：<http://127.0.0.1:8888/api/ping>，本次构建进程 PID `36396`。
- 后端运行目录 `backend`，加载原有 `etc/config.local.yaml`；数据库使用已有本机 MySQL，增量迁移已应用。
- 本次启动的两个服务保持运行。上述 PID 仅为本次验收记录，后续停止前须核对进程路径，不能直接复用过期 PID。

重新启动开发服务：在 backend 下 `go run .`，在 frontend 下 `npm run dev`。新环境先执行 `deploy/sql/migrations/20261023_multi_agent.sql`；启动不会自动迁移数据库。当前仍是单实例运行模型，不应直接启多个后端共享同一运行数据库。

## 边界

本次交付是完整的单层委派版本，不宣称实现无限递归、跨机执行、自动重放外部副作用、worktree 自动合并或恶意进程完整隔离。Claude Code 官方资料已实际读取；Codex 新官方文档正文在本次网络环境不可达，相关证据范围详见 research.md。
