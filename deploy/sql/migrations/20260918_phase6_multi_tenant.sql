-- =========================================================================
-- 阶段六多租户与开放 API 数据库增量迁移脚本 (Migration Script for Server MySQL)
-- 适用场景：已有数据库部署环境，执行增量变更（安全且幂等，不丢失存量数据）
-- 执行方式：
--   mysql -h <主机> -P <端口> -u <用户名> -p <数据库名> < deploy/sql/migrations/20260918_phase6_multi_tenant.sql
-- =========================================================================

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- 1. 创建企业与多租户基础表 companies (若不存在)
CREATE TABLE IF NOT EXISTS `companies` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '企业主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '企业/租户名称',
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '企业全局唯一英文字符标识/代号',
  `status` int NOT NULL DEFAULT 1 COMMENT '状态：1正常，0禁用',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_companies_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='企业与租户表';

-- 2. 插入默认企业兜底 (id=1, code='default')
INSERT INTO `companies` (`id`, `name`, `code`, `status`) 
VALUES (1, '默认企业', 'default', 1) 
ON DUPLICATE KEY UPDATE `name`=VALUES(`name`);

-- 3. 给 user_info 表新增 company_id 字段 (若不存在)
SET @col_exists := (
  SELECT count(*) FROM information_schema.columns 
  WHERE table_schema = DATABASE() AND table_name = 'user_info' AND column_name = 'company_id'
);
SET @alter_sql := IF(
  @col_exists = 0, 
  'ALTER TABLE `user_info` ADD COLUMN `company_id` bigint NOT NULL DEFAULT 1 COMMENT ''所属企业 ID'' AFTER `user_id`', 
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. 存量用户兜底绑定至默认企业 (company_id = 1)
UPDATE `user_info` SET `company_id` = 1 WHERE `company_id` = 0 OR `company_id` IS NULL;

-- 5. 检查并清理旧的单一 username 唯一索引 (如果存在，避免阻碍跨企同名)
SET @old_idx := (
  SELECT index_name FROM information_schema.statistics 
  WHERE table_schema = DATABASE() AND table_name = 'user_info' AND index_name = 'idx_username' AND non_unique = 0 
  LIMIT 1
);
SET @drop_idx_sql := IF(
  @old_idx IS NOT NULL, 
  'ALTER TABLE `user_info` DROP INDEX `idx_username`', 
  'SELECT 1'
);
PREPARE stmt FROM @drop_idx_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 6. 添加租户联合唯一索引 idx_company_username (company_id, username)
SET @idx_comp_user := (
  SELECT count(*) FROM information_schema.statistics 
  WHERE table_schema = DATABASE() AND table_name = 'user_info' AND index_name = 'idx_company_username'
);
SET @alter_sql := IF(
  @idx_comp_user = 0, 
  'ALTER TABLE `user_info` ADD UNIQUE KEY `idx_company_username` (`company_id`, `username`)', 
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 7. 添加 user_info.company_id 普通索引
SET @idx_comp := (
  SELECT count(*) FROM information_schema.statistics 
  WHERE table_schema = DATABASE() AND table_name = 'user_info' AND index_name = 'idx_user_info_company_id'
);
SET @alter_sql := IF(
  @idx_comp = 0, 
  'ALTER TABLE `user_info` ADD KEY `idx_user_info_company_id` (`company_id`)', 
  'SELECT 1'
);
PREPARE stmt FROM @alter_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 8. 创建开放 API Key 凭证表 api_keys (若不存在)
CREATE TABLE IF NOT EXISTS `api_keys` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `api_key` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '',
  `company_id` bigint NOT NULL DEFAULT '0',
  `is_global` tinyint(1) NOT NULL DEFAULT '0',
  `allowed_skills` json DEFAULT NULL,
  `max_concurrent` bigint NOT NULL DEFAULT '3',
  `is_active` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_api_keys_key` (`api_key`),
  KEY `idx_api_keys_company_id` (`company_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 9. 创建任务执行记录与审计表 skill_runs (若不存在)
CREATE TABLE IF NOT EXISTS `skill_runs` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `run_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `skill_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `skill_id` bigint DEFAULT NULL,
  `company_id` bigint NOT NULL DEFAULT '0',
  `user_id` bigint NOT NULL DEFAULT '0',
  `api_key_id` bigint DEFAULT NULL,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` varchar(20) COLLATE utf8mb4_unicode_ci NOT NULL,
  `params` json DEFAULT NULL,
  `result` json DEFAULT NULL,
  `artifacts` json DEFAULT NULL,
  `progress` json DEFAULT NULL,
  `error` text COLLATE utf8mb4_unicode_ci,
  `stdout_tail` text COLLATE utf8mb4_unicode_ci,
  `stderr_tail` text COLLATE utf8mb4_unicode_ci,
  `duration_ms` bigint DEFAULT NULL,
  `attempt` bigint NOT NULL DEFAULT '1',
  `max_attempts` bigint NOT NULL DEFAULT '1',
  `failure_kind` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `next_retry_at` datetime(3) DEFAULT NULL,
  `exec_context` json DEFAULT NULL,
  `attempts` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `started_at` datetime(3) DEFAULT NULL,
  `finished_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_skill_runs_run_id` (`run_id`),
  KEY `idx_skill_runs_status` (`status`),
  KEY `idx_skill_runs_created_at` (`created_at`),
  KEY `idx_skill_runs_api_key_id` (`api_key_id`),
  KEY `idx_skill_runs_session_id` (`session_id`),
  KEY `idx_skill_runs_skill_name` (`skill_name`),
  KEY `idx_skill_runs_skill_id` (`skill_id`),
  KEY `idx_skill_runs_company_id` (`company_id`),
  KEY `idx_skill_runs_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET FOREIGN_KEY_CHECKS = 1;
