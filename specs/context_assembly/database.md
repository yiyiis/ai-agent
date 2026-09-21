# 数据库规范（messages 治理列与迁移）

> 所属模块：[上下文组装治理](./README.md)  
> DDL 唯一源头：`deploy/sql/schema.sql`（DB-first）· 增量迁移：`deploy/sql/migrations/20260921_messages_context_columns.sql`

---

## 1. 治理列（messages 表新增 8 列）

全部可空/带默认值，对存量数据零破坏：

| 列 | 类型 | 语义 | 写入时机 |
|---|---|---|---|
| `stub_content` | longtext NULL | tool 结果精简版（冷区组装用）。**错误行=原文、天然短小=NULL**——NULL 时组装直接用 `content` | tool 行落库（`toolExtras`） |
| `stub_tool_calls` | json NULL | assistant 批次精简版（结构同 `tool_calls`，仅替换 write/edit 的 arguments；id/type/name 原样） | assistant 行落库（`assistantExtras`） |
| `tokens_full` | int NOT NULL DEFAULT 0 | content+tool_calls 粗估 token（`len/2` 字节估，偏保守）。**兼作旧数据判定**：`=0 且 content 非空` 视为治理列缺失的存量行，组装时现场生成 stub | 所有 assistant/tool 行 |
| `stub_tokens` | int NOT NULL DEFAULT 0 | stub 版粗估 token | 同上 |
| `blob_ref` | varchar(512) NULL | 完整输出的持久化定位符，JSON：`{"path":".tool-outputs/…","url":"归档URL"}`。path=沙箱工作区路径（模型侧身份）；url=对象存储归档（`storage.PutArchive`，Bucket 空落本地 uploads/），沙箱回收后凭此重物化。v1.0.0 裸路径行兼容读取但无从恢复。归属：tool 行=进程超限输出；assistant 行=被拒参数原文 | 超限/被拒时（双写：沙箱+归档） |
| `ctx_mode` | varchar(16) NOT NULL DEFAULT '' | 上下文口径：空=按热/冷分区；`stub`=恒用精简版。**仅 assistant 行**会设置（批次含被拒调用时） | 拦截路径 |
| `tool_target` | varchar(255) NULL | 冷区折叠键：文件类=path / search=query / bash=NULL。同批次目标不一致 → NULL（保守不折叠） | 写时提取（`callTarget`） |
| `tool_op` | varchar(16) NULL | 操作类型：write/append/edit/read/search/exec（冷区折叠用，**列先行、折叠逻辑后续阶段实现**） | 同上 |

### 1.1 不变量

1. `content` / `tool_calls` 列**永远有界**（工具层进场保证）：原文只存 blob 文件，永不进 SELECT * 大行——`ListMessagesBySessionID` 每轮全量拉取会话，大行会拖慢每一轮；
2. `stub_*` 是**额外一份**，不覆盖原文；被拒批次的 `tool_calls` 与 `stub_tool_calls` 同为 stub 版（有意冗余：前者被前端历史接口读取）；
3. user 行治理列全默认（叙事永不裁，无 stub 概念）。

## 2. 迁移脚本

`deploy/sql/migrations/20260921_messages_context_columns.sql`：8 个独立的 `information_schema.columns` 探测 + `PREPARE/EXECUTE` 守卫块，完全沿用 `20260918_phase6_multi_tenant.sql` 的幂等模式（可重复执行）。

执行方式：

```bash
mysql -h <主机> -P <端口> -u <用户> -p <数据库名> < deploy/sql/migrations/20260921_messages_context_columns.sql
```

**部署顺序强约束**：必须先迁移后发版——新代码走 gorm 全列 INSERT，旧表会报 unknown column。已在本地 MySQL 执行两遍验证幂等、8 列确认就位。

## 3. gen 重新生成（DB-first 流程）

1. 修改 `deploy/sql/schema.sql` 的 messages DDL（已同步）；
2. 对目标库执行迁移；
3. `cd backend/dal && go run .` 重新生成 `model/messages.gen.go` + `query/messages.gen.go`。

> **注**：本次的 gen 文件为手工对齐（int→int32、nullable→指针），与生成器输出一致；上线后建议对已迁移库跑一次生成器消除漂移。

## 4. dao 层零改动

`ListMessagesBySessionID` 为 SELECT *、`CreateMessage` 为全列 INSERT——新列经 gen 再生成后自动生效，接口签名不变。
