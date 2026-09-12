import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertCircle,
  ArrowDown,
  Brain,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Code2,
  Copy,
  FileText,
  Lightbulb,
  Loader2,
  RotateCcw,
  Square,
  Wrench,
  X,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import {
  api,
  editUserMessage,
  regenerateMessage,
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

interface UserTurn {
  kind: 'user'
  key: string
  messageId: string
  content: string
  attachments?: Attachment[] | null
}

interface NoticeTurn {
  kind: 'notice'
  key: string
  content: string
}

export type ProcessItem =
  | { kind: 'reasoning'; key: string; content: string }
  | { kind: 'step_note'; key: string; content: string }
  | { kind: 'tool'; key: string; invocation: ToolInvocation }

export type TurnItem = ProcessItem

interface AssistantTurn {
  kind: 'assistant'
  key: string
  messageId?: string
  processItems: ProcessItem[]
  finalAnswer: string
}

type ChatTurn = UserTurn | NoticeTurn | AssistantTurn

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

function buildTurns(messages: Message[]): ChatTurn[] {
  const turns: ChatTurn[] = []
  const invocationById = new Map<string, ToolInvocation>()
  let currentAssistantTurn: AssistantTurn | null = null

  const closeAssistantTurn = () => {
    if (
      currentAssistantTurn &&
      (currentAssistantTurn.processItems.length > 0 || currentAssistantTurn.finalAnswer.trim() !== '')
    ) {
      turns.push(currentAssistantTurn)
    }
    currentAssistantTurn = null
  }

  for (const m of messages) {
    if (m.role === 'notice') {
      closeAssistantTurn()
      turns.push({ kind: 'notice', key: m.id, content: m.content })
    } else if (m.role === 'user') {
      closeAssistantTurn()
      turns.push({
        kind: 'user',
        key: m.id,
        messageId: m.id,
        content: m.content,
        attachments: m.attachments,
      })
    } else if (m.role === 'assistant') {
      if (!currentAssistantTurn) {
        currentAssistantTurn = {
          kind: 'assistant',
          key: m.id,
          processItems: [],
          finalAnswer: '',
        }
      }
      currentAssistantTurn.messageId = m.id

      // 1. 思考过程（即使没有正文 content，思考过程也必须完整保留与展示）
      if (m.reasoning) {
        currentAssistantTurn.processItems.push({
          kind: 'reasoning',
          key: `${m.id}-reasoning`,
          content: m.reasoning,
        })
      }

      // 2. 判断是否是工具调用轮次
      const hasToolCalls = Boolean(m.tool_calls && m.tool_calls.length > 0)

      if (hasToolCalls) {
        // 模型在该轮调用工具前若先输出了导言/说明（如"我帮你重新整理了一份 README..."），
        // 归类为过程项中的 step_note（阶段说明），纳入折叠面板，绝不污染最终总结正文
        if (m.content) {
          currentAssistantTurn.processItems.push({
            kind: 'step_note',
            key: `${m.id}-note`,
            content: m.content,
          })
        }

        // 工具调用卡片
        if (m.tool_calls) {
          for (const tc of m.tool_calls) {
            const inv: ToolInvocation = {
              id: tc.id,
              name: tc.function.name,
              arguments: tc.function.arguments,
              status: 'pending',
            }
            invocationById.set(tc.id, inv)
            currentAssistantTurn.processItems.push({
              kind: 'tool',
              key: tc.id,
              invocation: inv,
            })
          }
        }
      } else {
        // 无工具调用的轮次：属于最终交付总结文本
        if (m.content) {
          currentAssistantTurn.finalAnswer = currentAssistantTurn.finalAnswer
            ? `${currentAssistantTurn.finalAnswer}\n\n${m.content}`
            : m.content
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

  closeAssistantTurn()

  // 中断轮遗留的悬挂调用处理
  for (const inv of invocationById.values()) {
    if (inv.status === 'pending' || inv.status === 'running') {
      inv.status = 'error'
      inv.error = '[中断] 该轮对话未完成（连接中断或服务重启）'
    }
  }

  return turns
}

function ReasoningBlock({
  content,
  defaultOpen = false,
  autoCollapse = false,
}: {
  content: string
  defaultOpen?: boolean
  autoCollapse?: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  const userToggledRef = useRef(false)

  useEffect(() => {
    if (autoCollapse && !userToggledRef.current) {
      setOpen(false)
    }
  }, [autoCollapse])

  return (
    <div className="rounded-xl border border-[#e3e3e3] dark:border-[#3c4043] bg-[#f8f9fa] dark:bg-[#1e1f20] overflow-hidden">
      <button
        type="button"
        onClick={() => {
          userToggledRef.current = true
          setOpen((o) => !o)
        }}
        className="flex w-full items-center gap-2 px-3 py-2 text-xs text-[#747775] dark:text-[#9aa0a6] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] transition-colors"
      >
        <Brain className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8]" />
        <span className="font-medium">思考过程</span>
        {open ? (
          <ChevronDown className="w-3.5 h-3.5 ml-auto" />
        ) : (
          <ChevronRight className="w-3.5 h-3.5 ml-auto" />
        )}
      </button>
      {open && (
        <div className="px-3 pb-2.5 text-xs leading-relaxed text-[#747775] dark:text-[#9aa0a6] whitespace-pre-wrap break-words border-t border-[#e3e3e3]/50 dark:border-[#3c4043]/50 pt-2 font-mono">
          {content}
        </div>
      )}
    </div>
  )
}

function ProcessAccordion({
  items,
  streaming,
  hasAnswer,
  onPreview,
}: {
  items: ProcessItem[]
  streaming?: boolean
  hasAnswer?: boolean
  onPreview?: (a: Attachment) => void
}) {
  // 当流式时默认展开，方便实时查看执行动作；流式结束且有正文交付时，默认折叠
  const [open, setOpen] = useState(Boolean(streaming || !hasAnswer))
  const userToggledRef = useRef(false)
  const prevStreamingRef = useRef(streaming)

  useEffect(() => {
    // 流式结束且有正文时，若用户没有手动展开/折叠过，自动收起
    if (prevStreamingRef.current && !streaming && hasAnswer && !userToggledRef.current) {
      setOpen(false)
    }
    prevStreamingRef.current = streaming
  }, [streaming, hasAnswer])

  const toolCount = useMemo(
    () => items.filter((i) => i.kind === 'tool').length,
    [items],
  )

  const hasError = useMemo(
    () =>
      items.some(
        (i) =>
          i.kind === 'tool' &&
          (i.invocation.status === 'error' || Boolean(i.invocation.error)),
      ),
    [items],
  )

  const runningTool = useMemo(() => {
    if (!streaming) return null
    for (let i = items.length - 1; i >= 0; i--) {
      const item = items[i]
      if (item.kind === 'tool' && item.invocation.status === 'running') {
        return item.invocation
      }
    }
    return null
  }, [items, streaming])

  return (
    <div className="rounded-xl border border-[#e3e3e3] dark:border-[#3c4043] bg-[#f8f9fa] dark:bg-[#1e1f20] overflow-hidden transition-all duration-200">
      <button
        type="button"
        onClick={() => {
          userToggledRef.current = true
          setOpen((o) => !o)
        }}
        className="flex w-full items-center justify-between px-3.5 py-2.5 text-xs text-[#444746] dark:text-[#c4c7c5] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] transition-colors"
      >
        <div className="flex items-center gap-2 min-w-0 font-medium">
          {runningTool ? (
            <>
              <Loader2 className="w-3.5 h-3.5 animate-spin text-[#1a73e8] dark:text-[#8ab4f8] shrink-0" />
              <span className="truncate text-[#1a73e8] dark:text-[#8ab4f8]">
                正在执行 {runningTool.name}…
              </span>
            </>
          ) : hasError ? (
            <>
              <AlertCircle className="w-3.5 h-3.5 text-amber-600 dark:text-amber-400 shrink-0" />
              <span className="truncate text-amber-700 dark:text-amber-300">
                已执行 {toolCount} 项操作（含异常）
              </span>
            </>
          ) : (
            <>
              <CheckCircle2 className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400 shrink-0" />
              <span className="truncate text-[#1f1f1f] dark:text-[#e3e3e3]">
                已完成 {toolCount} 项操作与思考
              </span>
            </>
          )}
        </div>
        <div className="flex items-center gap-1 shrink-0 text-[#747775] dark:text-[#9aa0a6] text-[11px] ml-2">
          <span>{open ? '收起步骤' : '查看步骤'}</span>
          {open ? (
            <ChevronDown className="w-3.5 h-3.5" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5" />
          )}
        </div>
      </button>

      {open && (
        <div className="px-3.5 py-3 border-t border-[#e3e3e3]/70 dark:border-[#3c4043]/70 space-y-3 bg-[#fdfdfd] dark:bg-[#1b1c1d]">
          {items.map((item, idx) => {
            if (item.kind === 'reasoning') {
              const isLast = idx === items.length - 1
              return (
                <ReasoningBlock
                  key={item.key}
                  content={item.content}
                  defaultOpen={streaming && isLast}
                  autoCollapse={streaming ? !isLast : false}
                />
              )
            }
            if (item.kind === 'step_note') {
              return (
                <div
                  key={item.key}
                  className="flex items-start gap-2 px-2.5 py-1.5 rounded-lg bg-[#f0f4f9]/70 dark:bg-[#28292a]/70 text-xs text-[#444746] dark:text-[#c4c7c5] leading-relaxed border-l-2 border-[#1a73e8] dark:border-[#8ab4f8]"
                >
                  <span className="font-medium text-[#1a73e8] dark:text-[#8ab4f8] shrink-0 select-none">
                    阶段说明:
                  </span>
                  <span className="break-words select-text">{item.content}</span>
                </div>
              )
            }
            if (item.kind === 'tool') {
              return (
                <ToolCallCard
                  key={item.key}
                  invocation={item.invocation}
                  onPreview={onPreview}
                />
              )
            }
            return null
          })}
        </div>
      )}
    </div>
  )
}

function AssistantTurnView({
  processItems,
  finalAnswer,
  isLastAssistant,
  streaming,
  onRegenerate,
  onPreview,
}: {
  processItems: ProcessItem[]
  finalAnswer: string
  messageId?: string
  isLastAssistant?: boolean
  streaming?: boolean
  onRegenerate?: () => void
  onPreview?: (a: Attachment) => void
}) {
  const [copied, setCopied] = useState(false)

  const copy = () => {
    if (!finalAnswer) return
    navigator.clipboard.writeText(finalAnswer)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  const hasTools = processItems.some((i) => i.kind === 'tool')
  const hasAnswer = Boolean(finalAnswer.trim())

  return (
    <div className="group flex gap-4 w-full font-sans animate-fade-in">
      {/* 助手专属头像：整个回答轮次的最顶端只显示一次 */}
      <div className="shrink-0 pt-0.5">
        <BrandIcon className="w-6 h-6" />
      </div>

      {/* 回合内容体 */}
      <div className="flex-1 min-w-0 flex flex-col space-y-3">
        {/* 1. 若调用了工具：渲染统一折叠面板 ProcessAccordion */}
        {hasTools && (
          <ProcessAccordion
            items={processItems}
            streaming={streaming}
            hasAnswer={hasAnswer}
            onPreview={onPreview}
          />
        )}

        {/* 2. 若未调用任何工具，但有思考过程：按轻量化 ReasoningBlock 原生展示 */}
        {!hasTools &&
          processItems.map((item) => {
            if (item.kind === 'reasoning') {
              return (
                <ReasoningBlock
                  key={item.key}
                  content={item.content}
                  defaultOpen={streaming && !hasAnswer}
                  autoCollapse={hasAnswer}
                />
              )
            }
            return null
          })}

        {/* 3. 最终交付结果（正文） */}
        {hasAnswer && (
          <div className="text-[15px] leading-relaxed text-[#1f1f1f] dark:text-[#e3e3e3] markdown-body select-text">
            <ReactMarkdown
              remarkPlugins={[remarkGfm]}
              rehypePlugins={[rehypeHighlight]}
            >
              {finalAnswer}
            </ReactMarkdown>
            {streaming && (
              <span className="inline-block w-2 h-4 ml-1 bg-[#1a73e8] dark:bg-[#8ab4f8] animate-pulse align-middle rounded-sm" />
            )}
          </div>
        )}

        {/* 4. 流式起始且完全没有内容时的等待中指示 */}
        {streaming && !hasAnswer && processItems.length === 0 && (
          <div className="text-xs text-[#747775] flex items-center gap-2 pt-1">
            <Loader2 className="w-3.5 h-3.5 animate-spin text-[#1a73e8]" />
            <span>思考中…</span>
          </div>
        )}

        {/* 5. 底部操作栏：复制（仅复制最终交付正文）与重新生成 */}
        {!streaming && (hasAnswer || processItems.length > 0) && (
          <div className="flex items-center gap-1 pt-1 opacity-0 group-hover:opacity-100 transition-opacity">
            {hasAnswer && (
              <button
                onClick={copy}
                className="p-1.5 text-[#747775] dark:text-[#9aa0a6] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded-full transition-colors"
                title="复制回答"
              >
                {copied ? (
                  <Check className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
                ) : (
                  <Copy className="w-4 h-4" />
                )}
              </button>
            )}
            {isLastAssistant && onRegenerate && (
              <button
                onClick={onRegenerate}
                className="p-1.5 text-[#747775] dark:text-[#9aa0a6] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded-full transition-colors"
                title="重新生成"
              >
                <RotateCcw className="w-4 h-4" />
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  )
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
  const [streamProcessItems, setStreamProcessItems] = useState<ProcessItem[]>([])
  const [streamFinalText, setStreamFinalText] = useState('')
  const streamFinalTextRef = useRef('')
  const [optimisticUser, setOptimisticUser] = useState<Message | null>(null)
  const [title, setTitle] = useState('')
  // 启用的 skill + 各自锁定的版本（null = 跟随激活）
  const [enabledEntries, setEnabledEntries] = useState<EnabledSkillEntry[]>([])
  const [allSkills, setAllSkills] = useState<Skill[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [currentModel, setCurrentModel] = useState<string>('')
  // 上一轮对话实际使用的模型：用于在模型真正变化的那一轮展示切换分隔条
  const lastRoundModelRef = useRef<string>('')
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
  const itemCounterRef = useRef(0)
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
      setStreamProcessItems([])
      setStreamFinalText('')
      streamFinalTextRef.current = ''
      setOptimisticUser(null)
      setStreaming(false)
      setEditingId(null)
      setEnabledEntries([])
      setAutoSkill(true)
      lastRoundModelRef.current = ''
      return
    }

    setLoading(true)
    setStreamProcessItems([])
    setStreamFinalText('')
    streamFinalTextRef.current = ''
    setOptimisticUser(null)
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
  }, [messages, streamProcessItems, streamFinalText, optimisticUser, streaming, scrollToBottom])

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
    for (let i = streamProcessItems.length - 1; i >= 0; i--) {
      const item = streamProcessItems[i]
      if (item.kind === 'tool' && item.invocation.status === 'running') {
        return { label: `正在执行 ${item.invocation.name}`, kind: 'tool' as const }
      }
    }
    if (streamFinalText) {
      return { label: '正在生成…', kind: 'gen' as const }
    }
    return { label: '思考中…', kind: 'think' as const }
  }, [streaming, streamProcessItems, streamFinalText])

  const updateLastReasoning = (delta: string) => {
    setStreamProcessItems((prev) => {
      const last = prev[prev.length - 1]
      if (last && last.kind === 'reasoning') {
        const next = [...prev]
        next[next.length - 1] = { ...last, content: last.content + delta }
        return next
      }
      const key = `stream-reasoning-${++itemCounterRef.current}`
      return [...prev, { kind: 'reasoning', key, content: delta }]
    })
  }

  const updateLastText = (delta: string) => {
    streamFinalTextRef.current += delta
    setStreamFinalText(streamFinalTextRef.current)
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
    // 严格时序：思考过程必须全部吐完排空，才允许吐正文，杜绝二者逐帧交替切碎
    if (pendingReasoningRef.current) {
      const n = Math.max(2, Math.ceil(Array.from(pendingReasoningRef.current).length / 6))
      const [take, rest] = takeRunes(pendingReasoningRef.current, n)
      pendingReasoningRef.current = rest
      updateLastReasoning(take)
    } else if (pendingTextRef.current) {
      const n = Math.max(2, Math.ceil(Array.from(pendingTextRef.current).length / 6))
      const [take, rest] = takeRunes(pendingTextRef.current, n)
      pendingTextRef.current = rest
      updateLastText(take)
    }
    rafRef.current = requestAnimationFrame(pumpStream)
  }

  const startStreamPump = () => {
    if (pumpActiveRef.current) return
    pumpActiveRef.current = true
    rafRef.current = requestAnimationFrame(pumpStream)
  }

  // 停泵并把剩余缓冲一次性吐完（done/error/停止生成时调用，保证内容完整）
  const stopStreamPump = () => {
    pumpActiveRef.current = false
    if (rafRef.current != null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
    if (pendingReasoningRef.current) {
      updateLastReasoning(pendingReasoningRef.current)
      pendingReasoningRef.current = ''
    }
    if (pendingTextRef.current) {
      updateLastText(pendingTextRef.current)
      pendingTextRef.current = ''
    }
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
    setStreamProcessItems((prev) => {
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
      const item = next[idx] as Extract<ProcessItem, { kind: 'tool' }>
      next[idx] = {
        ...item,
        invocation: { ...item.invocation, ...patch },
      }
      return next
    })
  }

  const handleEvent = (ev: StreamEvent) => {
    if (ev.type === 'delta') {
      // 后台标签页 rAF 冻结，打字机泵不转：直接落屏，避免回来时文字停在老位置。
      // 先冲掉队列残留，保证与直写文本的先后顺序。
      if (document.hidden) {
        if (pendingReasoningRef.current) {
          updateLastReasoning(pendingReasoningRef.current)
          pendingReasoningRef.current = ''
        }
        if (pendingTextRef.current) {
          updateLastText(pendingTextRef.current)
          pendingTextRef.current = ''
        }
        if (ev.reasoning) {
          updateLastReasoning(ev.reasoning)
        }
        if (ev.content) {
          updateLastText(ev.content)
        }
        return
      }
      if (ev.reasoning) {
        pendingReasoningRef.current += ev.reasoning
      }
      if (ev.content) {
        pendingTextRef.current += ev.content
      }
    } else if (ev.type === 'tool_call_start') {
      // 1. 工具卡片前队列中的思考和文字必须先吐完
      if (pendingReasoningRef.current) {
        updateLastReasoning(pendingReasoningRef.current)
        pendingReasoningRef.current = ''
      }
      if (pendingTextRef.current) {
        updateLastText(pendingTextRef.current)
        pendingTextRef.current = ''
      }
      // 2. 关键时序：若模型在调工具前输出了导言文本（如"我帮你重新整理了一份 README..."），
      // 将其固化为过程项中的 step_note，正文区清空以备最终交付总结使用
      const activeText = streamFinalTextRef.current.trim()
      if (activeText) {
        streamFinalTextRef.current = ''
        setStreamFinalText('')
        setStreamProcessItems((prev) => [
          ...prev,
          {
            kind: 'step_note',
            key: `stream-note-${++itemCounterRef.current}`,
            content: activeText,
          },
        ])
      }
      const startedAt = Date.now()
      updateInvocation(
        ev.id,
        { status: 'running', startedAt, name: ev.name, arguments: ev.arguments },
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
      setStreamProcessItems((prev) => {
        const idx = prev.findIndex(
          (s) => s.kind === 'tool' && s.invocation.id === ev.tool_call_id,
        )
        if (idx === -1) return prev
        const next = [...prev]
        const item = next[idx] as Extract<ProcessItem, { kind: 'tool' }>
        next[idx] = {
          ...item,
          invocation: {
            ...item.invocation,
            attachments: [...(item.invocation.attachments || []), ev.attachment],
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
      // 流结束：停泵并吐完剩余缓冲
      stopStreamPump()
      const currentId = activeIdRef.current
      if (currentId) {
        api.getSession(currentId).then((s) => {
          setMessages(s.messages)
          // 自动加载会改动启用列表——跟着刷新，避免面板里状态过期
          if (s.enabled_skills) setEnabledEntries(s.enabled_skills)
          setStreamProcessItems([])
          setStreamFinalText('')
          streamFinalTextRef.current = ''
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
        // 错误信息以横幅形式独立展示，避免被清掉一闪而过。
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
    setStreamProcessItems([])
    setStreamFinalText('')
    streamFinalTextRef.current = ''
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

    // 新一轮开始：清空上一轮的打字机缓冲
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

  // 重新生成：本地截断旧回答，调用 /regenerate 端点重新流式生成
  const regenerate = async () => {
    if (streaming || !activeIdRef.current) return
    setMessages((prev) => {
      let lastUserIdx = -1
      for (let i = prev.length - 1; i >= 0; i--) {
        if (prev[i].role === 'user') {
          lastUserIdx = i
          break
        }
      }
      if (lastUserIdx === -1) return prev
      return prev.slice(0, lastUserIdx + 1)
    })
    await runStream(
      (onEvent, opts) => regenerateMessage(activeIdRef.current!, onEvent, opts),
    )
  }

  const historyTurns = useMemo(() => buildTurns(messages), [messages])
  const isEmpty = !loading && historyTurns.length === 0 && !optimisticUser && !streaming

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
                {historyTurns.map((turn) => {
                  if (turn.kind === 'notice') {
                    return (
                      <div key={turn.key} className="flex items-center gap-3 py-1 select-none" aria-hidden>
                        <div className="flex-1 h-px bg-[#e3e3e3] dark:bg-[#3c4043]" />
                        <span className="text-[11px] text-[#747775] dark:text-[#9aa0a6] whitespace-nowrap">
                          {turn.content}
                        </span>
                        <div className="flex-1 h-px bg-[#e3e3e3] dark:bg-[#3c4043]" />
                      </div>
                    )
                  }
                  if (turn.kind === 'user') {
                    return (
                      <MessageBubble
                        key={turn.key}
                        role="user"
                        content={turn.content}
                        attachments={turn.attachments}
                        onPreview={setPreview}
                        isEditing={turn.messageId === editingId}
                        onSubmitEdit={
                          !streaming && turn.messageId
                            ? (content) =>
                                submitEdit(turn.messageId, content, turn.attachments ?? null)
                            : undefined
                        }
                        onCancelEdit={editingId ? () => setEditingId(null) : undefined}
                        onEdit={
                          !streaming && turn.messageId && turn.content
                            ? () => setEditingId(turn.messageId)
                            : undefined
                        }
                      />
                    )
                  }
                  if (turn.kind === 'assistant') {
                    return (
                      <AssistantTurnView
                        key={turn.key}
                        processItems={turn.processItems}
                        finalAnswer={turn.finalAnswer}
                        messageId={turn.messageId}
                        isLastAssistant={turn.messageId === lastAssistantId}
                        streaming={false}
                        onRegenerate={regenerate}
                        onPreview={setPreview}
                      />
                    )
                  }
                  return null
                })}
                {optimisticUser && (
                  <MessageBubble
                    role="user"
                    content={optimisticUser.content}
                    attachments={optimisticUser.attachments}
                    onPreview={setPreview}
                  />
                )}
                {streaming && (
                  <AssistantTurnView
                    processItems={streamProcessItems}
                    finalAnswer={streamFinalText}
                    streaming={true}
                    onPreview={setPreview}
                  />
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
