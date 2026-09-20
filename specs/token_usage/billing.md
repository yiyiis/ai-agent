# Token Usage 记账引擎与计费规范 (Recording & Billing Specification)

> **版本**：v1.0.0  
> **核心代码位置**：
> - 唯一写入口：[`backend/pkg/agent/usage_record.go`](../../backend/pkg/agent/usage_record.go)
> - ReAct 埋点：[`backend/pkg/agent/agent.go`](../../backend/pkg/agent/agent.go)（`Run` 循环）
> - 归属解析：[`backend/pkg/provider/registry.go`](../../backend/pkg/provider/registry.go)（`ProviderKeyForModel`）
> - 计价器：[`backend/pkg/provider/pricing.go`](../../backend/pkg/provider/pricing.go)
> - 单价配置：[`backend/etc/config.yaml`](../../backend/etc/config.yaml)（`LLM.Billing` 段）
> - 数据访问：[`backend/dao/token_usage.go`](../../backend/dao/token_usage.go)

---

## 1. 唯一写入口契约：`recordTokenUsage`

全系统所有 `token_usages` 写入**必须且只能**经由 `pkg/agent.recordTokenUsage` 收敛，禁止任何业务代码直接 `Create` 该表：

```go
// recordTokenUsage 记账是旁路：上游没回 usage 或全零不写空行；
// 落库失败仅告警，绝不打断对话（与 saveMessage 同款 WithoutCancel 模式）
func recordTokenUsage(ctx context.Context, session *model.Session, round int,
    usage *provider.Usage, purpose constants.TokenUsagePurpose)
```

### 1.1 旁路铁律（Bypass Rules）

| 规则 | 行为 | 理由 |
| :--- | :--- | :--- |
| 空 usage 不落行 | `usage == nil` 或 `prompt+completion == 0` 直接 return | 上游不回 usage 时写零行只会污染统计 |
| 落库失败不打断 | `slog.Warn("记录 Token 用量失败", ...)` 后正常返回 | 记账永远不能成为对话失败的原因 |
| 断连/中止仍落账 | `dao.CreateTokenUsage(context.WithoutCancel(ctx), ...)` | Turn 在后台 Goroutine 托管，客户端取消不应丢账 |
| 未匹配模型仍落账 | `provider_key` 记空串 | 模型下线不能导致漏账 |

### 1.2 字段装配

`session *model.Session` 自带全部身份字段，agent 层零额外查询：

| 列 | 来源 |
| :--- | :--- |
| `session_id` / `company_id` / `user_id` / `model` | `session` 结构体直读（无头任务由 dispatcher 物化会话时已写入调用方归属） |
| `provider_key` | `provider.ProviderKeyForModel(session.Model)` |
| `round_index` | 调用方传入的 ReAct 循环变量 `round`（从 0 起） |
| `purpose` | 调用方传入的用途枚举 |
| 四路 token | `usage` 结构体直读 |

---

## 2. ReAct 循环逐轮记账时序

```mermaid
sequenceDiagram
    autonumber
    participant Loop as agent.Run (round=0..31)
    participant SR as streamOneRound
    participant Rec as recordTokenUsage
    participant DB as token_usages
    participant FE as SSE 前端

    loop 每轮 LLM 调用
        Loop->>SR: 提交上下文流式推理
        SR-->>Loop: content/calls/finish + lastUsage
        Note over Loop: 埋点位于异常分支之前:<br/>异常中止 / length 截断 / ctx 取消的轮次<br/>同样带回已捕获的 lastUsage,照常记账
        Loop->>Rec: recordTokenUsage(round, usage, turn)
        Rec->>DB: INSERT 一行 (WithoutCancel)
        Note over Rec: 失败仅 slog.Warn,吞错返回
        Loop->>Loop: turnUsage = sumUsage(turnUsage, usage)
        alt 流正常结束 (无 tool_calls)
            Loop-->>FE: done 事件 (usage = 整轮累计)
        else 继续工具循环
            Loop->>Loop: 执行工具,追加结果,进入下一轮
        end
    end
```

### 2.1 关键时序约束

