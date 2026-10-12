-- =========================================================================
-- MCP 广场建表迁移 (MCP Plaza)
-- 阶段：阶段八 MCP 协议集成（详见 specs/mcp/）
-- 适用场景：已有数据库部署环境，执行增量变更（安全且幂等，不丢失存量数据）
-- 执行方式：
--   mysql -h <主机> -P <端口> -u <用户名> -p <数据库名> < deploy/sql/migrations/20261008_mcp_plaza.sql
-- 执行后需在 backend/dal 目录运行 `go run .` 重新生成模型与 query。
-- =========================================================================

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- 1. mcp_servers：MCP 广场注册表
SET @tbl_exists := (
  SELECT count(*) FROM information_schema.tables
  WHERE table_schema = DATABASE() AND table_name = 'mcp_servers'
);
SET @create_sql := IF(
  @tbl_exists = 0,
  'CREATE TABLE `mcp_servers` (
    `id` bigint NOT NULL AUTO_INCREMENT,
    `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT ''短名，工具消歧前缀，约束 [a-z0-9-]'',
    `description` text COLLATE utf8mb4_unicode_ci NOT NULL COMMENT ''广场卡片展示的用途说明'',
    `transport` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT ''http'' COMMENT ''http | stdio（stdio 仅 config 种子）'',
    `url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT ''http 型服务器端点'',
    `headers` json DEFAULT NULL COMMENT ''静态鉴权头 KV（明文 v1，出参打码）'',
    `command` json DEFAULT NULL COMMENT ''stdio 型子进程命令（JSON 数组）'',
    `env` json DEFAULT NULL COMMENT ''stdio 型子进程环境变量（KV）'',
    `call_timeout_sec` int NOT NULL DEFAULT 60 COMMENT ''tools/call 超时（秒）'',
    `tool_allow` json DEFAULT NULL COMMENT ''工具白名单（空=全部）'',
    `enabled` tinyint(1) NOT NULL DEFAULT 1 COMMENT ''停用后所有会话跳过'',
    `system` tinyint(1) NOT NULL DEFAULT 0 COMMENT ''config 种子标记：广场置顶、网页不可删改'',
    `owner_id` bigint NOT NULL DEFAULT 0 COMMENT ''创建者（0=系统种子）'',
    `owner_name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '''',
    `tool_count` int NOT NULL DEFAULT 0 COMMENT ''最近一次发现到的工具数（缓存，卡片展示）'',
    `last_error` text COLLATE utf8mb4_unicode_ci COMMENT ''最近一次连接/发现错误（卡片状态点）'',
    `last_synced_at` datetime(3) DEFAULT NULL COMMENT ''最近一次成功 tools/list 时间'',
    `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_mcp_servers_name` (`name`)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT=''MCP 广场注册表''',
  'SELECT 1'
);
PREPARE stmt FROM @create_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. session_mcp_servers：会话启用的 MCP Server 关联
SET @tbl_exists := (
  SELECT count(*) FROM information_schema.tables
  WHERE table_schema = DATABASE() AND table_name = 'session_mcp_servers'
);
SET @create_sql := IF(
  @tbl_exists = 0,
  'CREATE TABLE `session_mcp_servers` (
    `id` bigint NOT NULL AUTO_INCREMENT,
    `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
    `mcp_server_id` bigint NOT NULL,
    `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_session_mcp` (`session_id`,`mcp_server_id`),
    KEY `idx_session_mcp_servers_session_id` (`session_id`),
    KEY `idx_session_mcp_servers_mcp_server_id` (`mcp_server_id`)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT=''会话启用的 MCP Server 关联''',
  'SELECT 1'
);
PREPARE stmt FROM @create_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
