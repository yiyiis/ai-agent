import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertCircle,
  ArrowDown,
  Brain,
  ChevronDown,
  ChevronRight,
  Code2,
  FileText,
  Lightbulb,
  Loader2,
  Square,
  Wrench,
  X,
} from 'lucide-react'
import {
  api,
  editUserMessage,
  streamMessage,
  type StreamEvent,
} from '../api/client'
import type {
  Attachment,
  EnabledSkillEntry,
  Message,
  Model,
  Skill,
  ToolInvocation,
} from '../types'
import { BrandIcon } from './BrandIcons'
import { Composer } from './Composer'
import { FilePreviewModal } from './FilePreviewModal'
import { MessageBubble } from './MessageBubble'
import { ModelPicker } from './ModelPicker'
import { SkillsPanel } from './SkillsPanel'
import { ToolCallCard } from './ToolCallCard'

interface Props {
  sessionId: string | null
  onTitleChange: () => void
  userName?: string
  onSessionNotFound?: (errorMsg: string) => void
  onEmptyChange?: (isEmpty: boolean) => void
  onSessionCreated?: (newSessionId: string) => void
}

type Segment =
  | {
      kind: 'text'
      key: string
      role: 'user' | 'assistant'
      content: string
      reasoning?: string | null
      messageId?: string
      attachments?: Attachment[] | null
    }
  | { kind: 'tool'; key: string; invocation: ToolInvocation }
  | { kind: 'notice'; key: string; content: string }

/** 与后端 tools.errorPrefixes 保持一致：工具结果以这些前缀开头时按失败渲染 */
const TOOL_ERROR_PREFIXES = [
  '[执行失败]',
  '[超时]',
  '[未读取]',
  '[未改动]',
  '[参数缺失]',
  '[参数解析失败]',
  '[参数超限]',
]

function isToolError(content: string): boolean {
  return TOOL_ERROR_PREFIXES.some((p) => content.startsWith(p))
}

/** 把后端返回的扁平消息列表展开成 UI 渲染段。 */
function buildSegments(messages: Message[]): Segment[] {
  const segs: Segment[] = []
  const invocationById = new Map<string, ToolInvocation>()

  for (const m of messages) {
    if (m.role === 'notice') {
      segs.push({ kind: 'notice', key: m.id, content: m.content })
    } else if (m.role === 'user') {
      segs.push({
        kind: 'text',
        key: m.id,
        role: 'user',
        content: m.content,
        messageId: m.id,
        attachments: m.attachments,
      })
    } else if (m.role === 'assistant') {
      if (m.content) {
        segs.push({
          kind: 'text',
          key: m.id,
          role: 'assistant',
          content: m.content,
          reasoning: m.reasoning,
          messageId: m.id,
        })
      }
      if (m.tool_calls) {
        for (const tc of m.tool_calls) {
          const inv: ToolInvocation = {
            id: tc.id,
            name: tc.function.name,
            arguments: tc.function.arguments,
            status: 'pending',
          }
          invocationById.set(tc.id, inv)
          segs.push({ kind: 'tool', key: tc.id, invocation: inv })
        }
      }
    } else if (m.role === 'tool' && m.tool_call_id) {
      const inv = invocationById.get(m.tool_call_id)
      if (inv) {
        if (isToolError(m.content)) {
          inv.status = 'error'
          inv.error = m.content
        } else {
          inv.status = 'success'
          inv.output = m.content
        }
        if (m.attachments && m.attachments.length > 0) {
          inv.attachments = m.attachments
        }
      }
    }
  }
  // 中断轮遗留的悬挂调用（流式中断/刷新/重启，没有对应 tool 消息）不能永远转圈
  for (const inv of invocationById.values()) {
    if (inv.status === 'pending' || inv.status === 'running') {
      inv.status = 'error'
      inv.error = '[中断] 该轮对话未完成（连接中断或服务重启）'
    }
  }
  return segs
}

/** 把字符串按 rune 切成 [前 n 个, 剩余]，避免切断代理对 */
function takeRunes(s: string, n: number): [string, string] {
  const runes = Array.from(s)
  if (runes.length <= n) return [s, '']
  return [runes.slice(0, n).join(''), runes.slice(n).join('')]
}

