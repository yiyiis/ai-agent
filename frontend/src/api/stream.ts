import type { Attachment, TokenUsage } from '../types'

export type StreamEvent =
  | { type: 'user_message_id'; id: string }
  | { type: 'delta'; content?: string; reasoning?: string }
  | { type: 'tool_call_start'; id: string; name: string; arguments: string }
  | { type: 'tool_call_result'; id: string; output: string }
  | { type: 'tool_call_error'; id: string; error: string }
  | { type: 'round_progress'; usage: TokenUsage }
  | {
      type: 'memory_written'
      memory_id: string
      topic: string
      content: string
      is_update: boolean
    }
  | { type: 'tool_artifact'; tool_call_id: string; attachment: Attachment }
  | { type: 'skill_loaded'; tool_call_id: string; skill_id: number; name: string }
  | { type: 'done'; id: string; usage?: TokenUsage }
  | { type: 'error'; message: string }

export interface StreamOptions {
  signal?: AbortSignal
  /** 已交付事件的游标；重连从下一条开始补播。 */
  onSeq?: (seq: number) => void
  /** 已收到有效 SSE 响应，确认本次请求已进入后台订阅。 */
  onOpen?: () => void
}

type EventHandler = (ev: StreamEvent) => void
type RecoveryOptions = StreamOptions & {
  signal: AbortSignal
  onSeq: (seq: number) => void
}
export type StreamStarter = (
  onEvent: EventHandler,
  opts: RecoveryOptions,
) => Promise<boolean | void>

// 连接/读取故障向上抛，由订阅者重连；只有服务端业务错误产生 error 事件。
export async function consumeStream(
  res: Response,
  onEvent: EventHandler,
  opts?: StreamOptions,
) {
  if (res.status === 408 || res.status === 429 || res.status >= 500) {
    throw new Error(`HTTP ${res.status}`)
  }
  if (!res.ok) {
    onEvent({ type: 'error', message: `HTTP ${res.status}` })
    return
  }
  const contentType = res.headers.get('content-type') || ''
  if (contentType.includes('application/json')) {
    const body = await res.json()
    onEvent({ type: 'error', message: body?.msg || `HTTP ${res.status}` })
    return
  }
  if (!res.body || !contentType.includes('text/event-stream')) {
    throw new Error('服务器未返回事件流')
  }
  opts?.onOpen?.()

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  // id 与 data 可能落在不同的网络分块中，游标必须跨 read 保留。
  let pendingSeq: number | null = null
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      for (const raw of lines) {
        if (raw.startsWith('id: ')) {
          const seq = Number(raw.slice(4))
          pendingSeq = Number.isSafeInteger(seq) && seq >= 0 ? seq : null
          continue
        }
        if (!raw.startsWith('data: ')) continue
        const seq = pendingSeq
        pendingSeq = null
        let event: StreamEvent
        try {
          event = JSON.parse(raw.slice(6)) as StreamEvent
        } catch {
          continue
        }
        onEvent(event)
        if (seq != null) opts?.onSeq?.(seq)
      }
    }
  } catch (e) {
    if (!opts?.signal?.aborted) throw e
  } finally {
    reader.releaseLock()
  }
}

function waitForRetry(delay: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const finish = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', finish)
      resolve()
    }
    const timer = setTimeout(finish, delay)
    signal.addEventListener('abort', finish, { once: true })
  })
}

/** 只重接 GET 订阅，不重发创建/编辑/重新生成请求，避免重复执行任务。 */
export async function followStream(
  starter: StreamStarter,
  attach: (fromSeq: number, onEvent: EventHandler, opts: RecoveryOptions) => Promise<boolean>,
  opts: RecoveryOptions & {
    onEvent: EventHandler
    onRetry?: (attempt: number, max: number) => void
    retryDelays?: readonly number[]
  },
): Promise<'terminal' | 'unavailable' | 'aborted' | 'disconnected'> {
  const { signal } = opts
  let terminal = false
  let opened = false
  let lastSeq = -1
  const onEvent: EventHandler = (ev) => {
    if (signal.aborted) return
    opened = true
    if (ev.type === 'done' || ev.type === 'error') terminal = true
    opts.onEvent(ev)
  }
  const streamOpts: RecoveryOptions = {
    signal,
    onOpen: () => {
      opened = true
      opts.onOpen?.()
    },
    onSeq: (seq) => {
      if (signal.aborted) return
      lastSeq = seq
      opts.onSeq(seq)
    },
  }
  if (signal.aborted) return 'aborted'
  try {
    if ((await starter(onEvent, streamOpts)) === false) return 'unavailable'
  } catch {
    // 网络故障不代表后台任务结束，交给下面的补播重试。
  }
  if (signal.aborted) return 'aborted'
  if (terminal) return 'terminal'
  // 初始 POST 未确认进入新 SSE 时，GET 可能接到仍保留的上一轮，不能盲目补播。
  if (!opened) return 'disconnected'
  const delays = opts.retryDelays ?? [500, 1000, 2000, 4000]
  for (let attempt = 0; attempt < delays.length; attempt++) {
    if (signal.aborted) return 'aborted'
    if (terminal) return 'terminal'
    opts.onRetry?.(attempt + 1, delays.length)
    await waitForRetry(delays[attempt], signal)
    if (signal.aborted) return 'aborted'
    try {
      if (!(await attach(lastSeq + 1, onEvent, streamOpts))) return 'unavailable'
    } catch {
      // 读取失败、网络仍离线或临时 HTTP 故障，继续下一次退避。
    }
  }
  if (signal.aborted) return 'aborted'
  return terminal ? 'terminal' : 'disconnected'
}
