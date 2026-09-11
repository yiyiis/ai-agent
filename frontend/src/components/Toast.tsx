import { useEffect } from 'react'
import { CheckCircle2, Info, X, XCircle } from 'lucide-react'
import { cn } from '../lib/utils'

export type ToastVariant = 'success' | 'info' | 'error'

export interface ToastSpec {
  /** 短标题，会用大字号显示 */
  title: string
  /** 选填的副标题，多 1 行说明用 */
  description?: string
  variant?: ToastVariant
  /** 毫秒；不传或 0 = 不自动消失 */
  durationMs?: number
}

interface Props extends ToastSpec {
  onClose: () => void
}

/**
 * 顶部居中固定的轻量 toast。
 */
export function Toast({
  title,
  description,
  variant = 'info',
  durationMs = 4000,
  onClose,
}: Props) {
  useEffect(() => {
    if (!durationMs) return
    const t = setTimeout(onClose, durationMs)
    return () => clearTimeout(t)
  }, [durationMs, onClose])

  const styles = {
    success: {
      border: 'border-emerald-200/80 dark:border-emerald-800/60 bg-emerald-50/30 dark:bg-emerald-950/40',
      icon: <CheckCircle2 className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />,
    },
    error: {
      border: 'border-rose-200/80 dark:border-rose-800/60 bg-rose-50/30 dark:bg-rose-950/40',
      icon: <XCircle className="w-4 h-4 text-rose-600 dark:text-rose-400" />,
    },
    info: {
      border: 'border-[#b4d7fe] dark:border-[#3c4043] bg-[#d3e3fd]/30 dark:bg-[#28292a]/80',
      icon: <Info className="w-4 h-4 text-[#1a73e8] dark:text-[#8ab4f8]" />,
    },
  }[variant]

  return (
    <div className="fixed top-5 left-1/2 -translate-x-1/2 z-[80] pointer-events-none animate-slide-up">
      <div
        className={cn(
          'pointer-events-auto bg-white/95 dark:bg-[#1e1f20]/95 backdrop-blur-xl shadow-float rounded-2xl px-4 py-3 max-w-md flex items-start gap-3 border transition-all',
          styles.border,
        )}
      >
        <div className="mt-0.5 shrink-0">{styles.icon}</div>
        <div className="flex-1 min-w-0">
          <div className="text-xs font-semibold text-slate-800 dark:text-[#f1f3f4]">{title}</div>
          {description && (
            <div className="text-[11px] text-slate-600 dark:text-[#c4c7c5] mt-0.5 leading-relaxed">
              {description}
            </div>
          )}
        </div>
        <button
          onClick={onClose}
          className="text-slate-400 dark:text-[#9aa0a6] hover:text-slate-700 dark:hover:text-[#f1f3f4] p-0.5 rounded-lg hover:bg-slate-100 dark:hover:bg-[#28292a] shrink-0 transition-colors"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  )
}

