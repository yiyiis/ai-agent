# 开放 API 与多租户管理端点契约规范 (API Specification)

> **版本**：v1.1.0  
> **协议**：HTTP/1.1、HTTP/2  
> **认证体系**：
> - 外部开放 API：`X-API-Key: <sk_...>` 或 `Authorization: Bearer <sk_...>`
> - 内部管理 API：`Authorization: Bearer <JWT_TOKEN>` (同 Web 会话)  
> **相关代码目标位置**：
> - 开放接口层：[`backend/api/open_api.go`](../../backend/api/open_api.go)
> - 控制台管理层：[`backend/api/skill_runs.go`](../../backend/api/skill_runs.go)
> - 会话与交互层：[`backend/api/sessions.go`](../../backend/api/sessions.go)、[`backend/api/chat.go`](../../backend/api/chat.go)
> - 身份认证层：[`backend/api/auth.go`](../../backend/api/auth.go)

---

## 1. 外部开放 API 契约 (`/api/v1/*`)

### 1.1 提交异步技能任务 (Submit Skill Run)
- **路径**：`POST /api/v1/skill-runs`
- **鉴权**：API Key
- **请求体格式** (`application/json`)：
```json
{
  "skill_name": "fetch-url",
  "params": {
    "url": "https://news.ycombinator.com"
  },
  "mode": "auto",
  "model": "gpt-4o",
  "user_id": 1002,
  "max_attempts": 3,
  "idempotency_key": "task_20260918_abc123"
}
```

#### 请求字段详解
- **`skill_name`** (`string`, 必填)：需要调用的技能名称，必须严格匹配 `skills.name`。
- **`params`** (`object`, 可选)：传递给技能的输入参数，必须符合该技能在 `SKILL.md` 或 `manifest.json` 中声明的 `parameters` 模式。
- **`mode`** (`string`, 可选)：执行模式，默认 `"auto"`：
  - `"auto"`：由技能的 `manifest.json` 自行决定；
  - `"script"`：强制以纯脚本模式在隔离沙箱中执行，零大模型开销，不创建会话记录；
  - `"llm"`：强制拉起轻量大模型 ReAct Agent 执行（创建会话并写入消息记录）。
- **`model`** (`string`, 可选)：当以 LLM 模式执行时的模型覆盖选项；不填则遵循技能自身配置或后端默认模型。
- **`user_id`** (`int64`, 可选)：**有头模式与无头模式区分开关**：
  - `user_id = 0`（或不传，默认）：**大模型无头模式**。会话以系统无头账号（`user_id = 0`）运行，不污染普通员工的“我的对话”列表，仅在企业管理员全量审计视图中可见。
  - `user_id > 0`：**大模型有头模式**。外部业务系统代替企业指定员工（必须通过租户所属检验）发起会话，会话挂载于该员工名下，员工登录网页端后可在左侧列表直接查看、追溯并继续输入提问。
- **`max_attempts`** (`int`, 可选)：最大允许尝试次数，默认 `1`，上限 `5`。
- **`idempotency_key`** (`string`, 可选)：客户端传入的幂等键（防网络重试导致的重复提交）。若已存在相同 Key 且未终结的任务，直接返回已存在的任务信息。

#### 响应示例 (`201 Created` / `200 OK`)
```json
{
  "code": 0,
  "data": {
    "run_id": "run_7c9b8e12-98ab-41c3-8f0a-112233445566",
    "skill_name": "fetch-url",
    "status": "pending",
    "created_at": "2026-09-18T10:00:00.000Z"
  },
  "msg": "success"
}
```

#### 常见异常状态码
- **`400 Bad Request`**：技能不存在、参数不合规范、`mode` 非法、或指定的 `user_id` 不属于当前企业。
- **`401 Unauthorized`**：缺少 API Key 或 API Key 无效/已停用。
- **`403 Forbidden`**：该 API Key 的白名单（`allowed_skills`）中未包含指定的技能。
- **`429 Too Many Requests`**：当前 Key 的并发在跑任务数已达上限（`max_concurrent`）。

---

### 1.2 轮询任务状态与结果 (Get Skill Run)
- **路径**：`GET /api/v1/skill-runs/:run_id`
- **鉴权**：API Key（只能查该 Key 所属企业/租户下的任务）
- **响应示例** (`200 OK`)：
```json
{
  "code": 0,
  "data": {
    "id": "run_7c9b8e12-98ab-41c3-8f0a-112233445566",
    "status": "success",
    "duration_ms": 2840,
    "error": null,
    "created_at": "2026-09-18T10:00:00.000Z",
    "finished_at": "2026-09-18T10:00:02.840Z",
    "params": {
      "url": "https://news.ycombinator.com"
    },
    "result": {
      "title": "Hacker News",
      "markdown_length": 15820,
      "snippet": "# Hacker News\n1. Show HN: AI Agent..."
    },
    "progress": [
      { "time": "10:00:01", "stage": "sandbox_ready" },
      { "time": "10:00:02", "stage": "script_executed" }
    ],
    "artifacts": [
      {
        "name": "hn_content.md",
        "url": "https://cos.myqcloud.com/artifacts/run_7c9b8e12/hn_content.md"
      }
    ],
    "stderr_tail": "",
    "attempt": 1,
    "max_attempts": 3,
    "failure_kind": null,
    "attempts": []
  },
  "msg": "success"
}
```

