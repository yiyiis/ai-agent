import type {
  Attachment,
  LoginResult,
  Memory,
  Model,
  Session,
  SessionDetail,
  Skill,
  SkillBody,
  SkillFileContent,
  SkillFileNode,
  SkillUploadResult,
  SkillVersion,
  SkillRunDetail,
  SkillRunStats,
  SkillRunSummary,
  SkillVersionDiff,
  WorkingDiff,
} from '../types'
import { readJwtCookie } from '../lib/auth'

const BASE = '/api'

// 全局 401 处理器；App.tsx 启动时注册成"清状态 + 跳登录"
type UnauthorizedHandler = () => void
let onUnauthorized: UnauthorizedHandler | null = null
export function setUnauthorizedHandler(h: UnauthorizedHandler | null) {
  onUnauthorized = h
}

export function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = { ...(extra ?? {}) }
  const token = readJwtCookie()
  if (token) headers['x-token'] = token
  return headers
}

async function http<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...authHeaders(),
      ...(init?.headers as Record<string, string> | undefined),
    },
    credentials: 'include',
  })
  if (res.status === 401) {
    if (onUnauthorized) onUnauthorized()
    throw new Error('unauthorized')
  }
  if (!res.ok) throw new Error(`${res.status} ${await res.text()}`)
  if (res.status === 204) return undefined as T
  return unwrapEnvelope<T>(res)
}

// 后端统一信封 {code, msg, data}：code!==0 视为业务失败，抛出 msg
async function unwrapEnvelope<T>(res: Response): Promise<T> {
  const body = await res.json()
  if (body && typeof body === 'object' && 'code' in body) {
    if (body.code !== 0) throw new Error(body.msg || '系统异常')
    return (body.data ?? undefined) as T
  }
  return body as T
}

