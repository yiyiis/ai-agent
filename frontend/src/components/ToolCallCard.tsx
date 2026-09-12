import { useEffect, useState } from 'react'
import { AlertCircle, CheckCircle2, ChevronDown, ChevronRight, FileText, Loader2, Terminal } from 'lucide-react'
import type { Attachment, ToolInvocation } from '../types'

const STATUS_CONFIG = {
  pending: {
    icon: <Loader2 className="w-3.5 h-3.5 animate-spin text-[#747775] dark:text-[#9aa0a6]" />,
    badge: 'bg-[#f0f4f9] dark:bg-[#28292a] text-[#747775] dark:text-[#9aa0a6] border-[#e3e3e3] dark:border-[#3c4043]',
    label: '排队中',
  },
  running: {
    icon: <Loader2 className="w-3.5 h-3.5 animate-spin text-[#1a73e8] dark:text-[#8ab4f8]" />,
    badge: 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] border-[#b4d7fe] dark:border-[#1a73e8]',
    label: '运行中',
  },
  success: {
    icon: <CheckCircle2 className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" />,
    badge: 'bg-emerald-50 dark:bg-emerald-950/40 text-emerald-700 dark:text-emerald-300 border-emerald-200 dark:border-emerald-800',
    label: '完成',
  },
  error: {
    icon: <AlertCircle className="w-3.5 h-3.5 text-rose-600 dark:text-rose-400" />,
    badge: 'bg-rose-50 dark:bg-rose-950/40 text-rose-700 dark:text-rose-300 border-rose-200 dark:border-rose-900',
    label: '异常',
  },
}

/** running 期间每秒返回已用秒数；不在 running 状态时返回 0。 */
function useElapsedSeconds(startedAt: number | undefined, active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active || !startedAt) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [active, startedAt])
  if (!active || !startedAt) return 0
  return Math.max(0, Math.floor((now - startedAt) / 1000))
}

function summaryFrom(args: string): string {
  try {
    const parsed = JSON.parse(args)
    const firstVal = Object.values(parsed)[0]
    if (typeof firstVal === 'string') {
      return firstVal.length > 60 ? firstVal.slice(0, 60) + '…' : firstVal
    }
  } catch {
    /* noop */
  }
  return ''
}

interface Props {
  invocation: ToolInvocation
  onPreview?: (a: Attachment) => void
}

export function ToolCallCard({ invocation, onPreview }: Props) {
  const { name, arguments: args, status, error, output, attachments, startedAt } = invocation
  const summary = summaryFrom(args)
  const elapsed = useElapsedSeconds(startedAt, status === 'running')
  const slow = status === 'running' && elapsed >= 30
  const statusInfo = STATUS_CONFIG[status]
  const [outputOpen, setOutputOpen] = useState(false)

  return (
    <div className="border border-[#e3e3e3] dark:border-[#3c4043] rounded-2xl bg-[#f0f4f9] dark:bg-[#1e1f20] text-xs overflow-hidden transition-all duration-200 hover:border-[#b4d7fe] dark:hover:border-[#1a73e8]">
      <div className="flex items-center gap-2.5 px-3.5 py-2.5">
        <div className="w-5 h-5 rounded-md bg-white dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] flex items-center justify-center shadow-sm shrink-0">
          <Terminal className="w-3 h-3 text-[#1a73e8] dark:text-[#8ab4f8]" />
        </div>
        <span className="font-mono font-medium text-[#1f1f1f] dark:text-[#f1f3f4] shrink-0">
          {name}
        </span>
        {summary && (
          <span className="text-[#444746] dark:text-[#c4c7c5] truncate font-mono text-[11px] bg-white dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] px-2 py-0.5 rounded-full max-w-sm">
            {summary}
          </span>
        )}
        <div className="ml-auto flex items-center gap-2 shrink-0">
          {status === 'running' && elapsed > 0 && (
            <span
              className={
                'text-[11px] font-mono tabular-nums px-1.5 py-0.5 rounded ' +
                (slow ? 'bg-amber-50 dark:bg-amber-950/40 text-amber-700 dark:text-amber-300 border border-amber-200 dark:border-amber-800 font-semibold' : 'text-[#747775] dark:text-[#9aa0a6]')
              }
              title={slow ? '执行较慢，仍在等待…' : undefined}
            >
              {elapsed}s
            </span>
          )}
          <span className={`inline-flex items-center gap-1 px-2.5 py-0.5 rounded-full border text-[11px] font-medium ${statusInfo.badge}`}>
            {statusInfo.icon}
            <span>{statusInfo.label}</span>
          </span>
        </div>
      </div>
      {attachments && attachments.length > 0 && (
        <div className="border-t border-[#e3e3e3] dark:border-[#3c4043] bg-white/70 dark:bg-[#28292a]/70 px-3.5 py-2 flex flex-wrap gap-2">
          {attachments.map((a) => (
            <button
              key={a.url}
              type="button"
              onClick={() => onPreview?.(a)}
              className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] border border-[#e3e3e3] dark:border-[#3c4043] text-xs shadow-sm hover:border-[#1a73e8] dark:hover:border-[#8ab4f8] hover:bg-[#f0f4f9] dark:hover:bg-[#333537] transition-all"
              title={`预览 ${a.filename}`}
            >
              <FileText className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8]" />
              <span className="truncate max-w-[200px]">{a.filename}</span>
            </button>
          ))}
        </div>
      )}
      {output && (
        <div className="border-t border-[#e3e3e3] dark:border-[#3c4043]">
          <button
            type="button"
            onClick={() => setOutputOpen((o) => !o)}
            className="flex w-full items-center gap-1.5 px-3.5 py-1.5 text-[11px] text-[#747775] dark:text-[#9aa0a6] hover:bg-[#e8f0fe]/50 dark:hover:bg-[#28292a]/60 transition-colors"
          >
            {outputOpen ? (
              <ChevronDown className="w-3.5 h-3.5" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5" />
            )}
            <span className="font-medium">输出</span>
            <span className="ml-auto font-mono tabular-nums">{output.length} 字符</span>
          </button>
          {outputOpen && (
            <pre className="px-3.5 pb-2.5 pt-0.5 text-[11px] font-mono text-[#444746] dark:text-[#c4c7c5] whitespace-pre-wrap break-words leading-relaxed max-h-72 overflow-y-auto scrollbar-thin">
              {output}
            </pre>
          )}
        </div>
      )}
      {status === 'error' && error && (
        <div className="border-t border-rose-100 dark:border-rose-900/60 px-3.5 py-2 text-xs text-rose-700 dark:text-rose-300 bg-rose-50/70 dark:bg-rose-950/40 font-mono break-words leading-relaxed">
          {error}
        </div>
      )}
    </div>
  )
}