---

### 1.3 取消任务 (Cancel Skill Run)
- **路径**：`POST /api/v1/skill-runs/:run_id/cancel`
- **说明**：将排队中 (`pending`) 或执行中 (`running`) 的任务置为 `canceled` 终态。若任务已在沙箱中运行，调度器会立即发出终止信号强制杀死沙箱进程。
- **响应**：`{"code": 0, "data": {"status": "canceled"}, "msg": "success"}`

---

### 1.4 查询当前 Key 可用的技能清单 (List Available Skills)
- **路径**：`GET /api/v1/skills`
- **说明**：列出经过当前 API Key 的 `allowed_skills` 白名单过滤后、且在元数据中声明了 `api_enabled = true` 的全部外部可调用技能清单及它们的入参 Schema。

---

## 2. 内部多租户与 RBAC 会话端点契约

### 2.1 会话列表查询 (`GET /api/sessions`)
- **路径**：`GET /api/sessions`
- **Query 参数**：
  - `scope` (`string`, 可选)：
    - `""`（默认）：仅查询**我的个人会话** (`user_id = 当前登录用户`)；
    - `"all"`：查询范围根据当前用户角色动态适配：
      - **超级管理员 (UserType=1)**：查询全平台所有企业的所有会话；
      - **企业管理员 (UserType=2)**：查询本企业（`company_id`）所有员工创建的会话及 API 无头会话；
      - **普通员工 (UserType=3)**：无权限，自动降级为仅查本人会话。

### 2.2 会话消息写入与重新生成权限
- **消息发送 (`POST /api/sessions/:id/messages`)** & **重新生成 (`POST /api/sessions/:id/messages/regenerate`)**：
  - **普通员工 (UserType=3)**：仅限对本人创建的会话发起提问或重新生成；
  - **企业管理员 (UserType=2)**：除本人会话外，支持对归属本企业的无头/自动化任务会话（`user_id = 0`）进行交互与追加指令；
  - **超级管理员 (UserType=1)**：全量会话具有交互排查权限。

---

## 3. Web 管理控制台运维 API (`/api/skills/:id/runs/*`)

专供内部前端页面（如 [`frontend/src/components/SkillRunsPanel.tsx`](../../frontend/src/components/SkillRunsPanel.tsx)）进行可视化调用历史追溯、按天折线图统计与手动重试。

### 3.1 查询某个技能的执行历史列表
- **路径**：`GET /api/skills/:id/runs`
- **权限与隔离**：
  - 超管：跨企业全部记录；
  - 企管：归属当前企业（`company_id`）的全部记录；
  - 员工：仅限与当前用户关联的任务记录。
- **Query 参数**：
  - `days` (`int`, 默认 7)：时间范围（1 / 7 / 30 天）。
  - `limit` (`int`, 默认 100)：拉取数量上限。
  - `status` (`string`, 可选)：状态过滤，如传 `failed`（对应前端“只看失败”复选框）。

---

### 3.2 获取某个技能的执行指标统计 (Stats)
- **路径**：`GET /api/skills/:id/runs/stats?days=7`
- **响应数据格式**：`SkillRunStats`（按租户隔离聚合）

---

### 3.3 手动重试失败的任务
- **路径**：`POST /api/skills/:id/runs/:run_id/retry`
- **核心契约**：必须复用同一个 `run_id`，保留现场快照并自增 `attempt`。

---

## 4. 多租户登录与注册端点 (`/api/auth/*`)

### 4.1 用户登录 (`POST /api/auth/login`)
- **请求体**：
```json
{
  "account": "zhangsan",
  "password": "mypassword123",
  "company_code": "default"
}
```
- **参数说明**：
  - `company_code`（可选）：指定所属企业的代码标识。若不传，系统默认以 `default` 企业进行校验，或自动适配全局唯一用户名，保证旧客户端平滑登录。

### 4.2 用户注册 (`POST /api/auth/register`)
- **请求体**：
```json
{
  "username": "zhangsan",
  "password": "mypassword123",
  "nickname": "张三",
  "company_code": "default"
}
```
- **参数说明**：
  - `company_code`（可选）：新注册用户绑定的企业编码，默认为 `"default"`。同企业内用户名不可重复，不同企业可拥有相同用户名。
