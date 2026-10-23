-- ai-agent 全量表结构定义（唯一源头，手工维护）
-- 修改表结构：先改本文件 → 执行建表 → 运行 dal/gen.go 重新生成模型与 query 层。
-- 生成时间: 2026-09-12 00:15:22
-- 说明：应用启动只连接数据库；已有数据库须执行 migrations 下的增量迁移。
--       默认管理员账号（admin）由应用首次启动时自动播种，不在本脚本内。
-- 警告：脚本包含 DROP TABLE 与 USE 语句，执行会清空目标库中的表数据，执行前请确认连接的库名。

CREATE DATABASE IF NOT EXISTS `ai_agent` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE `ai_agent`;
SET FOREIGN_KEY_CHECKS = 0;

DROP TABLE IF EXISTS `companies`;
CREATE TABLE `companies` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '企业主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '企业/租户名称',
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '企业全局唯一英文字符标识/代号',
  `status` int NOT NULL DEFAULT 1 COMMENT '状态：1正常，0禁用',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_companies_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='企业与租户表';

INSERT INTO `companies` (`id`, `name`, `code`, `status`) VALUES (1, '默认企业', 'default', 1) ON DUPLICATE KEY UPDATE `id`=`id`;

-- user_info 为 ai-agent 用户表，归属于特定 company_id
DROP TABLE IF EXISTS `user_info`;
CREATE TABLE `user_info` (
  `user_id` int NOT NULL AUTO_INCREMENT COMMENT '用户id',
  `company_id` bigint NOT NULL DEFAULT 1 COMMENT '所属企业 ID',
  `username` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '用户名',
  `nickname` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '昵称',
  `password` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '密码',
  `phone` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '手机号码',
  `user_type` int NOT NULL DEFAULT 3 COMMENT '用户类型：1：超级管理员，2：管理员，3：普通用户',
  `head_image` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '用户头像',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '删除时间',
  PRIMARY KEY (`user_id`),
  UNIQUE KEY `idx_company_username` (`company_id`, `username`),
  KEY `idx_user_info_company_id` (`company_id`)
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
  `agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '消息所属子 Agent；空字符串为主 Agent',
  `role` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `content` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `reasoning` longtext COLLATE utf8mb4_unicode_ci COMMENT '模型思考过程',
  `tool_calls` json DEFAULT NULL,
  `tool_call_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `attachments` json DEFAULT NULL,
  `stub_content` longtext COLLATE utf8mb4_unicode_ci COMMENT 'tool 结果精简版（冷区组装用；错误行=原文；天然短小=NULL）',
  `stub_tool_calls` json DEFAULT NULL COMMENT 'assistant 批次精简版（结构同 tool_calls，仅替换 arguments）',
  `tokens_full` int NOT NULL DEFAULT 0 COMMENT 'content+tool_calls 粗估 token（字符数/2，写时估算）',
  `stub_tokens` int NOT NULL DEFAULT 0 COMMENT 'stub 版粗估 token',
  `blob_ref` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '完整输出的工作区文件引用（.tool-outputs/…）',
  `ctx_mode` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '上下文口径：空=按热/冷分区；stub=恒用精简版（超限被拦截标记）',
  `tool_target` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '冷区折叠键：文件类=path / search=query / bash=NULL',
  `tool_op` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '操作类型：write/append/edit/read/search/exec（冷区折叠用）',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_messages_message_id` (`message_id`),
  KEY `idx_messages_session_id` (`session_id`),
  KEY `idx_messages_session_agent` (`session_id`,`agent_id`,`id`),
  CONSTRAINT `fk_sessions_messages` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`session_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

DROP TABLE IF EXISTS `agent_runs`;
CREATE TABLE `agent_runs` (
  `agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `parent_agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '父 Agent；主 Agent 使用 session_id',
  `workspace_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '共享工作区；当前固定为 session_id',
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
  `source` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'full' COMMENT '来源：full 常驻全量 | auto 对话按需',
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
  `agent_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '计费所属子 Agent；空字符串为主 Agent',
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
  KEY `idx_token_usages_session_agent` (`session_id`,`agent_id`),
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

-- 阶段八：MCP 广场注册表（详见 specs/mcp/database.md）
DROP TABLE IF EXISTS `mcp_servers`;
CREATE TABLE `mcp_servers` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '短名，工具消歧前缀，约束 [a-z0-9-]',
  `description` text COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '广场卡片展示的用途说明',
  `transport` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'http' COMMENT 'http | stdio（stdio 仅 config 种子）',
  `url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'http 型服务器端点',
  `headers` json DEFAULT NULL COMMENT '静态鉴权头 KV（明文 v1，出参打码）',
  `command` json DEFAULT NULL COMMENT 'stdio 型子进程命令（JSON 数组）',
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='MCP 广场注册表';

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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='会话启用的 MCP Server 关联';

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
