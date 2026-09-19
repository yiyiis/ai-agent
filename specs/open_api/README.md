# 阶段六：开放 API 与分布式任务调度技术规范 (Open API & Async Run Dispatcher)

> **版本**：v1.1.0  
> **所属阶段**：系统架构阶段六 (Phase 6)  
> **核心组件**：开放 API 网关、多租户 RBAC、API Key 认证鉴权、分布式异步任务状态机、容灾重试调度引擎  

---

## 1. 架构总览与设计目标

阶段六致力于将平台内部的 Skill 执行能力与 Agent 驱动力对外开放，使第三方系统、自动化流水线（CI/CD、Cron、企业内部应用）能够通过标准化的 RESTful API 异步调用平台的各项专业技能，并具备工业级的多租户隔离、并发限制与容灾自愈能力。

### 1.1 核心设计目标

1. **企业级租户与权限隔离 (Multi-Tenant & Security Isolation)**  
   - 引入企业租户层（`companies`），支持不同企业注册相同用户名（联合唯一索引 `(company_id, username)`）；
   - 建立清晰的三级 RBAC 矩阵：超级管理员 (1)、企业管理员 (2)、普通员工 (3)；
   - 外部请求通过 `api_keys` 进行严格鉴权，绑定归属企业（`company_id`）、最大并发限制（`max_concurrent`）、以及技能白名单（`allowed_skills`）。

2. **生产级异步任务模型 (Async Job & State Machine)**  
   提供标准的“提交任务 (`POST`) -> 轮询结果 (`GET`) / 取消任务 (`POST /cancel`)”异步模型，规避 HTTP 长连接超时与网络抖动。

3. **三类运行模式全链路对齐 (Script vs Headless vs Headed)**  
   - **纯脚本模式 (`mode = script`)**：直接在沙箱执行，零 Token 开销，不产生空会话与消息污染；
   - **大模型无头模式 (`mode = llm, user_id = 0`)**：后端程序调用，创建内部无头会话，不污染员工个人对话，企业管理员可追溯审计；
   - **大模型有头模式 (`mode = llm, user_id > 0`)**：外部系统代理企业指定员工发起真实会话，员工登录网页端立即可见并支持无缝继续对话。

4. **容灾重试与故障分类器 (Error Classifier & Exponential Backoff)**  
   构建智能错误分类器，自动识别可恢复错误与不可恢复错误，对可恢复错误启用指数退避重试。

5. **全链路审计与运维可见性 (Audit & Observability)**  
   `skill_runs` 表留存完整快照，包含入参快照、最终产物、耗时、多轮重试快照（`attempts`）与控制台末尾日志。

6. **契约驱动与解耦抽取 (Manifest-driven Contract & Decoupled Extraction)**  
   全面引入 `manifest.json` 契约体系：网关层依据 `input_schema` 前置强校验拦截非法入参（返回 400）；执行引擎通过标准输入（`stdin: params_json`）管道向沙箱安全注参，并利用结构化抽取器（`result_extractors` 正则/文件）使纯净业务数据与脚本调试日志彻底解耦，同时对无 manifest 历史技能保持 100% 平滑降级兼容。

---

## 2. 总体架构拓扑图

```mermaid
flowchart TD
    Client[外部应用 / CI流水线 / 第三方系统] -->|携带 X-API-Key / Bearer| APIGateway[开放 API 网关 /api/v1/*]
    
    subgraph Gateway [网关鉴权与准入层]
        APIGateway --> KeyAuth{API Key 校验}
        KeyAuth -->|有效| TenantCheck[公司/租户隔离校验]
        TenantCheck --> WhitelistCheck{技能白名单匹配?}
        WhitelistCheck -->|拒绝| Err403[返回 403 Forbidden]
        WhitelistCheck -->|通过| ManifestCheck{manifest: api_enabled & input_schema 校验}
        ManifestCheck -->|未启用或参数不合规| Err400[返回 400 Bad Request]
        ManifestCheck -->|通过| ConcurrencyGuard{并发数 < max_concurrent?}
        ConcurrencyGuard -->|超限| Err429[返回 429 Too Many Requests]
    end

    ConcurrencyGuard -->|准入| Dispatcher[异步任务调度器 (Dispatcher)]

    subgraph Core [调度与执行层]
        Dispatcher --> CreateRun[生成唯一 run_id 并初始化状态 pending]
        CreateRun --> WorkerPool[分布式/后台 Worker 工作池]
        
        WorkerPool --> ModeCheck{执行模式 mode}
        ModeCheck -->|script (纯脚本)| SandboxExec[沙箱执行: stdin 管道注入 input.json / 占位符渲染]
        ModeCheck -->|llm (模型推理)| LLMBranch{有头 or 无头?}
        LLMBranch -->|有头 user_id > 0| HeadedExec[物化真实用户 Session 并执行 Agent]
        LLMBranch -->|无头 user_id = 0| HeadlessExec[物化无头审计 Session 并执行 Agent]
        
        SandboxExec --> InspectResult[结果抽取器: 正则提取 API_RESULT / 文件上传 OSS]
        HeadedExec --> InspectResult
        HeadlessExec --> InspectResult
    end

    subgraph Resiliency [容灾与状态收敛]
        InspectResult --> CheckSuccess{执行是否成功?}
        CheckSuccess -->|成功| SetSuccess[状态置为 success 并写入 result/artifacts]
        CheckSuccess -->|失败| Classifier[故障分类器 Failure Classifier]
        
        Classifier --> Retryable{是否属于可恢复故障 & attempt < max_attempts?}
        Retryable -->|是| Backoff[记录 attempt 快照并计算指数退避时间]
        Backoff --> ScheduleRetry[排队等待下次重试]
        ScheduleRetry --> WorkerPool
        Retryable -->|否| SetFailed[状态置为 failed，保留现场 diagnostics]
    end

    SetSuccess --> DoneDB[(持久化入库 skill_runs)]
    SetFailed --> DoneDB
    Client -.->|轮询 GET /api/v1/skill-runs/:run_id| DoneDB
```

---

## 3. 文档导航与索引

详细设计与实现细节已拆分为以下各专属技术规范文档，请参阅：

- [数据模型与数据库字段规范 (database.md)](./database.md)  
  包含 `companies`、`user_info`、`api_keys` 与 `skill_runs` 的完整建表 DDL、字段类型约束、联合唯一索引与三类运行模式映射。
- [接口契约与 API 规范 (api.md)](./api.md)  
  包含外部开放接口（`/api/v1/skill-runs` 系列）、多租户 RBAC 控制台会话接口（`/api/sessions?scope=all`）与多租户认证端点定义。
- [调度器与容灾重试引擎规范 (dispatcher.md)](./dispatcher.md)  
  包含并发守卫机制、Worker 队列调度流程、双模执行路径（Script vs LLM）、故障智能分类器以及指数退避重试算法。
