import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, ChevronDown, Cpu, Sparkles } from 'lucide-react'
import type { Model } from '../types'
import { cn } from '../lib/utils'

interface Props {
  models: Model[]
  value: string
  onChange: (modelId: string) => void
  disabled?: boolean
  variant?: 'header' | 'pill'
}

/** 头部或输入框内模型切换下拉框 */
export function ModelPicker({
  models,
  value,
  onChange,
  disabled,
  variant = 'header',
}: Props) {
  const [open, setOpen] = useState(false)
  const [placement, setPlacement] = useState<'top' | 'bottom'>('top')
  const [maxHeight, setMaxHeight] = useState<number>(360)
  const wrapRef = useRef<HTMLDivElement>(null)

  const uniqueModels = useMemo(() => {
    const seen = new Set<string>()
    return models.filter((m) => {
      if (!m.id || seen.has(m.id)) return false
      seen.add(m.id)
      return true
    })
  }, [models])

  // 将模型按厂商智能分组
  const groupedModels = useMemo(() => {
    const minimax: Model[] = []
    const glm: Model[] = []
    const others: Model[] = []

    for (const m of uniqueModels) {
      const lower = m.id.toLowerCase()
      if (lower.startsWith('minimax')) {
        minimax.push(m)
      } else if (lower.startsWith('glm')) {
        glm.push(m)
      } else {
        others.push(m)
      }
    }

    const groups: { label: string; items: Model[] }[] = []
    if (minimax.length > 0) groups.push({ label: 'MiniMax', items: minimax })
    if (glm.length > 0) groups.push({ label: '智谱 GLM (Coding Plan)', items: glm })
    if (others.length > 0) groups.push({ label: '其他模型', items: others })
    return groups
  }, [uniqueModels])

  // 动态检测屏幕视口上下剩余空间，智能决定向上还是向下展开，并限制最大高度防止被顶出屏幕
  useEffect(() => {
    if (!open) return

    const updatePosition = () => {
      if (!wrapRef.current) return
      const rect = wrapRef.current.getBoundingClientRect()
      const spaceAbove = rect.top - 16
      const spaceBelow = window.innerHeight - rect.bottom - 16

      if (variant === 'header') {
        setPlacement('bottom')
        setMaxHeight(Math.max(160, Math.min(spaceBelow, 420)))
      } else {
        // pill 药丸输入框内：
        // 如果上方空间不足 320px 且下方空间更大（如居中欢迎态），则智能向下展开；
        // 否则（如底部对话态），向上展开
        if (spaceAbove < 320 && spaceBelow > spaceAbove) {
          setPlacement('bottom')
          setMaxHeight(Math.max(160, Math.min(spaceBelow, 380)))
        } else {
          setPlacement('top')
          setMaxHeight(Math.max(160, Math.min(spaceAbove, 380)))
        }
      }
    }

    updatePosition()
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [open, variant])

  useEffect(() => {
    if (!open) return
    const onClick = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onClick)
    return () => document.removeEventListener('mousedown', onClick)
  }, [open])

  const current = uniqueModels.find((m) => m.id === value)
  const cleanLabel = current?.name ?? value ?? '模型'

  return (
    <div ref={wrapRef} className="relative">
      {variant === 'pill' ? (
        <button
          type="button"
          onClick={() => !disabled && setOpen((o) => !o)}
          disabled={disabled || models.length === 0}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-[#444746] dark:text-[#c4c7c5] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#e1e7f0] dark:hover:bg-[#28292a] rounded-full transition-colors disabled:opacity-50"
          title="切换模型"
        >
          <Sparkles className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8]" />
          <span className="max-w-[140px] truncate">{cleanLabel}</span>
          <ChevronDown
            className={cn(
              'w-3 h-3 text-[#747775] dark:text-[#9aa0a6] transition-transform duration-150',
              open && 'rotate-180',
            )}
          />
        </button>
      ) : (
        <button
          type="button"
          onClick={() => !disabled && setOpen((o) => !o)}
          disabled={disabled || models.length === 0}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-[#444746] dark:text-[#c4c7c5] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] rounded-full transition-colors disabled:opacity-50"
          title={disabled ? '请等待当前回答完成' : '切换模型'}
        >
          <Cpu className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8]" />
          <span className="max-w-[180px] truncate text-[#1f1f1f] dark:text-[#f1f3f4]">{cleanLabel}</span>
          <ChevronDown
            className={cn(
              'w-3 h-3 text-[#747775] dark:text-[#9aa0a6] transition-transform duration-150',
              open && 'rotate-180',
            )}
          />
        </button>
      )}

      {open && (
        <div
          style={{ maxHeight: `${maxHeight}px` }}
          className={cn(
            'absolute right-0 min-w-[240px] overflow-y-auto scrollbar-thin bg-white dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] rounded-2xl shadow-xl z-50 p-1.5',
            placement === 'top'
              ? 'bottom-full mb-2 animate-slide-up origin-bottom-right'
              : 'top-full mt-2 animate-slide-down origin-top-right',
          )}
        >
          {groupedModels.length === 0 ? (
            <div className="px-3 py-3 text-xs text-[#747775] dark:text-[#9aa0a6] text-center">
              暂无可用模型
            </div>
          ) : (
            groupedModels.map((group, gIdx) => (
              <div key={group.label} className={cn(gIdx > 0 && 'mt-1.5 pt-1.5 border-t border-[#f0f0f0] dark:border-[#28292a]')}>
                <div className="px-2.5 py-1 text-[10px] font-semibold text-[#747775] dark:text-[#9aa0a6] tracking-wider uppercase select-none">
                  {group.label}
                </div>
                {group.items.map((m) => (
                  <button
                    key={m.id}
                    type="button"
                    onClick={() => {
                      if (m.id !== value) onChange(m.id)
                      setOpen(false)
                    }}
                    className={cn(
                      'w-full flex items-center justify-between gap-2 px-2.5 py-2 text-xs text-left rounded-xl transition-colors',
                      m.id === value
                        ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-medium'
                        : 'text-[#1f1f1f] dark:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a]',
                    )}
                  >
                    <div className="flex items-center gap-1.5 min-w-0">
                      <span className="truncate">{m.name}</span>
                      {m.id !== m.name && (
                        <span className="text-[10px] text-[#747775] dark:text-[#9aa0a6] font-mono truncate">
                          ({m.id})
                        </span>
                      )}
                    </div>
                    {m.id === value && (
                      <Check className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8] shrink-0" />
                    )}
                  </button>
                ))}
              </div>
            ))
          )}
        </div>
      )}
    </div>
  )
}