export const api = {
  listSessions: () => http<Session[]>('/sessions'),

  // ---- 跨会话记忆 ----
  memoryStatus: () =>
    http<{ enabled: boolean; reasons: string[] }>('/memories/status'),

  listMemories: (includeDeleted = false) =>
    http<Memory[]>(`/memories${includeDeleted ? '?include_deleted=true' : ''}`),

  createMemory: (data: { scope: string; topic: string; content: string; pinned?: boolean }) =>
    http<Memory>('/memories', { method: 'POST', body: JSON.stringify(data) }),

  updateMemory: (id: string, data: Partial<Pick<Memory, 'topic' | 'content' | 'pinned'>>) =>
    http<Memory>(`/memories/${id}`, { method: 'PATCH', body: JSON.stringify(data) }),

  deleteMemory: (id: string) =>
    http<void>(`/memories/${id}`, { method: 'DELETE' }),

  restoreMemory: (id: string) =>
    http<Memory>(`/memories/${id}/restore`, { method: 'POST' }),

  createSession: (data?: Partial<Pick<Session, 'title' | 'model' | 'system_prompt'>>) =>
    http<Session>('/sessions', {
      method: 'POST',
      body: JSON.stringify(data ?? {}),
    }),

  getSession: (id: string) => http<SessionDetail>(`/sessions/${id}`),

  updateSession: (
    id: string,
    data: {
      title?: string
      system_prompt?: string | null
      model?: string
      auto_skill?: boolean
    },
  ) =>
    http<Session>(`/sessions/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    }),

  listModels: () => http<Model[]>('/models'),

  deleteSession: (id: string) =>
    http<void>(`/sessions/${id}`, { method: 'DELETE' }),

  /**
   * 设置会话启用的 skill 列表。
   * @param skills 每项 { skill_id, pinned_version_number? }；
   *   pinned_version_number=null/undefined 表示跟随当前激活版本
   */
  setEnabledSkills: (
    sessionId: string,
    skills: Array<{ skill_id: number; pinned_version_number?: number | null }>,
  ) =>
    http<void>(`/sessions/${sessionId}/skills`, {
      method: 'PUT',
      body: JSON.stringify({ skills }),
    }),

  login: (account: string, password: string) =>
    http<LoginResult>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ account, password }),
    }),

  listSkills: () => http<Skill[]>('/skills'),

  /**
   * 更新 skill 元信息。
   * model 传 '' 表示清空（跟随 manifest / 后端默认）；tags 传 [] 表示清空。
   */
  updateSkill: (
    id: number,
    data: { confidential?: boolean; model?: string; tags?: string[] },
  ) =>
    http<Skill>(`/skills/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    }),

  createEmptySkill: (data: { name: string; description?: string | null }) =>
    http<Skill>('/skills', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  getSkillBody: (id: number) => http<SkillBody>(`/skills/${id}/body`),

  getSkillSecrets: (id: number) =>
    http<Record<string, string>>(`/skills/${id}/secrets`),

  updateSkillSecrets: (id: number, secrets: Record<string, string>) =>
    http<void>(`/skills/${id}/secrets`, {
      method: 'PUT',
      body: JSON.stringify({ secrets }),
    }),

  uploadSkill: async (
    file: File,
    confidential = false,
  ): Promise<SkillUploadResult> => {
    const fd = new FormData()
    fd.append('file', file)
    fd.append('confidential', String(confidential))
    const res = await fetch(`${BASE}/skills/upload`, {
      method: 'POST',
      body: fd,
      headers: authHeaders(),
      credentials: 'include',
    })
    if (res.status === 401) {
      if (onUnauthorized) onUnauthorized()
      throw new Error('unauthorized')
    }
    if (!res.ok) throw new Error(`${res.status} ${await res.text()}`)
    return res.json()
  },

  deleteSkill: (id: number) =>
    http<void>(`/skills/${id}`, { method: 'DELETE' }),

  // ---------- skill 在线编辑 ----------
  getSkillTree: (id: number) =>
    http<SkillFileNode[]>(`/skills/${id}/tree`),

  getSkillFile: (id: number, path: string) =>
    http<SkillFileContent>(`/skills/${id}/file?path=${encodeURIComponent(path)}`),

  writeSkillFile: (id: number, path: string, content: string) =>
    http<void>(`/skills/${id}/file?path=${encodeURIComponent(path)}`, {
      method: 'PUT',
      body: JSON.stringify({ content }),
    }),

  createSkillFile: (id: number, path: string, content: string) =>
    http<void>(`/skills/${id}/file?path=${encodeURIComponent(path)}`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    }),

  deleteSkillFile: (id: number, path: string) =>
    http<void>(`/skills/${id}/file?path=${encodeURIComponent(path)}`, {
      method: 'DELETE',
    }),

  renameSkillFile: (id: number, oldPath: string, newPath: string) =>
    http<void>(`/skills/${id}/file/rename`, {
      method: 'POST',
      body: JSON.stringify({ old_path: oldPath, new_path: newPath }),
    }),

  // ---------- 版本管理 ----------
  listSkillVersions: (id: number) =>
    http<SkillVersion[]>(`/skills/${id}/versions`),

  publishSkillVersion: (id: number, notes: string | null) =>
    http<SkillVersion>(`/skills/${id}/versions`, {
      method: 'POST',
      body: JSON.stringify({ notes }),
    }),

  activateSkillVersion: (id: number, versionId: number) =>
    http<void>(`/skills/${id}/versions/${versionId}/activate`, { method: 'POST' }),

  restoreSkillVersion: (id: number, versionId: number) =>
    http<void>(`/skills/${id}/versions/${versionId}/restore`, { method: 'POST' }),

  deleteSkillVersion: (id: number, versionId: number) =>
    http<void>(`/skills/${id}/versions/${versionId}`, { method: 'DELETE' }),

  diffSkillVersion: (id: number, versionId: number) =>
    http<SkillVersionDiff>(`/skills/${id}/versions/${versionId}/diff`),

  /** 工作副本 vs 当前激活版本——发布前预览这次改了什么 */
  diffSkillWorkingCopy: (id: number) =>
    http<WorkingDiff>(`/skills/${id}/working-diff`),

  listSkillRuns: (
    id: number,
    opts: { days?: number; limit?: number; status?: string } = {},
  ) => {
    const q = new URLSearchParams()
    if (opts.days) q.set('days', String(opts.days))
    if (opts.limit) q.set('limit', String(opts.limit))
    if (opts.status) q.set('status', opts.status)
    return http<SkillRunSummary[]>(`/skills/${id}/runs?${q}`)
  },

  getSkillRunStats: (id: number, days = 7) =>
    http<SkillRunStats>(`/skills/${id}/runs/stats?days=${days}`),

  getSkillRunDetail: (id: number, runId: string) =>
    http<SkillRunDetail>(`/skills/${id}/runs/${runId}`),

  /** 重试一次失败的调用。**复用同一个 runId**，返回重置后的详情。 */
  retrySkillRun: (id: number, runId: string) =>
    http<SkillRunDetail>(`/skills/${id}/runs/${runId}/retry`, { method: 'POST' }),

  downloadSkillVersion: async (
    id: number,
    versionId: number,
    fallbackName: string,
    versionNumber: number,
  ): Promise<void> => {
    const res = await fetch(`${BASE}/skills/${id}/versions/${versionId}/download`, {
      headers: authHeaders(),
      credentials: 'include',
    })
    if (res.status === 401) {
      if (onUnauthorized) onUnauthorized()
      throw new Error('unauthorized')
    }
    if (!res.ok) throw new Error(`${res.status} ${await res.text()}`)

    let filename = `${fallbackName || 'skill'}-v${versionNumber}.zip`
    const dispo = res.headers.get('content-disposition') || ''
    const m = dispo.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i)
    if (m) filename = decodeURIComponent(m[1])

    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  },

  downloadSkill: async (id: number, fallbackName: string): Promise<void> => {
    const res = await fetch(`${BASE}/skills/${id}/download`, {
      headers: authHeaders(),
      credentials: 'include',
    })
    if (res.status === 401) {
      if (onUnauthorized) onUnauthorized()
      throw new Error('unauthorized')
    }
    if (!res.ok) throw new Error(`${res.status} ${await res.text()}`)

    // 优先用后端 Content-Disposition 里的 filename，回退到 <name>.zip
    let filename = `${fallbackName || 'skill'}.zip`
    const dispo = res.headers.get('content-disposition') || ''
    const m = dispo.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i)
    if (m) filename = decodeURIComponent(m[1])

    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  },

  uploadAttachment: async (file: File): Promise<Attachment> => {
    const fd = new FormData()
    fd.append('file', file)
    const res = await fetch(`${BASE}/uploads`, {
      method: 'POST',
      body: fd,
      headers: authHeaders(),
      credentials: 'include',
    })
    if (res.status === 401) {
      if (onUnauthorized) onUnauthorized()
      throw new Error('unauthorized')
    }
    if (!res.ok) throw new Error(`${res.status} ${await res.text()}`)
    return unwrapEnvelope<Attachment>(res)
  },

  // 启动时 ping 一下，确认登录态并拿到 user_id / company_id
  me: () => http<{ user_id: number; company_id: number; name?: string }>('/auth/me'),
}

