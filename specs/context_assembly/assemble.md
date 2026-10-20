# 分区组装规范（热/冷分区 · 轮内 Rezone · 图片策略）

> 所属模块：[上下文组装治理](./README.md)  
> 代码位置：`backend/pkg/agent/agent.go`（buildContext / ReAct 循环）、`backend/pkg/agent/assemble.go`（zoneBackward / rezoneMsgs）、`backend/pkg/ctxpolicy/`（策略）、`backend/pkg/provider/context_window.go`（窗口表）

---

## 1. 分区判定核心 —— `zoneBackward(items, model)`

统一的分区决策函数，两个适配器共用（保证行为一致）：

1. **跨轮组装**：buildContext 的历史行（读治理列）；
2. **轮内 rezone**：ReAct 循环预算超线时对内存 msgs 整体替换。

### 1.1 step 编号（前向）

- 含 `tool_calls` 的 assistant 条目**开启一个 step**（step = 一次上游调用）；
- 其后的 tool 条目归属**最近的批次**；
- 其余条目（user / 纯文本 assistant / system / 悬空合成行 / 无配对的 tool 行）标记为叙事，`isPayload=false`，**不参与分区、不占预算、永不替换**。

### 1.2 反向分区（粘性边界）

```
budget = ContextWindow(model) × HotBudgetRatio(0.25)
hotZone = true
从最新条目向最旧遍历：
  forceStub（ctx_mode='stub'）        → 冷区（无视一切）
  distance == 0（in-flight 最新 step） → 原文（计入 used，但不触发挤出）
  !hotZone                             → 冷区
  distance ≥ 该条目阈值                 → 冷区，hotZone = false（粘性）
  used + tokensFull > budget           → 冷区，hotZone = false（粘性）
  否则                                 → 热区，used += tokensFull
```

**粘性边界是缓存友好的关键**：一旦触顶，更旧条目全部进冷区，不再"恢复"——同样的输入永远产出逐字节相同的结果（有确定性测试钉住），分区边界只在新增 step 时前移。

### 1.3 阈值规则

| 条目 | 阈值取法 |
|---|---|
| assistant 批次 | 批内各调用工具阈值的 **max**（stub 版里小参数本来就原样，宽阈值只影响 write 载荷多留一步，预算兜底） |
| tool 行 | 按自身工具名查表 |
| 默认（`ctxpolicy.HotSteps`） | read_file:2 / write_file:1 / edit_file:1 / bash:3 / python_exec:3，未列工具 `DefaultHotSteps:8` |

预算**只统计 payload 行**（工具参数与结果）；叙事行（user 文本、assistant 纯文本）永远原文、不占预算——叙事是任务意图与推理主干，裁它归未来的 compaction。

## 2. buildContext 分区选择器（跨轮组装）

入口逻辑逐行保留原有行为：**悬空修复 flushDangling**（轮次边界合成 `[未执行]` 占位，保证配对合法）、**孤儿/重复 tool 行去重**。变化是走查时为每行构建：

- `ctxRow`：消息 + 冷区覆盖值；
- `zoneItem`：分区元数据（role / calls / toolName / tokensFull / forceStub）。

### 2.1 冷区口径三来源（tool 行）

1. `stub_content` 列非空 → 用它；
2. 列为空 **且** `tokens_full == 0`（**旧数据判定**：治理列缺失的存量行）→ 现场用**同一套规则函数**生成（配对参数经 `curCalls`（当前批次 callID → 原调用映射）取得）；
3. 列为空且新数据 → content 即口径（错误/短结果，生成规则保证 stub=content 时写 NULL）。

assistant 行同理：`stub_tool_calls` 列优先，缺失则 `stubForToolCalls(calls)` 现场生成。**免迁移**：存量会话行为自动对齐。

### 2.2 历史图片保留策略

两遍扫描：预收集全部历史 user 行的图片附件位置 → 仅最近 `ImageKeepLast(2)` 张进保留集。拼装时：
- 保留集内 → 走现有 `resolveAttachmentImageURL`（磁盘读 / 下载 / base64 / 公网直通）→ `image_url` part；
- 保留集外 → 文本 part `[图片已省略: 文件名]`，且**不调用** URL 解析（被裁图片连磁盘读取和 base64 编码都不发生——每轮省 CPU）。

**当前轮用户消息不经过 buildContext**（`buildMultimodalUserMessage` 在其后拼装），当前轮图片永远全量注入。

### 2.3 渲染

