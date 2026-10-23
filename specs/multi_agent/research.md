# 多 Agent 设计参考与证据边界

调研日期：2026-10-23（Asia/Shanghai）。本文是设计输入；本项目的最终接口与实施范围以本目录其他规格为准。

## 1. 已读取的官方资料

本次通过 HTTPS 实际读取了以下 Claude Code 官方页面及其 Markdown 版本，以下结论来自页面正文，未使用搜索摘要代替正文。

| 官方资料 | 与本设计相关的结论 |
| --- | --- |
| [Claude Code：Subagents](https://code.claude.com/docs/en/sub-agents) | 子 Agent 在同一 session 内拥有独立上下文、工具和任务指令，通常向调用方交付结果。普通子 Agent 从新上下文开始，fork 可以继承已有对话。子 Agent 有独立 ID 和 transcript，可以继续执行。 |
| [Claude Code：Agent teams](https://code.claude.com/docs/en/agent-teams) | Lead 负责拆分与汇总，teammate 有独立上下文，通过 mailbox 和共享任务列表协调。任务依赖与成员关系是显式状态；不是让所有 Agent 共用一份聊天历史。 |
| [Claude Code：Worktrees](https://code.claude.com/docs/en/worktrees) | Worktree 是文件修改隔离：独立目录和分支，共享 Git 历史。子 Agent 可以选择 `isolation: worktree`，它与是否使用子 Agent 是两个不同维度。 |

### 1.1 Claude Code 的版本相关限制

当前 subagents 正文明确允许子 Agent 继续生成子 Agent，默认最大深度为主对话以下 3 层，普通并发限制默认 20；两者可配置。因此，不能继续采用旧文章中的“子 Agent 一律不能再生成子 Agent”作为设计依据。当前命名子 Agent 也可通过消息协作，不宜把 subagents 简化为绝对无通信能力的函数调用。[来源](https://code.claude.com/docs/en/sub-agents#let-subagents-spawn-their-own-subagents)

Agent teams 页面仍将该能力标为实验性，列出同一 session 一个 team、不可嵌套 team、in-process teammate 随 session 恢复存在限制等问题。页面也提醒多人改同一文件会覆盖，应按文件划分任务。它证明了协调层与故障恢复需要单独设计，并不保证本项目照搬后即可获得生产可靠性。[来源](https://code.claude.com/docs/en/agent-teams#limitations)

Worktree 页面说明子 Agent 可以显式申请独立 worktree；有修改的 worktree 需要保留，活动期间还需防止清理。因此“增加工作目录”和“增加容器/虚机”不能混成一件事：前者是编辑隔离，后者是安全与资源隔离。这一抽象区分是本项目的设计推论。[来源](https://code.claude.com/docs/en/worktrees#isolate-subagents-with-worktrees)

## 2. Codex：已验证部分与本次访问限制

已实际请求旧版官方入口，服务返回 HTTP 308：

| 请求入口 | 返回的新地址 | 本次结果 |
| --- | --- | --- |
| `https://developers.openai.com/codex/multi-agent` | [Subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents) | 重定向可验证，新页面 TLS/连接失败，未读取正文 |
| `https://developers.openai.com/codex/app/worktrees` | [Git worktrees](https://learn.chatgpt.com/docs/environments/git-worktrees) | 重定向可验证，新页面无法获取正文 |

备用搜索连接器提示不可用；PowerShell、curl 和内置浏览器均未能读取 `learn.chatgpt.com` 正文。因此本文不声称已从远端文档确认当前 Codex 的内部数据库表、容器粒度、队列实现或所有版本的默认行为。

本次 Codex 执行环境直接提供的工具契约仍可作为有限的运行时证据：

- `spawn_agent` 返回独立 Agent 身份，支持父子任务路径，以及继承全部历史或从新上下文开始。
- `send_message`、`followup_task`、`wait_agent`、`interrupt_agent` 将消息、继续执行、等待、停止分成独立操作。
- 本会话的子 Agent 明确共享工作目录，修改对其他 Agent 立即可见；本次调研 Agent 即在这个机制下运行。
- 工作树由独立的 `create_worktree` / `attach_worktree` 工具创建或复用，而不是每次生成子 Agent 都必须创建。

这些是本次环境的接口和实际协作行为，不能推导为所有 Codex 部署的内部实现。可以借鉴的是“独立执行身份、独立上下文、显式通信、可选工作区隔离”的边界。

## 3. 对本项目的建议（设计判断）

### 3.1 采用分层组合，不做 orchestration 与共享状态二选一

建议采用 **父 Agent 编排 + 子 Agent 独立上下文 + 持久化共享任务状态 + 显式消息传递**。这是四个互补层：

1. 编排层决定分工、依赖、等待、取消和汇总，首期保留一个 root 作为最终答复责任人。
2. 对话层让每个执行单元保留自己的消息序列。只把任务说明、必要背景和结果送给其他 Agent，不广播所有 token 或工具输出。
3. 状态层保存状态机、父子关系、结果、失败、预算与资源绑定。数据库是状态事实来源，内存 goroutine/channel 只提供运行时加速。
4. 资源层按 workspace 身份复用沙箱；是否隔离代码修改由 workspace 策略决定，是否隔离安全边界由 sandbox 策略决定。

共享状态不是一份任意 Agent 都能覆写的巨大 JSON。任务认领、状态转换和取消应使用条件更新/版本号；模型通过受控工具操作，不直接改数据库。

### 3.2 session、Agent 与沙箱必须解耦

现有 `backend/pkg/sandbox/sandbox.go` 的 Driver 全部以 `sessionID` 定位资源；Docker/E2B 也按这个键创建资源。若为每个子 Agent 造一个新 session，然后直接沿用现有调用，自然就会误建多个沙箱。

最低改造是让每个子 Agent 持有独立执行身份和父子关系，但在工具执行上下文中传递 root 的 workspace/resource key。原来的 session key 可以暂时兼任 root workspace key，无需立即重写三个 Driver。应明确这是兼容层，而不是永久认为 session 与 workspace 必然一一对应。

关系记录至少需要稳定 ID、所属 root session、parent ID、上下文归属、workspace key、角色、任务、状态、起止时间、错误/结果。只有 `parent_agent_id` 不足以可靠恢复、鉴权或清理。后续若要保留一个 Agent 多次运行的历史，应进一步区分稳定 Agent 身份和每次 run/attempt；不要把“继续执行”和“新建 Agent”混用。

### 3.3 默认共用 root 工作区，按需求增加隔离

| 场景 | 工作区策略 | 沙箱策略 |
| --- | --- | --- |
| 研究、审查、互不重叠的文件编辑 | 共享 root workspace | 复用 root 沙箱 |
| 同仓库并行改相同文件、独立试验不同方案 | 可选 Git worktree / 独立目录，显式合并 | 可在同一安全边界内复用沙箱 |
| 不同用户/租户、不同凭据或不可信代码 | 独立 workspace | 独立安全沙箱 |

因此“每个 Agent 一个沙箱”没有必要，但“多个 sandbox 永远不合理”也不成立。边界由信任域与执行需求决定，而不是 Agent 数量。

共享工作区首期建议一个写入者、多个读者，或明确分配互不重叠的文件责任。仅给 `WriteFile` 加锁不等于解决并发冲突：Bash/Python 仍然能够修改文件，读后写也可能覆盖另一 Agent 的修改。任意 shell 写入在共享模式下需要执行级互斥或明确接受协作约定的局限。

### 3.4 应优先补齐的工程约束

- 资源键只能由服务端从 root 关系导出；子 Agent 的模型参数不能指定任意他人的 workspace/session。
- root 的租户、用户、预算、工具权限向下继承，子 Agent 可以缩小权限，不能放大；Agent 消息不能充当用户授权。
- 停止一个子 Agent 只停止其任务/进程，不得调用共享沙箱的 `Close`；删除 root 时先取消并等待所有子任务退出，再清理资源。
- root 进入等待子任务时不应占满工作者池，否则所有并发槽被父任务占用会死锁；深度、并发和总生成数必须分别限额。
- 附件只需在共享工作区正确物化一次；Python 临时文件、技能目录、导出路径和日志需考虑多个执行单元的命名冲突。
- 重启后将无法继续的 running 记录标为 interrupted，而不是自动重放有副作用的工具；用户可查看结果并显式继续。
- SSE 事件携带执行身份与 root session，聊天流展示汇总，子 Agent 详情展示完整过程，避免将并行输出混成一条 assistant 消息。

推荐先做可审计的分层委派，再逐步增加兄弟 Agent 邮箱、DAG 任务队列和 worktree。实现复杂度应由真实使用场景推动。
