# Token Usage 统计查询接口契约规范 (API Specification)

> **版本**：v1.0.0  
> **协议**：HTTP/1.1、HTTP/2  
> **认证体系**：`Authorization: Bearer <JWT_TOKEN>` 或 `x-token`（同 Web 会话，全局登录中间件）  
> **相关代码目标位置**：
> - 接口层：[`backend/api/token_usage.go`](../../backend/api/token_usage.go)
> - 路由注册：[`backend/router.go`](../../backend/router.go)
> - 数据聚合：[`backend/dao/token_usage.go`](../../backend/dao/token_usage.go)
> - 计价换算：[`backend/pkg/provider/pricing.go`](../../backend/pkg/provider/pricing.go)

---

## 1. 用量统计端点（聚合查询）

### 1.1 查询 Token 用量统计 (Get Token Usage Stats)

- **路径**：`GET /api/usage/stats`
- **鉴权**：JWT 登录态
- **请求参数**（Query String）：

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :--- | :--- |
| `days` | `int` | 否 | 统计时间窗（天），默认 `7`，仅允许 `1 ~ 90`（超限自动钳制） |
| `model` | `string` | 否 | 按模型过滤，空 = 全部模型 |
| `purpose` | `string` | 否 | 按用途过滤（`turn` / `compact` / `extract`），空 = 全部 |
| `scope` | `string` | 否 | `all` = 管理员视角看企业/全量；不传 = 仅看本人 |

#### 三级 RBAC 可见性矩阵

| 调用者身份 | 不传 `scope`（默认） | `scope=all` |
| :--- | :--- | :--- |
| 普通员工 (`user_type=3`) | 仅本人 (`user_id` = 自己) | 仍仅本人（`scope` 被忽略，无提权） |
| 企业管理员 (`user_type=2`) | 仅本人 | **本企业全量**（`company_id` = 本企业） |
| 超级管理员 (`user_type=1`) | 仅本人 | **全平台**（不加过滤条件） |

#### 响应示例 (`200 OK`)

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "days": 7,
    "total": {
      "calls": 12,
      "prompt_tokens": 53210,
      "completion_tokens": 8842,
      "cached_tokens": 12000,
      "reasoning_tokens": 3100,
      "total_tokens": 62052,
      "cost_estimate": 0.246168
    },
    "daily": [
      {
        "date": "2026-09-19",
        "calls": 2,
        "prompt_tokens": 8420,
        "completion_tokens": 1200,
        "cached_tokens": 2000,
        "reasoning_tokens": 300,
        "total_tokens": 9620,
        "cost_estimate": 0.031248
      }
    ],
    "by_model": [
      {
        "model": "MiniMax-Text-01",
        "provider_key": "minimax",
        "calls": 10,
        "prompt_tokens": 50000,
        "completion_tokens": 8000,
        "cached_tokens": 12000,
        "reasoning_tokens": 3000,
        "total_tokens": 58000,
        "cost_estimate": 0.220800
      }
    ],
    "by_purpose": [
      { "purpose": "turn", "calls": 12, "prompt_tokens": 53210, "completion_tokens": 8842, "total_tokens": 62052 }
    ]
  }
}
```

#### 响应字段详解

- **`total_tokens`**：`prompt_tokens + completion_tokens`（cached / reasoning 为子集，不重复相加）。
- **`cost_estimate`** (`float64`, 单位: 元)：依据 `LLM.Billing` 单价表在**查询时**换算的估算成本，非账单；未配置单价的模型恒为 `0`，token 计数照常展示。换算公式见 [billing.md](./billing.md) 第 4 节。
- **`daily`**：按自然日聚合（本地时区），窗口内无数据的天补零占位，方便前端直接绘曲线。
- **`by_model`**：按模型维度聚合，携带 `provider_key` 便于分账视角。
- **`by_purpose`**：按用途维度聚合（对话推理 / 压缩 / 记忆抽取的成本结构拆分）。
- **`daily` / `by_model` / `by_purpose` 各项的 `cost_estimate`** 与 total 同口径；`by_purpose` 不含金额字段（purpose 与单价正交，金额看 model 维度即可）。

#### 常见异常

- 登录态失效：`{"code": 1, "msg": "登录态已失效"}`
- 系统异常：`{"code": 1, "msg": "系统异常"}`（DAO 层三段式错误包装后由 Controller 信封统一返回）

---

## 2. 会话级用量明细端点

### 2.1 查询指定会话的用量明细 (Get Session Token Usage)

- **路径**：`GET /api/sessions/:id/usage`
- **鉴权**：JWT 登录态 + 会话级读权限（复用 `loadOwnedSession`：本人 / 本企企管 / 超管；不存在与他人会话统一报"会话不存在"，防存在性探测）
- **请求参数**（Query String）：

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :--- | :--- |
| `limit` | `int` | 否 | 返回条数上限，默认 `200`，钳制范围 `1 ~ 500` |

#### 响应示例 (`200 OK`)

```json
{
  "code": 0,
  "msg": "success",
  "data": [
    {
      "id": 1024,
      "session_id": "01h7...",
      "model": "MiniMax-Text-01",
      "provider_key": "minimax",
      "round_index": 1,
      "purpose": "turn",
      "prompt_tokens": 300,
      "completion_tokens": 45,
      "cached_tokens": 80,
      "reasoning_tokens": 20,
      "total_tokens": 345,
      "cost_estimate": 0.000978,
      "created_at": "2026-09-19T10:00:02.480Z"
    }
  ]
}
```

#### 语义说明

- 列表按 `id` 倒序（最新在前），一行对应一次 LLM 调用；`round_index` 相同的行属于同一 Turn 的不同推理轮次。
- 会话被删除后本端点返回"会话不存在"，但 `token_usages` 中的历史行保留（审计生命周期独立，见 [database.md](./database.md) 第 4 节），仍可经统计端点聚合呈现。

---

## 3. 设计边界

- 本模块**只读呈现**，不提供任何用量写入/修改/删除接口（写入唯一入口为 agent 记账旁路，见 [billing.md](./billing.md)）。
- 本期不做 quota 限额与熔断（统计不拦截请求）；金额字段恒为估算值 `cost_estimate`，不作为结算依据。
