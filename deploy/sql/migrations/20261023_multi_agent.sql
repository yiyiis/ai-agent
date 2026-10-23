-- Multi-agent incremental migration. Safe to re-run against an existing database.
-- Run with an explicitly selected database, then regenerate backend/dal with go run .
-- Existing messages and usage remain attributed to the main agent (agent_id = '').
SET NAMES utf8mb4;

CREATE TABLE IF NOT EXISTS `agent_runs` (
  `agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `parent_agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Parent agent; root uses session_id',
  `workspace_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Shared workspace; currently session_id',
  `tool_call_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '',
  `task` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `mode` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'read_only',
  `model` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'queued',
  `result` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `error` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `started_at` datetime(3) DEFAULT NULL,
  `finished_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`agent_id`),
  UNIQUE KEY `uk_agent_runs_session_tool` (`session_id`,`tool_call_id`),
  KEY `idx_agent_runs_session_created` (`session_id`,`created_at`),
  KEY `idx_agent_runs_parent` (`parent_agent_id`),
  KEY `idx_agent_runs_status` (`status`),
  CONSTRAINT `fk_sessions_agent_runs` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`session_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET @column_exists := (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'agent_id');
SET @migration_sql := IF(@column_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '''' COMMENT ''Child agent; empty for main agent'' AFTER `session_id`',
  'SELECT 1');
PREPARE migration_stmt FROM @migration_sql;
EXECUTE migration_stmt;
DEALLOCATE PREPARE migration_stmt;

SET @index_exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND index_name = 'idx_messages_session_agent');
SET @migration_sql := IF(@index_exists = 0,
  'ALTER TABLE `messages` ADD INDEX `idx_messages_session_agent` (`session_id`,`agent_id`,`id`)',
  'SELECT 1');
PREPARE migration_stmt FROM @migration_sql;
EXECUTE migration_stmt;
DEALLOCATE PREPARE migration_stmt;

SET @column_exists := (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'token_usages' AND column_name = 'agent_id');
SET @migration_sql := IF(@column_exists = 0,
  'ALTER TABLE `token_usages` ADD COLUMN `agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '''' COMMENT ''Child agent; empty for main agent'' AFTER `session_id`',
  'SELECT 1');
PREPARE migration_stmt FROM @migration_sql;
EXECUTE migration_stmt;
DEALLOCATE PREPARE migration_stmt;

SET @index_exists := (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema = DATABASE() AND table_name = 'token_usages' AND index_name = 'idx_token_usages_session_agent');
SET @migration_sql := IF(@index_exists = 0,
  'ALTER TABLE `token_usages` ADD INDEX `idx_token_usages_session_agent` (`session_id`,`agent_id`)',
  'SELECT 1');
PREPARE migration_stmt FROM @migration_sql;
EXECUTE migration_stmt;
DEALLOCATE PREPARE migration_stmt;