`applyCold` 闭包在分区判定时把冷区行切到 stub，然后输出 msgs。`ctx_mode='stub'` 优先于最新 step 豁免；大参数与非法参数原文不进入模型请求，数据库原文保持不变。旧行即便没有模式标记，也会按参数大小/合法性强制安全回放。

## 3. ReAct 轮内 Rezone —— `rezoneMsgs(msgs, model)`

```
循环内原始 calls 负责落库和执行；assistantContextMessage 负责内存回放：
  ctx_mode=stub → append stubForToolCalls(calls)，立即生效，不等待 rezone 水位
  其余 → append 正常 calls
round 边界（构造 req 之前）：
  lastPrompt = 上一轮 streamOneRound 返回的 usage.PromptTokens（精确值，滞后一轮）
  if lastPrompt > ContextWindow(session.Model) × RezoneRatio(0.65)：
      rezoneMsgs(msgs, session.Model)   // 用真实模型窗口整体分区
```

rezoneMsgs 内部：
- 从内存 msgs 建 `callID → savedCall` 映射（tool 行冷区生成 stub 需要配对参数）；
- tool 行无配对调用（悬空/合成）→ 叙事化，不动；
- applyCold 用**与落库同一套 stub 函数**现场生成——幂等（`_pruned`/`_rejected`/"已省略"守卫），重复调用结果不变（有测试钉住）；
- 内存消息携带不参与模型协议序列化的 `CtxMode`；强制 stub 优先于最新 step 豁免，其他 in-flight 与叙事行不动。

**纪律**：批量一次、替换后前缀只增不变（0.65 迟滞带保证不频繁触发）；首轮不触发（`lastPrompt == 0`），由 buildContext 分区兜底。预算检查的存在解决"单轮 ReAct 累积撑爆 → 全量已落库 → 重试继续爆 → 会话卡死"的生存性问题。

## 4. 一次请求的最终形态

```
[system：会话提示 + 模型说明 + 文件操作规范 + 技能目录]      ← 恒原文
[历史·叙事：user / 纯文本 assistant]                        ← 恒原文
[历史·冷区：stub_tool_calls + stub_content + 错误原文 + 旧图文本占位]
[历史·热区：最近 ≤K step 且 ≤25% 窗口，content 原文 + 最近 2 张真实图]
[当前轮：用户消息 + 全部当前图片]
[本轮新消息：assistant 按 ctx_mode 立即选择参数版本；tool 结果进场有界]
```

## 5. 配置

### 5.1 `etc/config.yaml`

```yaml
LLM:
  ContextWindows:            # 模型窗口表（Billing 同款"yaml 切片→运行时 map"先例）
    - {Model: "GLM-5.3", Window: 131072}
    - {Model: "MiniMax-Text-01", Window: 1000384}
AgentPolicy:                 # 分区策略（零值/缺段保持默认）
  HotSteps:
    - {Tool: "read_file", Steps: 2}
    - {Tool: "write_file", Steps: 1}
    - {Tool: "edit_file", Steps: 1}
    - {Tool: "bash", Steps: 3}
    - {Tool: "python_exec", Steps: 3}
  DefaultHotSteps: 8
  HotBudgetRatio: 0.25
  RezoneRatio: 0.65
  ImageKeepLast: 2
```

### 5.2 装载链路与匹配次序

- `main.go`：`provider.InitProviders(conf.LLM)`（内部 `SetContextWindows`）→ `ctxpolicy.Init(conf.AgentPolicy)`；
- `ContextWindow(model)`：精确（小写化）→ **前缀双向**（glm-5.3-x 吃 glm-5.3 的配置）→ 默认 131072；
- `config.local.yaml` 是**整体替换**不是合并——缺 AgentPolicy/ContextWindows 段时 `Init` 的零值合并保持默认，完全向后兼容。

### 5.3 `pkg/ctxpolicy` 叶子包

策略结构独立成包（而非放 agent 包）是**为了打破导入环**：`config` 需组合策略结构，而 `agent_test.go` 需 import `config` 加载 DB 连测试本地 MySQL。叶子包两侧都可依赖。

## 6. 测试基线

纯函数测试（无需 DB）：stub 形态/幂等/拦截置换（`stub_test.go`）、分区边界/预算挤出/force_stub/旧数据回退/图片 last-2/rezone 幂等/组装逐字节确定性（`assemble_test.go`）、工具截断与落盘（`tools_test.go` 增补）。原有悬空修复、多模态回放、Run 系（真实 MySQL）用例全部回归。
