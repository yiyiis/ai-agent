-- =========================================================================
-- messages 表上下文治理列增量迁移 (Context Assembly Columns)
-- 阶段：上下文组装改造（三档内容：blob 原文 / content 有界版 / stub 精简版）
-- 适用场景：已有数据库部署环境，执行增量变更（安全且幂等，不丢失存量数据）
-- 执行方式：
--   mysql -h <主机> -P <端口> -u <用户名> -p <数据库名> < deploy/sql/migrations/20260921_messages_context_columns.sql
-- 执行后需在 backend/dal 目录运行 `go run .` 重新生成模型与 query。
-- =========================================================================

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- 1. stub_content：tool 结果精简版（冷区组装用；错误行=原文；天然短小=NULL）
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'stub_content'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `stub_content` longtext COLLATE utf8mb4_unicode_ci COMMENT ''tool 结果精简版（冷区组装用；错误行=原文；天然短小=NULL）'' AFTER `attachments`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. stub_tool_calls：assistant 批次精简版（结构同 tool_calls，仅替换 arguments）
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'stub_tool_calls'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `stub_tool_calls` json DEFAULT NULL COMMENT ''assistant 批次精简版（结构同 tool_calls，仅替换 arguments）'' AFTER `stub_content`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. tokens_full：content+tool_calls 粗估 token（字符数/2，写时估算）
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'tokens_full'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `tokens_full` int NOT NULL DEFAULT 0 COMMENT ''content+tool_calls 粗估 token（字符数/2，写时估算）'' AFTER `stub_tool_calls`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. stub_tokens：stub 版粗估 token
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'stub_tokens'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `stub_tokens` int NOT NULL DEFAULT 0 COMMENT ''stub 版粗估 token'' AFTER `tokens_full`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 5. blob_ref：完整输出的工作区文件引用（.tool-outputs/…）
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'blob_ref'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `blob_ref` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT ''完整输出的工作区文件引用（.tool-outputs/…）'' AFTER `stub_tokens`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 6. ctx_mode：上下文口径（空=按热/冷分区；stub=恒用精简版）
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'ctx_mode'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `ctx_mode` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '''' COMMENT ''上下文口径：空=按热/冷分区；stub=恒用精简版（超限被拦截标记）'' AFTER `blob_ref`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 7. tool_target：冷区折叠键（文件类=path / search=query / bash=NULL）
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'tool_target'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `tool_target` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT ''冷区折叠键：文件类=path / search=query / bash=NULL'' AFTER `ctx_mode`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 8. tool_op：操作类型（write/append/edit/read/search/exec，冷区折叠用）
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'messages' AND column_name = 'tool_op'
);
SET @alter_sql := IF(
  @col_exists = 0,
  'ALTER TABLE `messages` ADD COLUMN `tool_op` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT ''操作类型：write/append/edit/read/search/exec（冷区折叠用）'' AFTER `tool_target`',
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