export function ChatArea({
  sessionId,
  onTitleChange,
  userName,
  onSessionNotFound,
  onEmptyChange,
  onSessionCreated,
}: Props) {
  const [messages, setMessages] = useState<Message[]>([])
  const [loading, setLoading] = useState(Boolean(sessionId))
  const [streaming, setStreaming] = useState(false)
  const [streamSegs, setStreamSegs] = useState<Segment[]>([])
  const [optimisticUser, setOptimisticUser] = useState<Message | null>(null)
  const [title, setTitle] = useState('')
  // 启用的 skill + 各自锁定的版本（null = 跟随激活）
  const [enabledEntries, setEnabledEntries] = useState<EnabledSkillEntry[]>([])
  const [allSkills, setAllSkills] = useState<Skill[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [currentModel, setCurrentModel] = useState<string>('')
  // 上一轮对话实际使用的模型：用于在模型真正变化的那一轮展示切换分隔条
  const lastRoundModelRef = useRef<string>('')
  // 流式思考过程（如 MiniMax-M3 的 <think> 块）：流式期间展开，正文开始后自动收起
  const [streamReasoning, setStreamReasoning] = useState('')
  const [reasoningOpen, setReasoningOpen] = useState(true)
  const sawReasoningRef = useRef(false)
  const [skillsOpen, setSkillsOpen] = useState(false)
  const [autoSkill, setAutoSkill] = useState(true)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [preview, setPreview] = useState<Attachment | null>(null)
  const [errorBanner, setErrorBanner] = useState<string | null>(null)
  // 自动加载 skill 的提示条——和错误条同一个位置，但不是错误，用中性配色
  const [noticeBanner, setNoticeBanner] = useState<string | null>(null)
  // 模型刚记下的跨会话记忆。**必须可见且能当场删掉**——记忆会注入之后每一轮
  // 对话，一句被误记的话（比如来自用户上传文档里的"请记住…"）会永久生效。
  // 用独立状态而不是复用 noticeBanner：这条是可操作的，不能几秒后自己消失。
  const [memoryNotice, setMemoryNotice] = useState<
    { id: string; content: string; isUpdate: boolean } | null
  >(null)
  const errorTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const noticeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const segCounterRef = useRef(0)
  const abortRef = useRef<AbortController | null>(null)

  // 内部维护当前实际会话 ID（草稿态为 null，首次发送消息后切换为生成的 ID）
  const activeIdRef = useRef<string | null>(sessionId)
  // 记录当前已成功加载就绪的会话 ID，避免草稿态转正或重复切换时重复拉取，同时保证页面初次载入或 F5 刷新时正常请求
  const loadedIdRef = useRef<string | null>(null)

  useEffect(() => {
    let cancelled = false

    // 若外部传入的 sessionId 与内部当前已加载就绪的会话一致（如首条消息触发路由替换为 /c/:id），无需重新拉取或重置
    if (sessionId && sessionId === loadedIdRef.current) {
      return
    }

    activeIdRef.current = sessionId

    if (!sessionId) {
      loadedIdRef.current = null
      // 草稿态 / 新会话：无须向后端请求，直接进入空状态
      setMessages([])
      setTitle('新会话')
      setLoading(false)
      setStreamSegs([])
      setOptimisticUser(null)
      setStreaming(false)
      setEditingId(null)
      setEnabledEntries([])
      setAutoSkill(true)
      lastRoundModelRef.current = ''
      setStreamReasoning('')
      setReasoningOpen(true)
      return
    }

    setLoading(true)
    setStreamSegs([])
    setOptimisticUser(null)
    setStreamReasoning('')
    setReasoningOpen(true)
    api
      .getSession(sessionId)
      .then((s) => {
        if (cancelled) return
        loadedIdRef.current = sessionId
        setMessages(s.messages)
        setTitle(s.title)
        // 优先用新的 enabled_skills（带 pin 信息）；老后端 / 老接口降级到 enabled_skill_ids
        setEnabledEntries(
          s.enabled_skills ??
            s.enabled_skill_ids.map((id) => ({
              skill_id: id,
              pinned_version_number: null,
            })),
        )
        setCurrentModel(s.model)
        lastRoundModelRef.current = s.model
        // 老后端没有这个字段——按默认开处理
        setAutoSkill(s.auto_skill !== false)
        // 悬空轮回滚还原：服务中断/中止留下的提问回到输入框
        if (s.pending_question?.content) {
          restoreComposer(s.pending_question.content, s.pending_question.attachments)
          setNoticeBanner('上一轮对话未完成，提问已还原到输入框')
          if (noticeTimerRef.current) clearTimeout(noticeTimerRef.current)
          noticeTimerRef.current = setTimeout(() => setNoticeBanner(null), 6000)
        }
      })
      .catch((err) => {
        if (cancelled) return
        loadedIdRef.current = null
        const msg = String(err)
        // 后端对"不存在"与"无权"统一报会话不存在（防存在性探测）
        if (msg.includes('会话不存在')) {
          onSessionNotFound?.('该会话不存在或已被删除')
        } else {
          onSessionNotFound?.(`加载会话失败：${msg}`)
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })
    setEditingId(null)

    return () => {
      cancelled = true
    }
  }, [sessionId, onSessionNotFound])

  // 模型列表全局只拉一次（不依赖会话切换）
  useEffect(() => {
    api
      .listModels()
      .then((list) => {
        setModels(list)
        if (list.length > 0) {
          setCurrentModel((prev) => prev || list[0].id)
        }
      })
      .catch(() => setModels([]))
  }, [])

  useEffect(() => {
    if (!skillsOpen) {
      api.listSkills().then(setAllSkills)
    }
  }, [skillsOpen])

  const enabledIdSet = useMemo(
    () => new Set(enabledEntries.map((e) => e.skill_id)),
    [enabledEntries],
  )
  const enabledSkills = useMemo(
    () => allSkills.filter((s) => enabledIdSet.has(s.id)),
    [allSkills, enabledIdSet],
  )

  // 贴底跟随：滚轮向上/触摸/滚动条上拖都视为"用户要阅读"，立即释放跟随；
  // 只有真正回到距底 24px 内才重新吸附。程序化滚动不触发这些意图信号。
  const stickRef = useRef(true)
  const lastTopRef = useRef(0)
  const lastHeightRef = useRef(0)
  const [showJump, setShowJump] = useState(false)

  const scrollToBottom = useCallback((smooth = false) => {
    const box = scrollRef.current
    if (!box) return
    const targetTop = box.scrollHeight - box.clientHeight
    box.scrollTo({ top: targetTop, behavior: smooth ? 'smooth' : 'auto' })
    lastTopRef.current = targetTop
  }, [])

  const handleScroll = useCallback((e: React.UIEvent<HTMLDivElement>) => {
    const box = e.currentTarget
    const maxScrollTop = box.scrollHeight - box.clientHeight
    const distance = maxScrollTop - box.scrollTop
    // 视口上方内容收缩（思考面板收起、代码高亮回流、图片加载等）会带动 scrollTop 回落，
    // 那是布局噪声不是用户意图——只有 scrollHeight 没变时的向上滚才解除吸附
    const shrank = box.scrollHeight < lastHeightRef.current
    if (!shrank && box.scrollTop < lastTopRef.current - 4) {
      stickRef.current = false
    } else if (distance < 8 || (box.scrollTop > lastTopRef.current + 2 && distance < 24)) {
      // 已贴近底部：无条件恢复吸附（含浏览器 clamp 类回落到贴底位置的情况）
      stickRef.current = true
    }
    lastTopRef.current = box.scrollTop
    lastHeightRef.current = box.scrollHeight
    setShowJump(distance >= 48)
  }, [])

  // 滚轮向上脱钩需要"真实意图"：free-spin 滚轮回落、触控板抬手动量都会产生
  // 微小负向增量，单帧一票否决会让贴底频繁意外脱钩——
  // 只有 400ms 窗口内累计向上超过 40px（或单次大力上滚）才判定为用户要读历史
  const wheelUpAccRef = useRef(0)
  const wheelAtRef = useRef(0)

  const handleWheel = useCallback((e: React.WheelEvent<HTMLDivElement>) => {
    const now = performance.now()
    if (now - wheelAtRef.current > 400) {
      wheelUpAccRef.current = 0
    }
    wheelAtRef.current = now

    if (e.deltaY < 0) {
      wheelUpAccRef.current += e.deltaY
      if (wheelUpAccRef.current <= -40) {
        // 持续/大力向上滚动滚轮：解除吸附并允许自由浏览
        stickRef.current = false
        setShowJump(true)
      }
    } else if (e.deltaY > 0) {
      const box = scrollRef.current
      if (box) {
        const distance = box.scrollHeight - box.clientHeight - box.scrollTop
        if (distance < 24) {
          stickRef.current = true
          setShowJump(false)
        }
      }
    }
  }, [])

  const handleTouchStart = useCallback(() => {
    // 触控屏幕开始拖动时，若已不在底部则解除吸附
    const box = scrollRef.current
    if (box) {
      const distance = box.scrollHeight - box.clientHeight - box.scrollTop
      if (distance > 24) {
        stickRef.current = false
      }
    }
  }, [])

  // 结构性变化（消息增减/工具卡）立即贴底；
  // 流式期间必须用 auto——smooth 动画会被高频更新反复打断，表现为"跟不上底"
  useEffect(() => {
    if (stickRef.current) scrollToBottom(false)
  }, [messages, streamSegs, optimisticUser, streaming, scrollToBottom])

  // 流式期间每帧贴底：思考阶段 delta 极密，逐事件滚动会被浏览器合并丢弃，
  // rAF 每帧强制贴底才能保持跟随，若用户向上滑动则立即停止
  useEffect(() => {
    if (!streaming) return
    let raf = 0
    const tick = () => {
      if (stickRef.current) scrollToBottom(false)
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [streaming, scrollToBottom])

  // 切会话时复位粘底
  useEffect(() => {
    stickRef.current = true
    setShowJump(false)
    scrollToBottom(false)
  }, [sessionId, scrollToBottom])

  const jumpToBottom = useCallback(() => {
    stickRef.current = true
    setShowJump(false)
    scrollToBottom(true)
  }, [scrollToBottom])

  // 推导实时状态：最近一个 running 工具优先，否则视情况显示思考/生成
  const liveStatus = useMemo(() => {
    if (!streaming) return null
    for (let i = streamSegs.length - 1; i >= 0; i--) {
      const seg = streamSegs[i]
      if (seg.kind === 'tool' && seg.invocation.status === 'running') {
        return { label: `正在执行 ${seg.invocation.name}`, kind: 'tool' as const }
      }
    }
    const last = streamSegs[streamSegs.length - 1]
    if (last && last.kind === 'text' && last.role === 'assistant') {
      return { label: '正在生成…', kind: 'gen' as const }
    }
    return { label: '思考中…', kind: 'think' as const }
  }, [streaming, streamSegs])

  const updateLastText = (delta: string) => {
    setStreamSegs((prev) => {
      const last = prev[prev.length - 1]
      if (last && last.kind === 'text' && last.role === 'assistant') {
        const next = [...prev]
        next[next.length - 1] = { ...last, content: last.content + delta }
        return next
      }
      const key = `seg-${++segCounterRef.current}`
      return [
        ...prev,
        { kind: 'text', key, role: 'assistant', content: delta },
      ]
    })
  }

  // ---- 流式打字机平滑 ----
  // 上游 SSE 的 chunk 是短语级大块，直接渲染会"一段一段蹦"；
  // 这里把 delta 入队，由 rAF 每帧小口吐字，积压越多每帧吐越多（约 6 帧追平）
  const pendingTextRef = useRef('')
  const pendingReasoningRef = useRef('')
  const rafRef = useRef<number | null>(null)
  const pumpActiveRef = useRef(false)

  const pumpStream = () => {
    if (!pumpActiveRef.current) return
    if (pendingTextRef.current) {
      const n = Math.max(2, Math.ceil(Array.from(pendingTextRef.current).length / 6))
      const [take, rest] = takeRunes(pendingTextRef.current, n)
      pendingTextRef.current = rest
      updateLastText(take)
    }
    if (pendingReasoningRef.current) {
      const n = Math.max(2, Math.ceil(Array.from(pendingReasoningRef.current).length / 6))
      const [take, rest] = takeRunes(pendingReasoningRef.current, n)
      pendingReasoningRef.current = rest
      setStreamReasoning((prev) => prev + take)
    }
    rafRef.current = requestAnimationFrame(pumpStream)
  }

  const startStreamPump = () => {
    if (pumpActiveRef.current) return
    pumpActiveRef.current = true
    rafRef.current = requestAnimationFrame(pumpStream)
  }

  // 停泵并把剩余正文缓冲一次性吐完（done/error/停止生成时调用，保证内容完整）；
  // 思考缓冲直接丢弃——此时实时面板即将被落库的折叠面板替代
  const stopStreamPump = () => {
    pumpActiveRef.current = false
    if (rafRef.current != null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
    if (pendingTextRef.current) {
      updateLastText(pendingTextRef.current)
      pendingTextRef.current = ''
    }
    pendingReasoningRef.current = ''
  }

  // 组件卸载时停掉 rAF，避免泄漏
  useEffect(() => {
    return () => {
      pumpActiveRef.current = false
      if (rafRef.current != null) cancelAnimationFrame(rafRef.current)
    }
  }, [])

  const updateInvocation = (
    id: string,
    patch: Partial<ToolInvocation>,
    fallback?: ToolInvocation,
  ) => {
    setStreamSegs((prev) => {
      const idx = prev.findIndex(
        (s) => s.kind === 'tool' && s.invocation.id === id,
      )
      if (idx === -1) {
        if (fallback) {
          return [
            ...prev,
            { kind: 'tool', key: id, invocation: fallback },
          ]
        }
        return prev
      }
      const next = [...prev]
      const seg = next[idx] as Extract<Segment, { kind: 'tool' }>
      next[idx] = {
        ...seg,
        invocation: { ...seg.invocation, ...patch },
      }
      return next
    })
  }

  const handleEvent = (ev: StreamEvent) => {
    if (ev.type === 'delta') {
      // 后台标签页 rAF 冻结，打字机泵不转：直接落屏，避免回来时文字停在老位置。
      // 先冲掉队列残留，保证与直写文本的先后顺序。
      if (document.hidden) {
        if (pendingTextRef.current) {
          updateLastText(pendingTextRef.current)
          pendingTextRef.current = ''
        }
        if (pendingReasoningRef.current) {
          setStreamReasoning((prev) => prev + pendingReasoningRef.current)
          pendingReasoningRef.current = ''
        }
        if (ev.reasoning) {
          sawReasoningRef.current = true
          setStreamReasoning((prev) => prev + ev.reasoning)
        }
        if (ev.content) {
          if (sawReasoningRef.current) setReasoningOpen(false)
          updateLastText(ev.content)
        }
        return
      }
      if (ev.reasoning) {
        sawReasoningRef.current = true
        pendingReasoningRef.current += ev.reasoning
      }
      if (ev.content) {
        pendingTextRef.current += ev.content
        // 思考结束、正文开始输出：思考面板自动收起
        if (sawReasoningRef.current) setReasoningOpen(false)
      }
    } else if (ev.type === 'tool_call_start') {
      // 工具卡片前的文本必须先吐完，否则队列剩余文本会错位到卡片之后
      if (pendingTextRef.current) {
        updateLastText(pendingTextRef.current)
        pendingTextRef.current = ''
      }
      const startedAt = Date.now()
      updateInvocation(
        ev.id,
        { status: 'running', startedAt },
        {
          id: ev.id,
          name: ev.name,
          arguments: ev.arguments,
          status: 'running',
          startedAt,
        },
      )
    } else if (ev.type === 'tool_call_result') {
      updateInvocation(ev.id, { status: 'success', output: ev.output })
    } else if (ev.type === 'tool_call_error') {
      updateInvocation(ev.id, { status: 'error', error: ev.error })
    } else if (ev.type === 'tool_artifact') {
      // 流式追加：把 artifact 挂到对应 invocation 上
      setStreamSegs((prev) => {
        const idx = prev.findIndex(
          (s) => s.kind === 'tool' && s.invocation.id === ev.tool_call_id,
        )
        if (idx === -1) return prev
        const next = [...prev]
        const seg = next[idx] as Extract<Segment, { kind: 'tool' }>
        next[idx] = {
          ...seg,
          invocation: {
            ...seg.invocation,
            attachments: [...(seg.invocation.attachments || []), ev.attachment],
          },
        }
        return next
      })
    } else if (ev.type === 'skill_loaded') {
      // 模型按意图自动加载了一个 skill：同步到启用列表（Skills 面板/角标立刻反映），
      // 并给一条提示条，让用户知道这轮多用了什么能力
      setEnabledEntries((prev) =>
        prev.some((e) => e.skill_id === ev.skill_id)
          ? prev
          : [
              ...prev,
              {
                skill_id: ev.skill_id,
                pinned_version_number: null,
                source: 'auto',
              },
            ],
      )
      setNoticeBanner(`已自动启用 skill：${ev.name}`)
      if (noticeTimerRef.current) clearTimeout(noticeTimerRef.current)
      noticeTimerRef.current = setTimeout(() => setNoticeBanner(null), 6000)
    } else if (ev.type === 'memory_written') {
      setMemoryNotice({
        id: ev.memory_id,
        content: ev.content,
        isUpdate: Boolean(ev.is_update),
      })
    } else if (ev.type === 'done' || ev.type === 'error') {
      // 流结束：停泵并吐完剩余缓冲，落库的思考过程以折叠面板形式留在回答上方
      stopStreamPump()
      setStreamReasoning('')
      setReasoningOpen(true)
      sawReasoningRef.current = false
      const currentId = activeIdRef.current
      if (currentId) {
        api.getSession(currentId).then((s) => {
          setMessages(s.messages)
          // 自动加载会改动启用列表——跟着刷新，避免面板里状态过期
          if (s.enabled_skills) setEnabledEntries(s.enabled_skills)
          setStreamSegs([])
          setOptimisticUser(null)
          setStreaming(false)
          abortRef.current = null
          if (s.title !== title) {
            setTitle(s.title)
            onTitleChange()
          }
        })
      }
      if (ev.type === 'error') {
        // 错误信息以横幅形式独立展示，避免在 streamSegs 里被 getSession.then 清掉一闪而过。
        // 8s 后自动消失；用户也可手动 ×。
        setErrorBanner(ev.message)
        if (errorTimerRef.current) clearTimeout(errorTimerRef.current)
        errorTimerRef.current = setTimeout(() => setErrorBanner(null), 8000)
      }
    }
  }

  const runStream = async (
    starter: (
      onEvent: (ev: StreamEvent) => void,
      opts: { signal: AbortSignal },
    ) => Promise<void>,
    optimistic?: Message | null,
  ) => {
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setOptimisticUser(optimistic ?? null)
    setStreaming(true)
    setStreamSegs([])
    pendingTextRef.current = ''
    pendingReasoningRef.current = ''
    startStreamPump()
    let sawDone = false
    // 包一层 handleEvent，记录是否见过 done/error——后端任何路径若漏 yield done，
    // 末尾的兜底会补发，确保 setStreaming(false) 一定被触发。
    const onEvent = (ev: StreamEvent) => {
      if (ev.type === 'done' || ev.type === 'error') sawDone = true
      handleEvent(ev)
    }
    try {
      await starter(onEvent, { signal: ctrl.signal })
    } catch (e) {
      if (!ctrl.signal.aborted) {
        onEvent({ type: 'error', message: String(e) })
      }
    }
    // 兜底：无论 abort、异常还是 SSE 正常关闭，确保 streaming 状态被清掉。
    // 重复触发 handleEvent('done') 是幂等的（最多多一次无害的 getSession）。
    if (!sawDone) {
      handleEvent({ type: 'done', id: '' })
    }
  }

  const send = async (content: string, attachments: Attachment[] = []) => {
    if (streaming) return

    let currentId = activeIdRef.current
    if (!currentId) {
      // 惰性创建：在用户发送首条消息时，才向后端真正申请创建会话
      try {
        const newSession = await api.createSession({
          model: currentModel || undefined,
        })
        currentId = newSession.id
        activeIdRef.current = newSession.id
        loadedIdRef.current = newSession.id
        if (enabledEntries.length > 0) {
          await api.setEnabledSkills(newSession.id, enabledEntries)
        }
        onSessionCreated?.(newSession.id)
      } catch (e) {
        setErrorBanner(`创建会话失败: ${String(e)}`)
        return
      }
    }

    stickRef.current = true
    setShowJump(false)
    scrollToBottom(false)

    // 记录本轮提问：停止生成/中断时用于还原到输入框
    lastAskRef.current = { content, attachments }

    // 新一轮开始：清空上一轮的思考过程展示与打字机缓冲
    setStreamReasoning('')
    setReasoningOpen(true)
    sawReasoningRef.current = false
    pendingTextRef.current = ''
    pendingReasoningRef.current = ''

    // 模型切换分隔条：对比上一轮实际使用的模型，真正发生变化才展示。
    // A→B→A 连续切换但中间没有对话时，上一轮与这一轮同为 A，不展示。
    if (
      lastRoundModelRef.current &&
      currentModel &&
      lastRoundModelRef.current !== currentModel
    ) {
      const nameOf = (id: string) => models.find((m) => m.id === id)?.name ?? id
      setMessages((prev) => [
        ...prev,
        {
          id: `switch-${Date.now()}`,
          role: 'notice',
          content: `模型从 ${nameOf(lastRoundModelRef.current)} 切换为 ${nameOf(currentModel)}`,
          created_at: new Date().toISOString(),
        },
      ])
    }
    lastRoundModelRef.current = currentModel

    if (editingId) return // 就地编辑在气泡内提交，不走底部输入框
    await runStream(
      (onEvent, opts) =>
        streamMessage(currentId!, content, onEvent, { ...opts, attachments }),
      {
        id: `temp-${Date.now()}`,
        role: 'user',
        content,
        attachments: attachments.length ? attachments : null,
        created_at: new Date().toISOString(),
      },
    )
  }

  // 就地编辑任意历史提问：截断其后全部记录，以编辑后内容重开一轮
  const submitEdit = async (
    messageId: string,
    content: string,
    attachments: Attachment[] | null,
  ) => {
    if (streaming || !activeIdRef.current) return
    setEditingId(null)
    // 本地立即截断：被编辑的提问及其后全部记录先从界面消失
    setMessages((prev) => {
      const idx = prev.findIndex((m) => m.id === messageId)
      return idx === -1 ? prev : prev.slice(0, idx)
    })
    await runStream(
      (onEvent, opts) =>
        editUserMessage(activeIdRef.current!, messageId, content, onEvent, {
          ...opts,
          attachments: attachments?.length ? attachments : undefined,
        }),
      {
        id: `temp-${Date.now()}`,
        role: 'user',
        content,
        attachments: attachments?.length ? attachments : null,
        created_at: new Date().toISOString(),
      },
    )
  }

  // 停止生成：中断流（服务端会回滚这轮未完成的记录），并把提问还原到输入框
  const lastAskRef = useRef<{ content: string; attachments: Attachment[] } | null>(null)
  const [composerKey, setComposerKey] = useState(0)
  const [composerValue, setComposerValue] = useState<string | undefined>(undefined)
  const [composerAttachments, setComposerAttachments] = useState<Attachment[] | null>(null)

  const restoreComposer = (content: string, attachments?: Attachment[] | null) => {
    setComposerValue(content)
    setComposerAttachments(attachments ?? null)
    setComposerKey((k) => k + 1) // 重复恢复同一段内容时也强制重挂载
  }

  const stop = () => {
    abortRef.current?.abort()
    if (lastAskRef.current) {
      restoreComposer(lastAskRef.current.content, lastAskRef.current.attachments)
      lastAskRef.current = null
    }
  }

  // 重试 = 原样重发最后一条提问：先在本地立刻移除旧一轮（提问+回答），
  // 再走编辑重发端点截断重流——不等网络返回，旧答案瞬间消失
  const regenerate = async () => {
    if (streaming || !activeIdRef.current) return
    let lastUser: Message | undefined
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === 'user') {
        lastUser = messages[i]
        break
      }
    }
    if (!lastUser) return
    await submitEdit(lastUser.id, lastUser.content, lastUser.attachments ?? null)
  }

  const historySegs = buildSegments(messages)
  const isEmpty = !loading && historySegs.length === 0 && !optimisticUser && !streaming

  useEffect(() => {
    if (!loading) {
      onEmptyChange?.(isEmpty)
    }
  }, [isEmpty, loading, onEmptyChange])

  // 任意 user 消息均可就地编辑；最后一条助手消息允许重生成
  const lastAssistantId = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === 'assistant') return messages[i].id
    }
    return null
  }, [messages])

  const handleModelChange = async (modelId: string) => {
    const prev = currentModel
    setCurrentModel(modelId)
    if (!activeIdRef.current) return
    try {
      await api.updateSession(activeIdRef.current, { model: modelId })
    } catch (e) {
      setCurrentModel(prev)
      setErrorBanner(`切换模型失败：${String(e)}`)
      if (errorTimerRef.current) clearTimeout(errorTimerRef.current)
      errorTimerRef.current = setTimeout(() => setErrorBanner(null), 6000)
    }
  }

  return (
    <div className="flex-1 flex flex-col h-full bg-white dark:bg-[#131314] bg-[radial-gradient(circle_at_50%_40%,_rgba(219,234,254,0.45)_0%,_rgba(240,244,249,0.15)_50%,_#ffffff_75%)] dark:bg-[radial-gradient(circle_at_50%_40%,_rgba(30,58,138,0.15)_0%,_rgba(24,24,27,0.3)_50%,_#131314_75%)] relative overflow-hidden">
      {/* 顶部轻量导航条 */}
      <header className="px-6 py-3 border-b border-[#e3e3e3]/70 dark:border-[#28292a] bg-white/80 dark:bg-[#1e1f20]/80 backdrop-blur-md sticky top-0 z-20 flex items-center gap-3 shrink-0">
        <div className="text-sm font-medium text-[#1f1f1f] dark:text-[#f1f3f4] truncate flex-1 tracking-tight">
          {title || (loading ? '加载中...' : '新会话')}
        </div>
        {!loading && !isEmpty && (
          <ModelPicker
            models={models}
            value={currentModel}
            disabled={streaming}
            onChange={handleModelChange}
          />
        )}
        <button
          onClick={() => setSkillsOpen(true)}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3] bg-white dark:bg-[#28292a] hover:bg-[#f0f4f9] dark:hover:bg-[#333537] border border-[#e3e3e3] dark:border-[#3c4043] rounded-full shadow-sm hover:border-[#c4c7c5] transition-all"
        >
          <BrandIcon className="w-3.5 h-3.5" />
          <span>Skills</span>
          {enabledEntries.length > 0 && (
            <span className="ml-0.5 px-1.5 py-0.5 bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] rounded-full text-[10px] font-semibold leading-none">
              {enabledEntries.length}
            </span>
          )}
        </button>
      </header>

      <SkillsPanel
        open={skillsOpen}
        onClose={() => setSkillsOpen(false)}
        sessionId={activeIdRef.current ?? ''}
        enabledEntries={enabledEntries}
        onEnabledChange={async (entries) => {
          setEnabledEntries(entries)
          if (activeIdRef.current) {
            try {
              await api.setEnabledSkills(activeIdRef.current, entries)
            } catch (e) {
              setErrorBanner(`保存技能配置失败: ${String(e)}`)
            }
          }
        }}
        autoSkill={autoSkill}
        onAutoSkillChange={async (next) => {
          const prev = autoSkill
          setAutoSkill(next)
          if (!activeIdRef.current) return
          try {
            await api.updateSession(activeIdRef.current, { auto_skill: next })
          } catch (e) {
            setAutoSkill(prev)
            setErrorBanner(`切换自动选 skill 失败：${String(e)}`)
            if (errorTimerRef.current) clearTimeout(errorTimerRef.current)
            errorTimerRef.current = setTimeout(() => setErrorBanner(null), 6000)
          }
        }}
      />

      {loading ? (
        <div className="flex-1 flex flex-col items-center justify-center -mt-10">
          <div className="w-12 h-12 rounded-2xl bg-[#f0f4f9] dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] flex items-center justify-center animate-pulse shadow-sm">
            <BrandIcon className="w-6 h-6 text-[#1a73e8]" />
          </div>
          <span className="mt-4 text-xs font-medium text-[#747775] dark:text-[#9aa0a6] tracking-wide animate-pulse">
            正在载入会话...
          </span>
        </div>
      ) : isEmpty ? (
        /* Gemini 经典居中欢迎态（忠实还原用户截图） */
        <div className="flex-1 flex flex-col items-center justify-center -mt-10 px-4 max-w-3xl w-full mx-auto animate-fade-in">
          <h1 className="text-3xl md:text-4xl font-normal text-[#1f1f1f] dark:text-[#f1f3f4] tracking-tight mb-8 text-center select-none">
            {userName ? `${userName}，有什么我可以协助你的？` : '你好，有什么我可以协助你的？'}
          </h1>
          <div className="w-full max-w-2xl mb-8">
            <Composer
              onSend={send}
              disabled={streaming}
              skills={enabledSkills}
              models={models}
              currentModel={currentModel}
              onModelChange={handleModelChange}
              placeholder="输入消息，随时开始..."
            />
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 w-full max-w-2xl text-left">
            {[
              {
                icon: Code2,
                title: '编写与优化代码',
                desc: '用 Python 实现带超时的异步并发请求函数',
                prompt: '用 Python 实现一个带超时与指数退避重试的异步并发请求函数，并附上使用示例。',
              },
              {
                icon: Wrench,
                title: '技能工具探索',
                desc: '了解当前可调用的技能和工具能力',
                prompt: '请介绍一下当前会话中已经启用的技能和工具，以及它们各自适合在什么场景下使用？',
              },
              {
                icon: Lightbulb,
                title: '头脑风暴与构思',
                desc: '规划智能体工作流与自动化架构',
                prompt: '假设我们要为一个研发团队搭建智能 Agent 工作流，推荐几种业界常见的落地架构与最佳实践。',
              },
              {
                icon: FileText,
                title: '分析与总结提炼',
                desc: '梳理一份高效的技术评审清单',
                prompt: '帮我设计一份高效的技术架构评审清单，包含安全性、扩展性、性能与可观测性等关键维度。',
              },
            ].map((card, i) => (
              <button
                key={i}
                onClick={() => send(card.prompt)}
                className="group p-3.5 rounded-2xl bg-white dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] hover:border-[#b4d7fe] dark:hover:border-[#1a73e8] hover:bg-[#f8fafd] dark:hover:bg-[#28292a] hover:shadow-gemini-pill transition-all duration-200 text-left flex flex-col justify-between"
              >
                <div className="flex items-center gap-2 mb-1.5">
                  <div className="w-6 h-6 rounded-lg bg-[#f0f4f9] dark:bg-[#28292a] text-[#1a73e8] dark:text-[#8ab4f8] flex items-center justify-center group-hover:bg-[#d3e3fd] dark:group-hover:bg-[#004a77] group-hover:text-[#041e49] dark:group-hover:text-[#c2e7ff] transition-colors">
                    <card.icon className="w-3.5 h-3.5" />
                  </div>
                  <span className="text-xs font-semibold text-[#1f1f1f] dark:text-[#f1f3f4] group-hover:text-[#1a73e8] dark:group-hover:text-[#8ab4f8] transition-colors">
                    {card.title}
                  </span>
                </div>
                <p className="text-[11px] text-[#747775] dark:text-[#9aa0a6] leading-relaxed line-clamp-2">
                  {card.desc}
                </p>
              </button>
            ))}
          </div>
        </div>
      ) : (
        /* 对话消息流与底部输入 */
        <>
          <div className="flex-1 min-h-0 relative flex flex-col overflow-hidden">
            <div
              ref={scrollRef}
              onScroll={handleScroll}
              onWheel={handleWheel}
              onTouchStart={handleTouchStart}
              className="flex-1 overflow-y-auto scrollbar-thin [overflow-anchor:none]"
            >
              <div className="max-w-3xl mx-auto px-6 py-6 space-y-6">
                {(() => {
                  let turnHasAssistantAvatar = false
                  return historySegs.map((s) => {
                    if (s.kind === 'notice') {
                      turnHasAssistantAvatar = false
                      return (
                        <div key={s.key} className="flex items-center gap-3 py-1 select-none" aria-hidden>
                          <div className="flex-1 h-px bg-[#e3e3e3] dark:bg-[#3c4043]" />
                          <span className="text-[11px] text-[#747775] dark:text-[#9aa0a6] whitespace-nowrap">
                            {s.content}
                          </span>
                          <div className="flex-1 h-px bg-[#e3e3e3] dark:bg-[#3c4043]" />
                        </div>
                      )
                    }
                    if (s.kind === 'text') {
                      if (s.role === 'user') {
                        turnHasAssistantAvatar = false
                        return (
                          <MessageBubble
                            key={s.key}
                            role="user"
                            content={s.content}
                            reasoning={s.reasoning}
                            attachments={s.attachments}
                            onPreview={setPreview}
                            isEditing={s.messageId === editingId}
                            onSubmitEdit={
                              !streaming && s.messageId
                                ? (content) =>
                                    submitEdit(s.messageId!, content, s.attachments ?? null)
                                : undefined
                            }
                            onCancelEdit={editingId ? () => setEditingId(null) : undefined}
                            onEdit={
                              !streaming && s.messageId && s.content
                                ? () => setEditingId(s.messageId!)
                                : undefined
                            }
                          />
                        )
                      }
                      const hideAvatar = turnHasAssistantAvatar
                      turnHasAssistantAvatar = true
                      return (
                        <MessageBubble
                          key={s.key}
                          role="assistant"
                          content={s.content}
                          reasoning={s.reasoning}
                          attachments={s.attachments}
                          hideAvatar={hideAvatar}
                          onPreview={setPreview}
                          onRegenerate={
                            !streaming &&
                            s.messageId === lastAssistantId
                              ? regenerate
                              : undefined
                          }
                        />
                      )
                    }
                    return (
                      <div key={s.key} className="pl-11 pr-2">
                        <ToolCallCard invocation={s.invocation} onPreview={setPreview} />
                      </div>
                    )
                  })
                })()}
                {optimisticUser && (
                  <MessageBubble
                    role="user"
                    content={optimisticUser.content}
                    attachments={optimisticUser.attachments}
                    onPreview={setPreview}
                  />
                )}
                {streamReasoning && (
                  <div className="pl-11 pr-2">
                    <div className="rounded-xl border border-[#e3e3e3] dark:border-[#3c4043] bg-[#f8f9fa] dark:bg-[#1e1f20] overflow-hidden">
                      <button
                        onClick={() => setReasoningOpen((o) => !o)}
                        className="flex w-full items-center gap-2 px-3 py-2 text-xs text-[#747775] dark:text-[#9aa0a6] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] transition-colors"
                      >
                        <Brain className="w-3.5 h-3.5" />
                        <span className="font-medium">思考过程</span>
                        {reasoningOpen ? (
                          <ChevronDown className="w-3.5 h-3.5 ml-auto" />
                        ) : (
                          <ChevronRight className="w-3.5 h-3.5 ml-auto" />
                        )}
                      </button>
                      {reasoningOpen && (
                        <div className="px-3 pb-2.5 text-xs leading-relaxed text-[#747775] dark:text-[#9aa0a6] whitespace-pre-wrap break-words">
                          {streamReasoning}
                        </div>
                      )}
                    </div>
                  </div>
                )}
                {(() => {
                  let streamHasAvatar = false
                  return streamSegs.map((s) => {
                    if (s.kind === 'text') {
                      const hideAvatar = streamHasAvatar
                      streamHasAvatar = true
                      return (
                        <MessageBubble
                          key={s.key}
                          role="assistant"
                          content={s.content}
                          streaming
                          hideAvatar={hideAvatar}
                          onPreview={setPreview}
                        />
                      )
                    }
                    if (s.kind === 'tool') {
                      return (
                        <div key={s.key} className="pl-11 pr-2">
                          <ToolCallCard invocation={s.invocation} onPreview={setPreview} />
                        </div>
                      )
                    }
                    return null
                  })
                })()}
                {streaming && streamSegs.length === 0 && (
                  <div className="text-xs text-[#747775] pl-11 flex items-center gap-2">
                    <Loader2 className="w-3.5 h-3.5 animate-spin text-[#1a73e8]" />
                    <span>思考中…</span>
                  </div>
                )}
              </div>
            </div>

            {/* 回到底部悬浮快捷按钮 */}
            {showJump && (
              <button
                onClick={jumpToBottom}
                className="absolute bottom-4 right-8 z-20 flex items-center gap-1.5 px-3.5 py-1.5 text-xs font-medium text-[#1f1f1f] dark:text-[#f1f3f4] bg-white/95 dark:bg-[#1e1f20]/95 hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] backdrop-blur-md border border-[#e3e3e3] dark:border-[#3c4043] hover:border-[#c4c7c5] rounded-full shadow-lg hover:shadow-xl transition-all duration-200 active:scale-95 animate-fade-in"
              >
                <ArrowDown className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8]" />
                <span>回到底部</span>
              </button>
            )}
          </div>

          {/* 运行中状态浮层 */}
          {streaming && (
            <div className="px-6 -mb-2 flex justify-center gap-2 z-10 animate-slide-up">
              {liveStatus && (
                <div className="flex items-center gap-2 px-3.5 py-1.5 text-xs bg-white/95 dark:bg-[#1e1f20]/95 backdrop-blur-md border border-[#e3e3e3] dark:border-[#3c4043] rounded-full shadow-md text-[#1f1f1f] dark:text-[#f1f3f4]">
                  <Loader2 className="w-3.5 h-3.5 animate-spin text-[#1a73e8] dark:text-[#8ab4f8]" />
                  <span className={liveStatus.kind === 'tool' ? 'font-mono text-[#1f1f1f] dark:text-[#f1f3f4] font-medium' : ''}>
                    {liveStatus.label}
                  </span>
                </div>
              )}
              <button
                onClick={stop}
                className="flex items-center gap-1.5 px-3.5 py-1.5 text-xs bg-white/95 dark:bg-[#1e1f20]/95 backdrop-blur-md border border-[#e3e3e3] dark:border-[#3c4043] rounded-full shadow-md hover:bg-rose-50 dark:hover:bg-rose-950/40 hover:text-rose-600 dark:hover:text-rose-400 hover:border-rose-200 dark:hover:border-rose-900 text-[#444746] dark:text-[#c4c7c5] transition font-medium"
              >
                <Square className="w-3 h-3 fill-current" />
                停止生成
              </button>
            </div>
          )}

          {/* 记忆保存通知浮层 */}
          {memoryNotice && (
            <div className="px-6 -mb-2 flex justify-center z-10 animate-slide-up">
              <div className="flex items-center gap-2 max-w-[80%] px-3.5 py-1.5 text-xs bg-[#d3e3fd]/90 dark:bg-[#004a77]/90 backdrop-blur-md border border-[#b4d7fe] dark:border-[#1a73e8] text-[#041e49] dark:text-[#c2e7ff] rounded-full shadow-sm">
                <Brain className="w-3.5 h-3.5 shrink-0 text-[#1a73e8] dark:text-[#8ab4f8]" />
                <span className="truncate">
                  {memoryNotice.isUpdate ? '已更新长期记忆' : '已记录到记忆库'}：{memoryNotice.content}
                </span>
                <button
                  onClick={async () => {
                    await api.deleteMemory(memoryNotice.id)
                    setMemoryNotice(null)
                  }}
                  className="px-1.5 py-0.5 hover:bg-[#b4d7fe] dark:hover:bg-[#1a3860] rounded text-[#041e49] dark:text-[#c2e7ff] underline font-medium shrink-0"
                  title="删掉这条记忆"
                >
                  撤销
                </button>
                <button
                  onClick={() => setMemoryNotice(null)}
                  className="p-0.5 hover:bg-[#b4d7fe] dark:hover:bg-[#1a3860] rounded text-[#041e49] dark:text-[#c2e7ff] shrink-0"
                  title="关闭"
                >
                  <X className="w-3 h-3" />
                </button>
              </div>
            </div>
          )}

          {/* 技能自动加载通知 */}
          {noticeBanner && (
            <div className="px-6 -mb-2 flex justify-center z-10 animate-slide-up">
              <div className="flex items-center gap-2 max-w-[80%] px-3.5 py-1.5 text-xs bg-white/95 dark:bg-[#1e1f20]/95 backdrop-blur-md border border-[#e3e3e3] dark:border-[#3c4043] text-[#1f1f1f] dark:text-[#f1f3f4] rounded-full shadow-sm">
                <BrandIcon className="w-3.5 h-3.5 shrink-0" />
                <span className="truncate">{noticeBanner}</span>
                <button
                  onClick={() => {
                    setNoticeBanner(null)
                    if (noticeTimerRef.current) clearTimeout(noticeTimerRef.current)
                  }}
                  className="p-0.5 hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded text-[#747775] dark:text-[#9aa0a6] shrink-0"
                  title="关闭"
                >
                  <X className="w-3 h-3" />
                </button>
              </div>
            </div>
          )}

          {/* 错误提示条 */}
          {errorBanner && !streaming && (
            <div className="px-6 -mb-2 flex justify-center z-10 animate-slide-up">
              <div className="flex items-center gap-2 max-w-[80%] px-3.5 py-1.5 text-xs bg-rose-50/95 dark:bg-rose-950/90 backdrop-blur-md border border-rose-200 dark:border-rose-900 text-rose-700 dark:text-rose-300 rounded-full shadow-sm">
                <AlertCircle className="w-3.5 h-3.5 shrink-0 text-rose-600 dark:text-rose-400" />
                <span className="truncate">{errorBanner}</span>
                <button
                  onClick={() => {
                    setErrorBanner(null)
                    if (errorTimerRef.current) clearTimeout(errorTimerRef.current)
                  }}
                  className="p-0.5 hover:bg-rose-100 dark:hover:bg-rose-900 rounded text-rose-500 shrink-0"
                  title="关闭"
                >
                  <X className="w-3 h-3" />
                </button>
              </div>
            </div>
          )}

          {/* 底部输入区域 */}
          <div className="max-w-3xl mx-auto w-full px-6 pb-4 pt-2 shrink-0">
            <Composer
              key={composerKey}
              onSend={send}
              disabled={streaming}
              skills={enabledSkills}
              models={models}
              currentModel={currentModel}
              onModelChange={handleModelChange}
              initialValue={composerValue}
              initialAttachments={composerAttachments}
            />
            <div className="text-center mt-2 text-[11px] text-[#747775] dark:text-[#9aa0a6]">
              AI 可能会显示不准确的信息，请仔细核对重要事实与关键结论。
            </div>
          </div>
        </>
      )}

      <FilePreviewModal attachment={preview} onClose={() => setPreview(null)} />
    </div>
  )
}
