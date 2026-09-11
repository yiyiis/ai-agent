import { useEffect, useState } from 'react'
import { Bot, Pin, PinOff, Plus, RotateCcw, Trash2, User, Wand2 } from 'lucide-react'
import { api } from '../api/client'
import type { Memory } from '../types'
import { cn } from '../lib/utils'

/**
 * 记忆管理。
 *
 * **这个界面是自动抽取的硬前置。** 跨会话记忆会被无条件注入每一轮 system prompt，
 * 而其中一部分是模型自己判断记下来的——用户必须能看到全部内容并随时删掉，
 * 否则一句被误记的话会永久影响之后所有会话。
 *
 * 列表顺序刻意与注入顺序一致（后端 list_active 的排序），这样"为什么这条没生效"
 * 一眼就能看出来：排在预算之外的就不会被注入。
 */

const SOURCE_META: Record<Memory['source'], { label: string; icon: typeof Bot; cls: string }> = {
  explicit: { label: '对话中记录', icon: Bot, cls: 'bg-blue-50 text-blue-700 dark:bg-blue-950/40 dark:text-blue-300' },
  extracted: { label: '自动抽取', icon: Wand2, cls: 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300' },
  manual: { label: '手工添加', icon: User, cls: 'bg-neutral-100 text-neutral-600 dark:bg-[#28292a] dark:text-[#c4c7c5]' },
}

export function MemoriesPanel() {
  const [rows, setRows] = useState<Memory[]>([])
  const [showDeleted, setShowDeleted] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // 管理接口不受 MEMORY_ENABLED 约束（那样才能在正式打开前先准备好记忆），
  // 所以"加了却不生效"是一个真实存在的状态，必须显式告诉用户
  const [status, setStatus] = useState<{ enabled: boolean; reasons: string[] } | null>(null)
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState({ topic: '', content: '', scope: 'preference' })

  const load = async () => {
    try {
      const [list, st] = await Promise.all([
        api.listMemories(showDeleted),
        api.memoryStatus(),
      ])
      setRows(list)
      setStatus(st)
      setError(null)
    } catch (e) {
      setError(String(e))
    }
  }

  useEffect(() => {
    void load()
     
  }, [showDeleted])

  const submit = async () => {
    if (!draft.content.trim()) return
    try {
      await api.createMemory({ ...draft, content: draft.content.trim() })
      setDraft({ topic: '', content: '', scope: 'preference' })
      setAdding(false)
      await load()
    } catch (e) {
      // 后端的拒绝理由（超长 / 疑似凭据）是给人看的，直接展示
      setError(String(e).replace(/^Error:\s*/, ''))
    }
  }

  return (
    <div className="flex-1 overflow-y-auto bg-slate-50/40 dark:bg-[#131314]">
      <div className="max-w-3xl mx-auto px-6 py-8">
        <div className="flex items-start justify-between mb-1">
          <div>
            <h1 className="text-xl font-bold text-slate-900 dark:text-[#f1f3f4] tracking-tight">长期记忆库</h1>
            <p className="text-xs text-slate-500 dark:text-[#9aa0a6] mt-1">
              这些记忆条目会作为系统偏好注入每一轮对话开头，可在需要时随时修改或撤销。
            </p>
          </div>
          <button
            onClick={() => setAdding((v) => !v)}
            className="flex items-center gap-1.5 px-4 py-2 bg-[#1a73e8] hover:bg-[#1557b0] text-white text-xs font-medium rounded-full shadow-sm hover:shadow transition-all"
          >
            <Plus className="w-4 h-4" />
            添加记忆
          </button>
        </div>

        <div className="h-4" />

        {status && !status.enabled && (
          <div className="mb-4 px-4 py-3 bg-amber-50/90 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-900 text-amber-800 dark:text-amber-300 text-xs rounded-2xl shadow-card">
            <div className="font-semibold mb-0.5">长期记忆当前未启用</div>
            <div className="text-[11px] text-amber-700 dark:text-amber-400 leading-relaxed">
              下面的内容暂时不会被带进对话。原因：{status.reasons.join('；') || '未知'}。
              这里仍然可以先添加和整理，开启后立即生效。
            </div>
          </div>
        )}

        {error && (
          <div className="mb-4 px-4 py-2.5 bg-rose-50 dark:bg-rose-950/30 border border-rose-200 dark:border-rose-900 text-rose-700 dark:text-rose-400 text-xs rounded-2xl shadow-card">
            {error}
          </div>
        )}

        {adding && (
          <div className="mb-4 p-4 border border-[#e3e3e3] dark:border-[#3c4043] rounded-2xl bg-white dark:bg-[#1e1f20] shadow-sm space-y-3 animate-slide-up">
            <div className="flex gap-2.5">
              <input
                value={draft.topic}
                onChange={(e) => setDraft({ ...draft, topic: e.target.value })}
                placeholder="主题，如「报告格式」"
                className="w-48 px-3 py-1.5 text-xs border border-[#e3e3e3] dark:border-[#3c4043] rounded-xl outline-none focus:border-[#1a73e8] dark:focus:border-[#8ab4f8] focus:ring-2 focus:ring-[#d3e3fd] dark:focus:ring-[#004a77] bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4]"
              />
              <select
                value={draft.scope}
                onChange={(e) => setDraft({ ...draft, scope: e.target.value })}
                className="px-3 py-1.5 text-xs border border-[#e3e3e3] dark:border-[#3c4043] rounded-xl bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] outline-none focus:border-[#1a73e8] dark:focus:border-[#8ab4f8]"
              >
                <option value="preference">偏好</option>
                <option value="context">业务背景</option>
                <option value="reference">资源位置</option>
              </select>
            </div>
            <textarea
              value={draft.content}
              onChange={(e) => setDraft({ ...draft, content: e.target.value })}
              placeholder="一句话描述，最多 200 字符"
              maxLength={200}
              rows={2}
              className="w-full px-3 py-2 text-xs border border-[#e3e3e3] dark:border-[#3c4043] rounded-xl resize-none outline-none focus:border-[#1a73e8] dark:focus:border-[#8ab4f8] focus:ring-2 focus:ring-[#d3e3fd] dark:focus:ring-[#004a77] placeholder:text-slate-400 dark:placeholder:text-[#9aa0a6] bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4]"
            />
            <div className="flex items-center justify-between">
              <span className="text-[11px] text-slate-400 dark:text-[#9aa0a6]">
                {draft.content.length}/200
              </span>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setAdding(false)}
                  className="px-3 py-1.5 text-xs text-slate-500 dark:text-[#9aa0a6] hover:text-slate-700 dark:hover:text-[#f1f3f4] rounded-xl"
                >
                  取消
                </button>
                <button
                  onClick={submit}
                  className="px-3.5 py-1.5 bg-[#1a73e8] hover:bg-[#1557b0] text-white text-xs font-medium rounded-full shadow-sm transition-colors"
                >
                  保存记忆
                </button>
              </div>
            </div>
          </div>
        )}


        <label className="flex items-center gap-2 mb-3 text-sm text-neutral-500 dark:text-[#9aa0a6]">
          <input
            type="checkbox"
            checked={showDeleted}
            onChange={(e) => setShowDeleted(e.target.checked)}
          />
          显示已删除的（删除是可恢复的）
        </label>

        {rows.length === 0 ? (
          <div className="text-center py-16 text-neutral-400 dark:text-[#747775] text-sm">
            还没有任何记忆。当你说「以后都……」这类长期约定时，模型会记下来。
          </div>
        ) : (
          <div className="space-y-2">
            {rows.map((m) => (
              <MemoryRow key={m.id} memory={m} onChanged={load} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function MemoryRow({ memory, onChanged }: { memory: Memory; onChanged: () => void }) {
  const meta = SOURCE_META[memory.source] ?? SOURCE_META.manual
  const Icon = meta.icon
  const deleted = memory.deleted_at !== null

  return (
    <div
      className={cn(
        'group flex items-start gap-3 px-3 py-2.5 border rounded-lg bg-white dark:bg-[#1e1f20]',
        deleted ? 'border-neutral-100 dark:border-[#28292a] opacity-50' : 'border-neutral-200 dark:border-[#3c4043]',
      )}
    >
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 mb-1">
          {memory.topic && (
            <span className="text-xs px-1.5 py-0.5 bg-neutral-100 dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5] rounded">
              {memory.topic}
            </span>
          )}
          <span
            className={cn('text-xs px-1.5 py-0.5 rounded flex items-center gap-1', meta.cls)}
          >
            <Icon className="w-3 h-3" />
            {meta.label}
          </span>
          {memory.pinned && (
            <span className="text-xs px-1.5 py-0.5 bg-neutral-900 dark:bg-[#e3e3e3] text-white dark:text-[#131314] rounded flex items-center gap-1">
              <Pin className="w-3 h-3" />
              置顶
            </span>
          )}
          {memory.hit_count > 0 && (
            <span className="text-xs text-neutral-400 dark:text-[#9aa0a6]">用过 {memory.hit_count} 次</span>
          )}
        </div>
        <div className={cn('text-sm text-[#1f1f1f] dark:text-[#f1f3f4]', deleted && 'line-through')}>{memory.content}</div>
      </div>
      <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition">
        {deleted ? (
          <button
            title="恢复"
            onClick={async () => {
              await api.restoreMemory(memory.id)
              onChanged()
            }}
            className="p-1.5 text-neutral-400 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] rounded"
          >
            <RotateCcw className="w-4 h-4" />
          </button>
        ) : (
          <>
            <button
              title={memory.pinned ? '取消置顶' : '置顶（优先注入）'}
              onClick={async () => {
                await api.updateMemory(memory.id, { pinned: !memory.pinned })
                onChanged()
              }}
              className={cn(
                'p-1.5 rounded hover:text-neutral-900 dark:hover:text-[#f1f3f4]',
                memory.pinned ? 'text-neutral-900 dark:text-[#f1f3f4] opacity-100' : 'text-neutral-400 dark:text-[#9aa0a6]',
              )}
            >
              {memory.pinned ? <Pin className="w-4 h-4" /> : <PinOff className="w-4 h-4" />}
            </button>
            <button
              title="删除"
              onClick={async () => {
                await api.deleteMemory(memory.id)
                onChanged()
              }}
              className="p-1.5 text-neutral-400 dark:text-[#9aa0a6] hover:text-red-600 dark:hover:text-red-400 rounded"
            >
              <Trash2 className="w-4 h-4" />
            </button>
          </>
        )}
      </div>
    </div>
  )
}