export type StreamEvent =
  | { type: 'user_message_id'; id: string }
  | { type: 'delta'; content?: string; reasoning?: string }
  | { type: 'tool_call_start'; id: string; name: string; arguments: string }
  | { type: 'tool_call_result'; id: string; output: string }
  | { type: 'tool_call_error'; id: string; error: string }
  | {
      type: 'memory_written'
      memory_id: string
      topic: string
      content: string
      is_update: boolean
    }
  | { type: 'tool_artifact'; tool_call_id: string; attachment: Attachment }
  | {
      type: 'skill_loaded'
      tool_call_id: string
      skill_id: number
      name: string
    }
  | { type: 'done'; id: string }
  | { type: 'error'; message: string }

interface StreamOptions {
  signal?: AbortSignal
}

async function consumeStream(
  res: Response,
  onEvent: (ev: StreamEvent) => void,
  signal?: AbortSignal,
) {
  if (!res.ok || !res.body) {
    onEvent({ type: 'error', message: `HTTP ${res.status}` })
    return
  }
  // 流开始前的失败（鉴权/会话隔离等）以标准信封 JSON 返回，而非 SSE 帧
  const ct = res.headers.get('content-type') || ''
  if (ct.includes('application/json')) {
    const body = await res.json().catch(() => null)
    onEvent({ type: 'error', message: body?.msg || `HTTP ${res.status}` })
    return
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      for (const raw of lines) {
        if (!raw.startsWith('data: ')) continue
        try {
          onEvent(JSON.parse(raw.slice(6)) as StreamEvent)
        } catch {
          // 忽略解析失败的片段
        }
      }
    }
  } catch (e) {
    if (signal?.aborted) {
      // 用户主动停止——上层会自己派发 done 事件
      return
    }
    onEvent({ type: 'error', message: String(e) })
  }
}

export async function streamMessage(
  sessionId: string,
  content: string,
  onEvent: (ev: StreamEvent) => void,
  opts?: StreamOptions & { attachments?: Attachment[] },
) {
  const body: Record<string, unknown> = { content }
  if (opts?.attachments?.length) body.attachments = opts.attachments
  const res = await fetch(`${BASE}/sessions/${sessionId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    credentials: 'include',
    body: JSON.stringify(body),
    signal: opts?.signal,
  })
  if (res.status === 401 && onUnauthorized) onUnauthorized()
  await consumeStream(res, onEvent, opts?.signal)
}

export async function editUserMessage(
  sessionId: string,
  messageId: string,
  content: string,
  onEvent: (ev: StreamEvent) => void,
  opts?: StreamOptions & { attachments?: Attachment[] },
) {
  const body: Record<string, unknown> = { content }
  if (opts?.attachments?.length) body.attachments = opts.attachments
  const res = await fetch(
    `${BASE}/sessions/${sessionId}/messages/${messageId}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', ...authHeaders() },
      credentials: 'include',
      body: JSON.stringify(body),
      signal: opts?.signal,
    },
  )
  if (res.status === 401 && onUnauthorized) onUnauthorized()
  await consumeStream(res, onEvent, opts?.signal)
}
