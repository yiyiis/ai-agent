-- ai-agent 全量表结构定义（唯一源头，手工维护）
-- 修改表结构：先改本文件 → 执行建表 → 运行 dal/gen.go 重新生成模型与 query 层。
-- 生成时间: 2026-09-12 00:15:22
-- 说明：应用启动时 GORM AutoMigrate 会按生成的模型自动补齐表结构；
--       默认管理员账号（admin）由应用首次启动时自动播种，不在本脚本内。
-- 警告：脚本包含 DROP TABLE 与 USE 语句，执行会清空目标库中的表数据，执行前请确认连接的库名。

CREATE DATABASE IF NOT EXISTS `ai_agent` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE `ai_agent`;
SET FOREIGN_KEY_CHECKS = 0;

-- user_info 为 ai-agent 扩展表（mirror 原型无用户表，身份由中台 SSO JWT 签发；
-- 本项目独立部署，web 控制台自带登录/注册，故保留此表）
DROP TABLE IF EXISTS `user_info`;
CREATE TABLE `user_info` (
  `user_id` int NOT NULL AUTO_INCREMENT COMMENT '用户id',
  `username` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '用户名',
  `nickname` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '昵称',
  `password` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '密码',
  `phone` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '手机号码',
  `user_type` int NOT NULL DEFAULT 3 COMMENT '用户类型：1：超级管理员，2：管理员，3：普通用户',
  `head_image` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '用户头像',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '删除时间',
  PRIMARY KEY (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `sessions`;
CREATE TABLE `sessions` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `company_id` bigint NOT NULL DEFAULT '0',
  `user_id` bigint NOT NULL DEFAULT '0',
  `title` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '新会话',
  `model` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `system_prompt` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `auto_skill` tinyint(1) NOT NULL DEFAULT '1',
  `interactive` tinyint(1) NOT NULL DEFAULT '0',
  `api_key_id` bigint DEFAULT NULL,
  `last_context_tokens` bigint NOT NULL DEFAULT '0',
  `memory_extracted_upto_message_id` bigint NOT NULL DEFAULT '0',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_sessions_session_id` (`session_id`),
  KEY `idx_sessions_company_id` (`company_id`),
  KEY `idx_sessions_user_id` (`user_id`),
  KEY `idx_sessions_api_key_id` (`api_key_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `messages`;
CREATE TABLE `messages` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `message_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `role` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `content` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `reasoning` longtext COLLATE utf8mb4_unicode_ci COMMENT '模型思考过程',
  `tool_calls` json DEFAULT NULL,
  `tool_call_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `attachments` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_messages_message_id` (`message_id`),
  KEY `idx_messages_session_id` (`session_id`),
  CONSTRAINT `fk_sessions_messages` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`session_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `skills`;
CREATE TABLE `skills` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `description` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `dir_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `owner_id` bigint NOT NULL DEFAULT '0',
  `owner_name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '',
  `confidential` tinyint(1) NOT NULL DEFAULT '0',
  `model` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `tags` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_skills_dir_name` (`dir_name`),
  UNIQUE KEY `idx_skills_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `skill_versions`;
CREATE TABLE `skill_versions` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `skill_id` bigint NOT NULL,
  `version_number` bigint NOT NULL,
  `notes` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_by` bigint NOT NULL DEFAULT '0',
  `created_by_name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '',
  `source` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'publish',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_skill_versions_skill_id` (`skill_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `session_skills`;
CREATE TABLE `session_skills` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `skill_id` bigint NOT NULL,
  `pinned_version_number` bigint DEFAULT NULL,
  `source` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'manual',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_session_skill` (`session_id`,`skill_id`),
  KEY `idx_session_skills_session_id` (`session_id`),
  KEY `idx_session_skills_skill_id` (`skill_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `api_keys`;
CREATE TABLE `api_keys` (
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

DROP TABLE IF EXISTS `skill_runs`;
CREATE TABLE `skill_runs` (
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

DROP TABLE IF EXISTS `token_usages`;
CREATE TABLE `token_usages` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `company_id` bigint NOT NULL DEFAULT '0',
  `user_id` bigint NOT NULL DEFAULT '0',
  `model` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `provider_key` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '',
  `round_index` bigint NOT NULL DEFAULT '0',
  `purpose` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'turn',
  `prompt_tokens` bigint NOT NULL DEFAULT '0',
  `completion_tokens` bigint NOT NULL DEFAULT '0',
  `cached_tokens` bigint NOT NULL DEFAULT '0',
  `reasoning_tokens` bigint NOT NULL DEFAULT '0',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_token_usages_model` (`model`),
  KEY `idx_token_usages_purpose` (`purpose`),
  KEY `idx_token_usages_created_at` (`created_at`),
  KEY `idx_token_usages_session_id` (`session_id`),
  KEY `idx_token_usages_company_id` (`company_id`),
  KEY `idx_token_usages_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `memories`;
CREATE TABLE `memories` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `memory_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `company_id` bigint NOT NULL DEFAULT '0',
  `user_id` bigint NOT NULL DEFAULT '0',
  `scope` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'preference',
  `topic` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '',
  `key_hint` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `content` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `source` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'explicit',
  `source_session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `source_message_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `pinned` tinyint(1) NOT NULL DEFAULT '0',
  `hit_count` bigint NOT NULL DEFAULT '0',
  `last_used_at` datetime(3) DEFAULT NULL,
  `deleted_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_memories_memory_id` (`memory_id`),
  UNIQUE KEY `uk_owner_key` (`company_id`,`user_id`,`scope`,`key_hint`),
  KEY `idx_memories_deleted_at` (`deleted_at`),
  KEY `idx_memories_company_id` (`company_id`),
  KEY `idx_memories_user_id` (`user_id`),
  KEY `idx_memories_topic` (`topic`),
  KEY `idx_memories_source` (`source`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `session_digests`;
CREATE TABLE `session_digests` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `upto_message_id` bigint NOT NULL,
  `content` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `covered_tokens` bigint NOT NULL DEFAULT '0',
  `covered_messages` bigint NOT NULL DEFAULT '0',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_session_digests_session_id` (`session_id`),
  KEY `idx_session_digests_upto_message_id` (`upto_message_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET FOREIGN_KEY_CHECKS = 1;
