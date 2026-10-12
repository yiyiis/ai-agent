# MCP 广场数据库模型与字段规范 (Database Specification)

> **版本**：v1.0.0
> **数据库引擎**：MySQL 8.0+ / InnoDB / `utf8mb4`
> **ORM 映射**：GORM Gen ([`backend/dal/model/`](../../backend/dal/model/))
> **建表源头**：[`deploy/sql/schema.sql`](../../deploy/sql/schema.sql)（手工维护，先建表再生成）
> **增量迁移**：[`deploy/sql/migrations/20261008_mcp_plaza.sql`](../../deploy/sql/migrations/20261008_mcp_plaza.sql)（幂等信息探测风格）

---

## 1. mcp_servers（广场注册表）

```sql
DROP TABLE IF EXISTS `mcp_servers`;
CREATE TABLE `mcp_servers` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '短名，工具消歧前缀，约束 [a-z0-9-]',
  `description` text COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '广场卡片展示的用途说明',
  `transport` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'http' COMMENT 'http | stdio（stdio 仅 config 种子）',
  `url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'http 型服务器端点',
  `headers` json DEFAULT NULL COMMENT '静态鉴权头 KV（明文 v1，出参打码）',
  `command` json DEFAULT NULL COMMENT 'stdio 型子进程命令（数组）',
  `env` json DEFAULT NULL COMMENT 'stdio 型子进程环境变量（KV）',
  `call_timeout_sec` int NOT NULL DEFAULT 60 COMMENT 'tools/call 超时（秒）',
  `tool_allow` json DEFAULT NULL COMMENT '工具白名单（空=全部）',
  `enabled` tinyint(1) NOT NULL DEFAULT 1 COMMENT '停用后所有会话跳过',
  `system` tinyint(1) NOT NULL DEFAULT 0 COMMENT 'config 种子标记：广场置顶、网页不可删改',
  `owner_id` bigint NOT NULL DEFAULT 0 COMMENT '创建者（0=系统种子）',
  `owner_name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '',
  `tool_count` int NOT NULL DEFAULT 0 COMMENT '最近一次发现到的工具数（缓存，卡片展示）',
  `last_error` text COLLATE utf8mb4_unicode_ci COMMENT '最近一次连接/发现错误（卡片状态点）',
  `last_synced_at` datetime(3) DEFAULT NULL COMMENT '最近一次成功 tools/list 时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_mcp_servers_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

字段语义：

- **name**：全局唯一短名，即引擎层消歧前缀（`mcp_<name>_<tool>`）。约束 `[a-z0-9-]`，
  保证生成的 function 名落在 OpenAI 名称字符集 `[a-zA-Z0-9_-]` 内且可无歧义反查。
- **headers**：JSON 对象文本（gorm gen 生成 `*string`，业务按 JSON 文本读写，与
  skills.tags 同一映射策略）。值明文存储（v1）；API 出参一律打码 `***`。
- **system=1**：由 `config.yaml MCP.Servers` 种子投影产生（按 name INSERT IGNORE，
  种子字段变更时更新），网页端只读——管理员改配置即改种子。
- **tool_count / last_error / last_synced_at**：引擎"测试连接"与后台发现的回写缓存，
  广场卡片的状态点与工具数直接消费，避免列表页逐个拨号。

## 2. session_mcp_servers（会话启用关联）

```sql
DROP TABLE IF EXISTS `session_mcp_servers`;
CREATE TABLE `session_mcp_servers` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `mcp_server_id` bigint NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_session_mcp` (`session_id`,`mcp_server_id`),
  KEY `idx_session_mcp_servers_session_id` (`session_id`),
  KEY `idx_session_mcp_servers_mcp_server_id` (`mcp_server_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

- PUT 整体替换语义（对齐 `ReplaceSessionSkills`：事务内删旧插新）。
- server 删除时连带清理关联（`DeleteMcpServerCascade`）。
- 无 source 三态：MCP 无挂载成本，启用即注入全部（白名单后）工具。

## 3. ER 关系

```text
mcp_servers 1 ──── N session_mcp_servers N ──── 1 sessions(session_id)
     │
     └──（name 唯一；engine 层 qualified tool 映射的主键维度）
```
