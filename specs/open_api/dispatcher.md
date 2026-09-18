# 异步调度器与容灾重试引擎规范 (Dispatcher Specification)

> **版本**：v1.0.0  
> **核心组件**：
> - 调度引擎核心：[`backend/pkg/dispatcher/dispatcher.go`](../../backend/pkg/dispatcher/dispatcher.go)
> - 故障分类与退避：[`backend/pkg/dispatcher/classifier.go`](../../backend/pkg/dispatcher/classifier.go)
> - 独立执行器桥接：[`backend/pkg/dispatcher/executor.go`](../../backend/pkg/dispatcher/executor.go)

---

## 1. 任务调度架构与生命周期

异步任务调度器（`Dispatcher`）作为系统的后台工作引擎，负责解耦外部 API 的 HTTP 请求生命周期与沙箱/大模型的耗时计算。

### 1.1 调度引擎拓扑

```mermaid
flowchart TD
    Submit[POST /api/v1/skill-runs] --> DBInit[(写入 skill_runs: pending)]
    DBInit --> NotifyChan[向调度通道 TaskChan 投递 TaskID]
    
    subgraph DispatcherLoop [调度引擎主循环]
        NotifyChan --> DispatchQueue
        PollTicker[定时补偿 Poller (10s)] -.->|捕获异常漏单| DispatchQueue
        
        DispatchQueue --> SelectWorker{WorkerPool 是否有空闲槽位?}
        SelectWorker -->|有空闲| SpawnWorker[派发给 Worker Goroutine]
        SelectWorker -->|饱和排队| WaitSlot[等待槽位释放]
    end

    subgraph Execution [Worker 执行单元]
        SpawnWorker --> MarkRunning[更新 DB: status = running, started_at]
        MarkRunning --> PrepareSandbox[挂载沙箱 & 注入机密]
        PrepareSandbox --> ExecLogic{判断执行模式}
        
        ExecLogic -->|mode = script| DirectScript[沙箱直接调用脚本]
        ExecLogic -->|mode = llm| LLMReAct[拉起轻量 Agent 轮次]
        
        DirectScript --> CollectOutput[采集 Stdout/Stderr/耗时/工件]
        LLMReAct --> CollectOutput
    end

    CollectOutput --> EvalResult{退出码与输出分析}
    EvalResult -->|0 且无致命错误| MarkSuccess[更新 DB: status = success, result]
    EvalResult -->|异常| FailurePipeline[进入容灾收敛管线]
```

---

## 2. 并发限制与准入控制 (Concurrency Guard)

### 2.1 基于 API Key 的并发准入
每个企业签发的 API Key 具备硬性配额 `max_concurrent`（默认 3）。在任务进入 `pending` 状态前，网关执行快速原子检查：
```sql
SELECT COUNT(1) FROM skill_runs 
WHERE api_key_id = ? AND status IN ('pending', 'running');
```
- 若当前激活任务数 $\ge \text{max\_concurrent}$，直接返回 `429 Too Many Requests`，拒绝入库，防止单一租户并发洪峰击穿系统沙箱池。

---

## 3. 双模执行引擎 (Execution Modes)

技能在设计时可以通过 `SKILL.md` frontmatter 或配套 `manifest.json` 声明其执行倾向：

### 3.1 纯脚本直连模式 (`mode = script`)
适用于确定性强、计算型或已有成熟代码的技能（如文本格式化、微积分计算、爬虫脚本）：
1. **沙箱分配**：为该任务申请独立隔离沙箱环境；
2. **参数注入**：输入参数 `params` 转换为 JSON，通过环境变量 `SKILL_PARAMS_JSON` 或命令行 `--params '<json>'` 注入脚本进程；
3. **机密合并**：从宿主机 `.secrets/<dir_name>.env` 读取该技能私密配置注入进程环境；
4. **输出解析**：脚本约定在标准输出（Stdout）打印 JSON 结果，调度器捕获后直接解析为 `result` 实体；生成的临时文件从沙箱指定目录打包提取为 `artifacts`。

