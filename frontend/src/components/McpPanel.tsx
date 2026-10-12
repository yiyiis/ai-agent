import { useEffect, useState } from 'react'
import {
  ChevronDown,
  ChevronRight,
  Loader2,
  Plug,
  X,
} from 'lucide-react'
import { api } from '../api/client'
import type { McpServer, McpToolInfo } from '../types'
import { cn } from '../lib/utils'

interface Props {
  open: boolean
  onClose: () => void
  /** 会话当前启用的 MCP Server ID 集合 */
  enabledIds: number[]
  onEnabledChange: (ids: number[]) => void
}

/**
 * 会话 MCP 抽屉（阶段八）：广场 Server 的会话级启用开关。
 * 状态变更只上抛给父层：已有会话由父层立即 PUT 持久化；新对话草稿态本地暂存。
 */
export function McpPanel({ open, onClose, enabledIds, onEnabledChange }: Props) {
  const [mcps, setMcps] = useState<McpServer[]>([])
  const [error, setError] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<number | null>(null)
  // 懒加载的工具清单缓存：server_id → tools[]（null 表示加载中）
  const [toolsCache, setToolsCache] = useState<Record<number, McpToolInfo[] | undefined>>({})

  useEffect(() => {
    if (open) {
      setError(null)
      api
        .listMcps()
        .then(setMcps)
        .catch((e) => setError(String(e)))
    }
  }, [open])

  const usable = mcps.filter((m) => m.enabled)

  const toggle = (id: number) => {
    onEnabledChange(
      enabledIds.includes(id) ? enabledIds.filter((x) => x !== id) : [...enabledIds, id],
    )
  }

  /** 展开时懒拉该 server 的工具清单 */
  const ensureTools = async (id: number) => {
    if (toolsCache[id] !== undefined) return
    setToolsCache((c) => ({ ...c, [id]: undefined }))
    try {
      const tools = await api.getMcpTools(id)
      setToolsCache((c) => ({ ...c, [id]: tools }))
    } catch (e) {
      setError(String(e))
      setToolsCache((c) => {
        const next = { ...c }
        delete next[id]
        return next
      })
    }
  }

  if (!open) return null

  return (
    <>
      <div className="fixed inset-0 bg-black/20 z-30" onClick={onClose} />
      <aside className="fixed top-0 right-0 h-full w-96 bg-white dark:bg-[#1e1f20] border-l border-neutral-200 dark:border-[#3c4043] z-40 flex flex-col shadow-xl text-[#1f1f1f] dark:text-[#f1f3f4]">
        <header className="flex items-center justify-between px-4 py-3 border-b border-neutral-200 dark:border-[#3c4043]">
          <div className="flex items-center gap-2">
            <span className="font-medium">MCP 工具服务</span>
            <span className="text-xs text-neutral-500 dark:text-[#9aa0a6]">
              （已启用 {enabledIds.length}）
            </span>
          </div>
          <button
            className="p-1 hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded text-neutral-500 dark:text-[#9aa0a6]"
            onClick={onClose}
          >
            <X className="w-4 h-4" />
          </button>
        </header>

        <div className="px-4 py-2.5 bg-neutral-50/70 dark:bg-[#202122] border-b border-neutral-200 dark:border-[#3c4043] flex items-start gap-2">
          <Plug className="w-3.5 h-3.5 text-neutral-400 dark:text-[#9aa0a6] mt-0.5 shrink-0" />
          <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] leading-relaxed">
            启用后该服务的全部工具会在本轮对话生效，模型以
            <span className="font-mono text-neutral-700 dark:text-[#e3e3e3]"> mcp_&lt;server&gt;_&lt;tool&gt; </span>
            名调用。工具在远端执行，不经会话沙箱。
          </p>
        </div>

        {error && (
          <div className="mx-4 mt-3 px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
            {error}
          </div>
        )}

        <div className="flex-1 overflow-y-auto scrollbar-thin px-3 py-2">
          {usable.length === 0 && (
            <div className="text-center text-neutral-400 dark:text-[#747775] text-sm py-12">
              广场还没有可用的 MCP Server，先去广场添加
            </div>
          )}
          {usable.map((m) => {
            const on = enabledIds.includes(m.id)
            const tools = toolsCache[m.id]
            return (
              <div
                key={m.id}
                className={cn(
                  'group px-3 py-2.5 rounded-xl border mb-2 flex flex-col gap-2 transition-colors',
                  on
                    ? 'border-emerald-500/60 bg-emerald-50/20 dark:border-emerald-600/40 dark:bg-emerald-950/20'
                    : 'border-neutral-200 dark:border-[#3c4043] hover:bg-neutral-50/60 dark:hover:bg-[#28292a]',
                )}
              >
                <div className="flex items-center justify-between gap-2">
                  <button
                    type="button"
                    className="flex items-center gap-1.5 min-w-0 flex-1 text-left"
                    onClick={() => {
                      const next = expanded === m.id ? null : m.id
                      setExpanded(next)
                      if (next !== null) ensureTools(m.id)
                    }}
                    title="展开工具清单"
                  >
                    {expanded === m.id ? (
                      <ChevronDown className="w-3.5 h-3.5 text-neutral-400 shrink-0" />
                    ) : (
                      <ChevronRight className="w-3.5 h-3.5 text-neutral-400 shrink-0" />
                    )}
                    <span className="text-sm font-medium truncate text-[#1f1f1f] dark:text-[#f1f3f4]">
                      {m.name}
                    </span>
                    {m.system && (
                      <span className="shrink-0 text-[10px] px-1.5 py-px rounded bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff]">
                        官方
                      </span>
                    )}
                    <span className="shrink-0 text-[10px] text-neutral-400 dark:text-[#747775]">
                      {m.tool_count ? `${m.tool_count} 工具` : ''}
                    </span>
                  </button>

                  {/* 启用开关 */}
                  <button
                    type="button"
                    role="switch"
                    aria-checked={on}
                    onClick={() => toggle(m.id)}
                    className={cn(
                      'relative w-9 h-5 rounded-full transition-colors shrink-0',
                      on ? 'bg-emerald-500' : 'bg-neutral-200 dark:bg-[#3c4043]',
                    )}
                    title={on ? '点击停用' : '点击启用'}
                  >
                    <span
                      className={cn(
                        'absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white shadow transition-transform',
                        on && 'translate-x-4',
                      )}
                    />
                  </button>
                </div>

                {m.description && (
                  <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] line-clamp-2">
                    {m.description}
                  </p>
                )}

                {expanded === m.id && (
                  <div className="pt-1.5 border-t border-neutral-100 dark:border-[#3c4043]/60">
                    {tools === undefined ? (
                      <div className="flex items-center gap-1.5 text-xs text-neutral-400 py-1.5">
                        <Loader2 className="w-3 h-3 animate-spin" />
                        正在连接并获取工具清单…
                      </div>
                    ) : tools.length === 0 ? (
                      <p className="text-xs text-neutral-400 dark:text-[#747775] py-1.5">
                        未发现任何工具（可能未测试过或被白名单过滤）
                      </p>
                    ) : (
                      <div className="space-y-1.5 py-1">
                        {tools.map((t) => (
                          <div key={t.qualified}>
                            <p className="text-[11px] font-medium text-[#1a73e8] dark:text-[#8ab4f8] font-mono break-all">
                              {t.qualified}
                            </p>
                            {t.description && (
                              <p className="text-[11px] text-neutral-500 dark:text-[#9aa0a6] line-clamp-2">
                                {t.description}
                              </p>
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </aside>
    </>
  )
}
