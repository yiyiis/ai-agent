export interface Session {
  id: string
  title: string
  model: string
  system_prompt: string | null
  /** 自动按用户意图选 skill；关掉后只用手工勾选的 skill */
  auto_skill?: boolean
  created_at: string
  updated_at: string
}

export interface ToolCall {
  id: string
  type: 'function'
  function: {
    name: string
    arguments: string
  }
}

export interface Attachment {
  url: string
  filename: string
  size?: number | null
  content_type?: string | null
}

export interface Message {
  id: string
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  tool_calls?: ToolCall[] | null
  tool_call_id?: string | null
  name?: string | null
  attachments?: Attachment[] | null
  created_at: string
}

export interface EnabledSkillEntry {
  skill_id: number
  /** NULL = 跟随当前激活版本 */
  pinned_version_number: number | null
  /** 'manual' = 用户勾选；'auto' = 对话中模型按意图自动加载 */
  source?: string
}

export interface SessionDetail extends Session {
  messages: Message[]
  enabled_skill_ids: number[]
  enabled_skills?: EnabledSkillEntry[]
}

export interface Skill {
  id: number
  name: string
  description: string | null
  dir_name: string
  created_at: string
  // 所有者 & 机密
  owner_id?: number
  owner_name?: string
  confidential?: boolean
  is_owner?: boolean
  can_view?: boolean // 机密时仅所有者为 true；false 表示只能使用、不可预览/下载
  /** API 以 LLM 模式跑这个 skill 时用的模型；null = 跟随 manifest / 后端默认 */
  model?: string | null
  /** 分类标签，平台侧维护 */
  tags?: string[]
  /** 有 manifest.json 且 api_enabled=true —— 可被外部接口调用 */
  api_enabled?: boolean
  llm_enabled?: boolean
  /** manifest.json 解析失败的原因；非空表示该 skill 已从对外接口消失 */
  manifest_error?: string | null
  /** 工作副本相对当前激活版本有改动（编辑过但没发布） */
  has_unpublished_changes?: boolean
  // 列表接口才填充；从 upload/create 单体返回时也会有
  current_version_number?: number | null
  version_count?: number
  updated_at?: string | null
  enabled_session_count?: number
}

export interface LoginUser {
  user_id: number
  company_id: number
  name: string
  phone?: string
  username?: string
}

export interface LoginResult {
  token: string
  user: LoginUser
}

export interface Model {
  id: string
  name: string
}

export interface SkillUploadResult {
  skill: Skill
  created_version_number: number
  activated: boolean
}

export interface SkillBody {
  name: string
  description: string | null
  body: string
  has_secrets: boolean
  secret_keys: string[]
  /** 有 manifest.json 且 api_enabled=true */
  api_enabled?: boolean
  /** manifest 里开了 llm_enabled——只有这种 skill 的模型设置才会真正生效 */
  llm_enabled?: boolean
  /** manifest.json 里声明的模型（会被界面设置覆盖） */
  manifest_model?: string | null
  /** 目录里有没有 manifest.json——配合 manifest_error 区分「没有」和「坏了」 */
  manifest_present?: boolean
  manifest_error?: string | null
}

/** 调用历史列表项——不含 params/result/progress 等大字段 */
export interface SkillRunSummary {
  id: string
  status: string
  duration_ms: number | null
  error: string | null
  created_at: string
  finished_at: string | null
  user_id: number
  api_key_id: number | null
  /** 第几次尝试；1 表示没重试过，界面上不显示角标 */
  attempt: number
}

/** 一次历史尝试的诊断快照（当前这次不在里面，它在顶层字段上） */
export interface SkillRunAttempt {
  attempt: number
  failure_kind: string | null
  error: string | null
  duration_ms: number | null
  started_at: string | null
  finished_at: string | null
  stdout_tail: string | null
  stderr_tail: string | null
}

export interface SkillRunDetail {
  id: string
  status: string
  duration_ms: number | null
  error: string | null
  created_at: string
  finished_at: string | null
  params: Record<string, unknown>
  result: Record<string, unknown> | null
  progress: Array<Record<string, unknown>>
  artifacts: Array<Record<string, unknown>>
  stderr_tail: string | null
  attempt: number
  max_attempts: number
  /** 失败分类：sandbox / timeout / script_error / llm_error / param_invalid … */
  failure_kind: string | null
  /** 能不能重试。**后端算好的，前端别自己推规则**——规则以后会变 */
  retryable: boolean
  attempts: SkillRunAttempt[]
}

export interface SkillRunDayCount {
  day: string
  total: number
  failed: number
}

export interface SkillRunStats {
  days: number
  total: number
  succeeded: number
  failed: number
  running: number
  /** 终态里成功的占比；没有终态样本时为 null */
  success_rate: number | null
  p50_ms: number | null
  p95_ms: number | null
  last_failure_at: string | null
  daily: SkillRunDayCount[]
}

/** 工作副本 vs 当前激活版本的差异 */
export interface WorkingDiff {
  /** 当前激活版本号；null = 还没有任何已发布版本 */
  from_version: number | null
  files: SkillFileDiff[]
}

export interface SkillFileNode {
  path: string
  size: number
  is_binary: boolean
}

export interface SkillFileContent {
  path: string
  content: string
  is_binary: boolean
}

export interface SkillVersion {
  id: number
  version_number: number
  notes: string | null
  created_at: string
  is_current: boolean
  created_by: number
  /** 发版时从 JWT 抽到的展示名；空串时前端 fallback 到 u<id> */
  created_by_name: string
  /** 'upload' | 'publish' | 'create' —— 这个版本是怎么产生的 */
  source: string
}

export interface SkillFileDiff {
  path: string
  /** 'added' | 'removed' | 'modified' */
  status: string
  /** unified diff 文本；二进制时是占位说明 */
  diff: string
}

export interface SkillVersionDiff {
  from_version: number | null
  to_version: number
  files: SkillFileDiff[]
}

/** UI 渲染用：把 assistant 的 tool_call 与之后的 tool 消息配对 */
export interface ToolInvocation {
  id: string
  name: string
  arguments: string
  status: 'pending' | 'running' | 'success' | 'error'
  output?: string
  error?: string
  /** export_artifact 等工具产出的可预览文件 */
  attachments?: Attachment[]
  /** running 起算的时间戳（ms），仅流式期间设置 */
  startedAt?: number
}

/** 跨会话记忆。source: explicit=模型记的 / extracted=自动抽取 / manual=手工添加 */
export interface Memory {
  id: string
  scope: string
  topic: string
  content: string
  source: 'explicit' | 'extracted' | 'manual'
  pinned: boolean
  hit_count: number
  last_used_at: string | null
  source_session_id: string | null
  /** 非空 = 已软删（仅 include_deleted 视图会返回非空值） */
  deleted_at: string | null
  created_at: string
  updated_at: string
}