### 3.2 大模型 Agent 编排模式 (`mode = llm`)
适用于需要语义理解、多步骤推理或模糊指令的技能（如合同审查、多源信息归纳）：
1. 调度器初始化一个专有临时会话，将该技能以 `full` 挂载；
2. 构造针对性系统任务提示词，将 `params` 作为用户需求输入给大模型；
3. 模型在沙箱中按需调用脚本工具并输出最终总结，整个过程结构化收敛为 `result`。

---

## 4. 故障智能分类器 (Error Classifier)

调度器在任务退出后，通过检查退出码、标准错误流关键字、沙箱驱动错误与大模型 HTTP 状态码，对错误进行多维分类：

```go
type FailureKind string

const (
    FailureKindParamInvalid FailureKind = "param_invalid" // 参数 Schema 校验失败
    FailureKindScriptError  FailureKind = "script_error"  // 脚本代码逻辑崩溃
    FailureKindTimeout      FailureKind = "timeout"       // 执行超时强杀
    FailureKindSandbox      FailureKind = "sandbox"       // 沙箱容器启动/文件IO异常
    FailureKindRateLimited  FailureKind = "rate_limited"  // 上游 LLM 触发 429 限流
    FailureKindNetworkError FailureKind = "network_error" // 外部依赖网络抖动丢包
)
```

### 4.1 自动重试决策表

| 故障类别 | 是否可重试 (`isRetryable`) | 自动重试依据 |
| :--- | :--- | :--- |
| `param_invalid` | **否** | 输入参数错误，重试无意义，直接终结并指明错误字段。 |
| `script_error` | **否** | 脚本代码语法或业务抛错，重试结果恒等，直接失败。 |
| `timeout` | **否**（默认） | 已经消耗满额超时时间，重试往往继续超时（除非显式配置重试）。 |
| `sandbox` | **是** | 沙箱容器守护进程瞬时冷启动超时，重试往往能恢复。 |
| `rate_limited` | **是** | 大模型或上游 API 瞬时限流，退避等待后重试大概率成功。 |
| `network_error` | **是** | 瞬时 DNS 解析抖动或网络丢包，具备自愈特征。 |

---

## 5. 指数退避重试算法 (Exponential Backoff with Jitter)

当任务执行失败且被认定为 **可重试故障**，且当前 `attempt < max_attempts` 时，调度器不会立即重跑，而是启动带抖动的指数退避策略：

### 5.1 退避延迟计算公式

$$\text{BackoffDelay} = \min\left(\text{BaseInterval} \times 2^{(\text{attempt} - 1)} + \text{Jitter},\; \text{MaxInterval}\right)$$

* **基准间隔 ($\text{BaseInterval}$)**：$2$ 秒
* **最大间隔 ($\text{MaxInterval}$)**：$60$ 秒
* **随机抖动 ($\text{Jitter}$)**：$[0, 1000]\text{ ms}$ 的随机值，防止多个并发失败任务在同一时刻发生“重试惊群”

### 5.2 状态转移与现场保护
1. 当前尝试的现场信息打包为 `SkillRunAttempt` 快照：
   ```go
   attemptSnapshot := SkillRunAttempt{
       Attempt: run.Attempt,
       FailureKind: failureKind,
       Error: errSummary,
       DurationMs: durationMs,
       StartedAt: run.StartedAt,
       FinishedAt: time.Now(),
       StdoutTail: tail(stdout, 4000),
       StderrTail: tail(stderr, 4000),
   }
   ```
2. 将 `attemptSnapshot` 追加至数据库 `attempts` 列表中；
3. `attempt` 自增 1；
4. 计算 `next_retry_at = time.Now().Add(delay)`，保持状态为 `pending`；
5. 定时轮询器在 `time.Now() >= next_retry_at` 时再次拣选该任务投入 WorkerPool。