1. **记账先于异常分支**：`streamOneRound` 返回后立即记账，**再**进入 `streamErr != nil` / `finish == "length"` 判断。中途失败的轮次已经向上游烧掉了 token，记在 Turn 收尾会漏账。
2. **done 事件带整轮累计**：SSE `done` 事件的 `usage` 字段为各轮 `sumUsage` 求和（跨轮累计的 prompt/completion/cached/reasoning），而非仅最后一轮；异常/截断/死循环路径的 done 不带 usage（与历史行为一致）。
3. **一处埋点双链路覆盖**：交互聊天（`api/chat.go` → Turn Registry → `agent.Run`）与开放 API 无头任务（`dispatcher/executor.go` → 物化 `run_<run_id>` 会话 → 同一 `agent.Run`）共用此埋点，无头任务用量自动归属调用方企业。

---

## 3. provider_key 归属算法：`ProviderKeyForModel`

```go
// ProviderKeyForModel 返回模型的计费归属 provider 名；匹配不到返回空串（仍记账，归属留空）
func ProviderKeyForModel(model string) string
```

| 匹配步骤 | 规则 |
| :--- | :--- |
| 1. 精确匹配 | `modelRoutes[model]` 命中直接返回 |
| 2. 大小写不敏感 | `strings.EqualFold` 逐项比对 |
| 3. 前缀推断 | 小写前缀 `glm` / `minimax` 命中对应 provider |
| 4. 不兜底 | 全部未命中返回 `""` |

**与 `GetProviderForModel` 的关键差异**：后者是可用性优先（回退首个 provider 保证对话不中断），前者是**准确性优先**（计费归属不允许张冠李戴，未匹配宁可留空）。分账场景下，不同 provider 挂不同 API Key / 不同账户时，错误的归属比空归属危害更大。

---

## 4. 计价配置与查询时换算

### 4.1 单价表配置（`etc/config.yaml` → `LLM.Billing`）

```yaml
LLM:
  DefaultModel: "MiniMax-Text-01"
  Providers: [...]
  # 计费单价表：每百万 token 单价（元）。查询时换算，不回写历史账单；
  # 未配置的模型金额记 0，token 计数照常统计。
  Billing:
    - Model: "MiniMax-Text-01"
      Input: 2.0            # 未命中缓存的输入单价
      Output: 8.0           # 输出单价（reasoning 含在输出内，不另计）
      CachedInput: 0.2      # 缓存命中输入单价（缺省按 Input 计）
    - Model: "GLM-5.3"
      Input: 2.0
      Output: 8.0
```

- 配置挂在 `LLM` 段下（`provider.Config.Billing`），与模型路由同源同生命周期，`main` 启动时随 `LoadConfig` 一次载入，运行期只读。
- 单价单位统一为 **元 / 百万 token**。

### 4.2 换算公式（`provider.Cost`）

```go
// Cost 估算一次聚合的成本（元）：cached ⊂ prompt 扣减后计价，reasoning ⊂ completion 不重复计
func Cost(model string, prompt, cached, completion int64) float64
```

```text
uncached      = max(prompt - cached, 0)
cost_estimate = uncached / 1e6 × Input
              + cached    / 1e6 × (CachedInput 或 Input,当 CachedInput 未配置/为 0)
              + completion/ 1e6 × Output
```

- **调用位置约束**：换算只发生在 API 层（`api/token_usage.go`）组装响应时；DAO 返回纯 token 聚合——**账（token）与价（price）分离**，价格变更不影响数据层。
- **未配置单价**：返回 `0`，统计接口 token 照常展示、金额为 0，不报错。
- **命名语义**：响应金额字段统一叫 `cost_estimate`，明示是估算值而非账单。

---

## 5. purpose 扩展规范（阶段七后续模块）

后续模块新增 LLM 调用点时，**必须**复用 `recordTokenUsage` 并在 `constants` 补充对应枚举：

| 模块 | purpose | 埋点要求 |
| :--- | :--- | :--- |
| 阶段七·模块二 历史压缩 | `compact` | digest 生成 LLM 调用返回 usage 后立即记账（round=0，model=压缩档模型） |
| 阶段七·模块三 记忆抽取 | `extract` | 后台抽取任务 LLM 调用返回 usage 后记账（round=0，owner 用会话归属） |

禁止绕过写入口直接 INSERT，禁止复用 `turn` 语义承载其他用途——purpose 是成本归因的一级维度，混用会让"对话成本 vs 系统开销"不可分割。
