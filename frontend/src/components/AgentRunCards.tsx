import { useEffect, useState } from 'react'
import { AlertCircle, Bot, CheckCircle2, ChevronDown, ChevronRight, Clock, Loader2, RefreshCw } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { api } from '../api/client'
import type { AgentDetail, AgentRun, AgentStatus, Message } from '../types'
import { markdownLinkComponents } from '../lib/markdown'

const statusLabels: Record<AgentStatus, string> = {
  queued: '等待执行',
  running: '执行中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
  interrupted: '已中断',
}

function StatusIcon({ status }: { status: AgentStatus }) {
  if (status === 'running') return <Loader2 className="h-3.5 w-3.5 animate-spin text-blue-500" />
  if (status === 'completed') return <CheckCircle2 className="h-3.5 w-3.5 text-emerald-600" />
  if (status === 'queued') return <Clock className="h-3.5 w-3.5 text-[#747775]" />
  return <AlertCircle className="h-3.5 w-3.5 text-amber-600" />
}

function AgentMessage({ message }: { message: Message }) {
  const label = message.role === 'tool' ? `工具 · ${message.name || message.tool_call_id || ''}`
    : message.role === 'user' ? '任务指令' : message.role === 'assistant' ? 'Agent 回复' : '记录'
  return (
    <article className="space-y-2 rounded-lg bg-[#f0f4f9] p-3 dark:bg-[#28292a]">
      <div className="text-[11px] font-medium text-[#747775] dark:text-[#9aa0a6]">{label}</div>
      {message.reasoning && (
        <details className="text-xs text-[#747775] dark:text-[#9aa0a6]">
          <summary className="cursor-pointer">思考过程</summary>
          <p className="mt-2 whitespace-pre-wrap break-words">{message.reasoning}</p>
        </details>
      )}
      {message.content && (message.role === 'tool'
        ? <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words text-xs">{message.content}</pre>
        : <div className="markdown-body text-sm"><ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownLinkComponents}>{message.content}</ReactMarkdown></div>)}
      {message.tool_calls?.map((call) => (
        <details key={call.id} className="text-xs">
          <summary className="cursor-pointer">调用 {call.function.name}</summary>
          <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words">{call.function.arguments}</pre>
        </details>
      ))}
      {message.attachments?.map((attachment, index) => (
        <ReactMarkdown key={`${attachment.url}-${index}`} components={markdownLinkComponents}>
          {`[${attachment.filename.replace(/[\[\]\\]/g, '')}](<${attachment.url.replace(/[<>\s]/g, encodeURIComponent)}>)`}
        </ReactMarkdown>
      ))}
    </article>
  )
}

function AgentRunCard({ agent, nested }: { agent: AgentRun; nested: boolean }) {
  const [open, setOpen] = useState(false)
  const [detail, setDetail] = useState<AgentDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    setError(null)
    api.getAgent(agent.session_id, agent.agent_id)
      .then((value) => { if (!cancelled) setDetail(value) })
      .catch(() => { if (!cancelled) setError('暂时无法读取执行记录，请重试。') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [open, agent.session_id, agent.agent_id, agent.status, revision])

  const messages = (detail?.messages ?? []).filter((message) => message.role !== 'system')
  return (
    <div className={`overflow-hidden rounded-xl border border-[#dadce0] bg-white dark:border-[#3c4043] dark:bg-[#1e1f20] ${nested ? 'ml-4' : ''}`}>
      <button type="button" onClick={() => setOpen((value) => !value)} aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left hover:bg-[#f0f4f9]/60 dark:hover:bg-[#28292a]">
        <Bot className="h-4 w-4 shrink-0 text-[#1a73e8] dark:text-[#8ab4f8]" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-xs font-medium">{agent.name || '子 Agent'}</span>
          <span className="mt-0.5 block truncate text-[11px] text-[#747775] dark:text-[#9aa0a6]">{agent.task}</span>
        </span>
        <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-[#747775] dark:text-[#9aa0a6]" aria-live="polite">
          <StatusIcon status={agent.status} />{statusLabels[agent.status] || agent.status}
        </span>
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
      </button>
      {open && (
        <div className="space-y-3 border-t border-[#dadce0] px-3 py-3 dark:border-[#3c4043]">
          <p className="whitespace-pre-wrap break-words text-xs text-[#444746] dark:text-[#c4c7c5]">{agent.task}</p>
          <div className="flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-[#747775] dark:text-[#9aa0a6]">
            <span>{agent.mode === 'read_only' ? '只读任务' : agent.mode === 'write' ? '可编辑任务' : agent.mode}</span>
            <span>{agent.model}</span>
          </div>
          {agent.error && <p role="alert" className="whitespace-pre-wrap break-words text-xs text-red-600 dark:text-red-400">{agent.error}</p>}
          {agent.result && (
            <div className="space-y-2">
              <h4 className="text-xs font-medium">执行结果</h4>
              <div className="markdown-body max-h-96 overflow-auto text-sm"><ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownLinkComponents}>{agent.result}</ReactMarkdown></div>
            </div>
          )}
          <div className="flex items-center justify-between">
            <h4 className="text-xs font-medium">独立执行记录</h4>
            <button type="button" onClick={() => setRevision((value) => value + 1)} disabled={loading}
              className="flex items-center gap-1 rounded px-2 py-1 text-[11px] text-[#1a73e8] hover:bg-[#f0f4f9] disabled:opacity-50 dark:text-[#8ab4f8] dark:hover:bg-[#28292a]">
              <RefreshCw className={`h-3 w-3 ${loading ? 'animate-spin' : ''}`} />刷新记录
            </button>
          </div>
          {error && <p role="alert" className="text-xs text-amber-700 dark:text-amber-400">{error}</p>}
          {loading && !detail && <p className="text-xs text-[#747775]">正在读取记录…</p>}
          {!loading && !error && messages.length === 0 && <p className="text-xs text-[#747775]">暂无执行记录</p>}
          <div className="max-h-[32rem] space-y-2 overflow-y-auto">
            {messages.map((message) => <AgentMessage key={message.id} message={message} />)}
          </div>
        </div>
      )}
    </div>
  )
}

export function AgentRunCards({ agents }: { agents: AgentRun[] }) {
  if (agents.length === 0) return null
  const ids = new Set(agents.map((agent) => agent.agent_id))
  return (
    <section aria-label="协作 Agent" className="max-w-3xl space-y-2 text-[#1f1f1f] dark:text-[#e3e3e3]">
      <h3 className="text-xs font-medium text-[#747775] dark:text-[#9aa0a6]">协作 Agent · {agents.length}</h3>
      {agents.map((agent) => <AgentRunCard key={agent.agent_id} agent={agent} nested={Boolean(agent.parent_agent_id && ids.has(agent.parent_agent_id))} />)}
    </section>
  )
}
