import { useEffect, useRef, useState } from 'react'
import {
  Download,
  Eye,
  GitBranch,
  Lock,
  Sparkles,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import { api } from '../api/client'
import type { EnabledSkillEntry, Skill, SkillVersion } from '../types'
import { cn } from '../lib/utils'
import { SkillDetailModal } from './SkillDetailModal'

interface Props {
  open: boolean
  onClose: () => void
  /** 已启用的 skill + 各自锁定的版本（null = 跟随当前） */
  enabledEntries: EnabledSkillEntry[]
  onEnabledChange: (entries: EnabledSkillEntry[]) => void
}

export function SkillsPanel({
  open,
  onClose,
  enabledEntries,
  onEnabledChange,
}: Props) {
  const [skills, setSkills] = useState<Skill[]>([])
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [viewing, setViewing] = useState<Skill | null>(null)
  // 懒加载的版本列表缓存：skill_id → versions[]
  const [versionCache, setVersionCache] = useState<Record<number, SkillVersion[]>>(
    {},
  )
  const fileRef = useRef<HTMLInputElement>(null)

  const refresh = async () => {
    setSkills(await api.listSkills())
  }

  useEffect(() => {
    if (open) {
      setError(null)
      refresh()
    }
  }, [open])

  const entryOf = (id: number) =>
    enabledEntries.find((e) => e.skill_id === id)
  const modeOf = (id: number): 'disabled' | 'auto' | 'full' => {
    const entry = entryOf(id)
    if (!entry) return 'disabled'
    if (entry.source === 'auto') return 'auto'
    return 'full'
  }
  const pinOf = (id: number): number | null =>
    entryOf(id)?.pinned_version_number ?? null
  const isAutoLoaded = (id: number) =>
    entryOf(id)?.source === 'auto_loaded'

  const fullCount = enabledEntries.filter((e) => e.source !== 'auto').length
  const autoCount = enabledEntries.filter((e) => e.source === 'auto').length

  // 状态变更只上抛给父层：已有会话由父层立即 PUT 持久化；新对话草稿态由父层本地暂存
  const commit = (next: EnabledSkillEntry[]) => {
    onEnabledChange(next)
  }

  const setSkillMode = (id: number, mode: 'disabled' | 'auto' | 'full') => {
    if (mode === 'disabled') {
      commit(enabledEntries.filter((e) => e.skill_id !== id))
      return
    }
    const existing = entryOf(id)
    const next: EnabledSkillEntry[] = existing
      ? enabledEntries.map((e) =>
          e.skill_id === id ? { ...e, source: mode } : e,
        )
      : [
          ...enabledEntries,
          { skill_id: id, pinned_version_number: null, source: mode },
        ]
    commit(next)
  }

  const setAllMode = (mode: 'disabled' | 'auto' | 'full') => {
    if (mode === 'disabled') {
      commit([])
      return
    }
    const next: EnabledSkillEntry[] = skills.map((s) => {
      const existing = entryOf(s.id)
      return {
        skill_id: s.id,
        pinned_version_number: existing?.pinned_version_number ?? null,
        source: mode,
      }
    })
    commit(next)
  }

  const setPin = async (id: number, pinned: number | null) => {
    const next = enabledEntries.map((e) =>
      e.skill_id === id ? { ...e, pinned_version_number: pinned } : e,
    )
    commit(next)
  }

  /** 懒加载某个 skill 的版本列表——下拉首次展开时调一次 */
  const ensureVersions = async (skillId: number) => {
    if (versionCache[skillId]) return
    try {
      const vs = await api.listSkillVersions(skillId)
      setVersionCache((c) => ({ ...c, [skillId]: vs }))
    } catch (e) {
      setError(String(e))
    }
  }

  const onUpload = async (file: File) => {
    setError(null)
    setUploading(true)
    try {
      const result = await api.uploadSkill(file)
      await refresh()
      // 上传成功后开版本缓存失效，让下拉重新拉
      setVersionCache((c) => {
        const next = { ...c }
        delete next[result.skill.id]
        return next
      })
    } catch (e) {
      setError(String(e))
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  const remove = async (skill: Skill) => {
    if (!confirm(`删除 skill "${skill.name}"？`)) return
    try {
      await api.deleteSkill(skill.id)
      onEnabledChange(enabledEntries.filter((e) => e.skill_id !== skill.id))
      await refresh()
    } catch (e) {
      setError(String(e))
    }
  }

  const download = async (skill: Skill) => {
    setError(null)
    try {
      await api.downloadSkill(skill.id, skill.name)
    } catch (e) {
      setError(String(e))
    }
  }

  if (!open) return null

  return (
    <>
      <div className="fixed inset-0 bg-black/20 z-30" onClick={onClose} />
      <aside className="fixed top-0 right-0 h-full w-96 bg-white dark:bg-[#1e1f20] border-l border-neutral-200 dark:border-[#3c4043] z-40 flex flex-col shadow-xl text-[#1f1f1f] dark:text-[#f1f3f4]">
        <header className="flex items-center justify-between px-4 py-3 border-b border-neutral-200 dark:border-[#3c4043]">
          <div className="flex items-center gap-2">
            <span className="font-medium">Skills</span>
            <span className="text-xs text-neutral-500 dark:text-[#9aa0a6]">
              ({fullCount} 常驻 · {autoCount} 按需)
            </span>
          </div>
          <button
            className="p-1 hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded text-neutral-500 dark:text-[#9aa0a6]"
            onClick={onClose}
          >
            <X className="w-4 h-4" />
          </button>
        </header>

        {/* 模式说明注释条 */}
        <div className="px-4 py-2.5 bg-neutral-50/70 dark:bg-[#202122] border-b border-neutral-200 dark:border-[#3c4043] flex items-start gap-2">
          <Sparkles className="w-3.5 h-3.5 text-neutral-400 dark:text-[#9aa0a6] mt-0.5 shrink-0" />
          <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] leading-relaxed">
            设为<span className="text-neutral-700 dark:text-[#e3e3e3] font-medium">「按需」</span>的技能将在提示词中注入摘要，由模型自主决策装载；设为<span className="text-neutral-700 dark:text-[#e3e3e3] font-medium">「常驻」</span>的技能每轮始终预载。
          </p>
        </div>

        {/* 批量操作工具栏 */}
        <div className="px-4 py-2 border-b border-neutral-200 dark:border-[#3c4043] flex items-center justify-between gap-2 bg-neutral-50/70 dark:bg-[#252728]">
          <span className="text-xs text-neutral-500 dark:text-[#9aa0a6] shrink-0 font-medium">批量操作</span>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => setAllMode('auto')}
              className="px-2 py-0.5 text-xs rounded border border-neutral-200 dark:border-[#3c4043] hover:bg-white dark:hover:bg-[#333537] text-neutral-700 dark:text-[#f1f3f4] transition"
              title="将全部技能设为智能按需"
            >
              全部按需
            </button>
            <button
              type="button"
              onClick={() => setAllMode('full')}
              className="px-2 py-0.5 text-xs rounded border border-neutral-200 dark:border-[#3c4043] hover:bg-white dark:hover:bg-[#333537] text-neutral-700 dark:text-[#f1f3f4] transition"
              title="将全部技能设为常驻全量"
            >
              全部常驻
            </button>
            <button
              type="button"
              onClick={() => setAllMode('disabled')}
              className="px-2 py-0.5 text-xs rounded border border-neutral-200 dark:border-[#3c4043] hover:bg-white dark:hover:bg-[#333537] text-neutral-500 dark:text-[#9aa0a6] hover:text-red-600 dark:hover:text-red-400 transition"
              title="关闭全部技能"
            >
              全部关闭
            </button>
          </div>
        </div>

        <div className="px-4 py-3 border-b border-neutral-200 dark:border-[#3c4043]">
          <input
            ref={fileRef}
            type="file"
            accept=".zip"
            className="hidden"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) onUpload(f)
            }}
          />
          <button
            disabled={uploading}
            onClick={() => fileRef.current?.click()}
            className="w-full flex items-center justify-center gap-2 px-3 py-2 border border-dashed border-neutral-300 dark:border-[#3c4043] rounded-lg text-sm text-neutral-600 dark:text-[#c4c7c5] hover:bg-neutral-50 dark:hover:bg-[#28292a] disabled:opacity-50"
          >
            <Upload className="w-4 h-4" />
            {uploading ? '上传中…' : '上传 .zip 包'}
          </button>
          <p className="text-xs text-neutral-400 dark:text-[#747775] mt-2">
            zip 内必须包含 SKILL.md；若含 secrets.env 或 .env 会被自动收纳到机密区
          </p>
        </div>

        {error && (
          <div className="mx-4 mt-3 px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
            {error}
          </div>
        )}

        <div className="flex-1 overflow-y-auto scrollbar-thin px-3 py-2">
          {skills.length === 0 && (
            <div className="text-center text-neutral-400 dark:text-[#747775] text-sm py-12">
              还没有 skill，先上传一个
            </div>
          )}
          {skills.map((s) => (
            <SkillRow
              key={s.id}
              skill={s}
              mode={modeOf(s.id)}
              autoLoaded={isAutoLoaded(s.id)}
              pinned={pinOf(s.id)}
              versions={versionCache[s.id]}
              onChangeMode={(m) => setSkillMode(s.id, m)}
              onSetPin={(v) => setPin(s.id, v)}
              onLoadVersions={() => ensureVersions(s.id)}
              onView={() => setViewing(s)}
              onDownload={() => download(s)}
              onDelete={() => remove(s)}
            />
          ))}
        </div>
      </aside>
      {viewing && (
        <SkillDetailModal
          skill={viewing}
          onClose={() => {
            setViewing(null)
            // 详情弹窗关闭时刷新——可能发布了新版本，下拉应能看到
            refresh()
            setVersionCache((c) => {
              if (!viewing) return c
              const next = { ...c }
              delete next[viewing.id]
              return next
            })
          }}
        />
      )}
    </>
  )
}

function SkillRow({
  skill,
  mode,
  autoLoaded,
  pinned,
  versions,
  onChangeMode,
  onSetPin,
  onLoadVersions,
  onView,
  onDownload,
  onDelete,
}: {
  skill: Skill
  mode: 'disabled' | 'auto' | 'full'
  autoLoaded: boolean
  pinned: number | null
  versions: SkillVersion[] | undefined
  onChangeMode: (mode: 'disabled' | 'auto' | 'full') => void
  onSetPin: (v: number | null) => void
  onLoadVersions: () => void
  onView: () => void
  onDownload: () => void
  onDelete: () => void
}) {
  return (
    <div
      className={cn(
        'group px-3 py-2.5 rounded-xl border mb-2 flex flex-col gap-2 transition-colors',
        mode === 'full'
          ? 'border-emerald-500/60 bg-emerald-50/20 dark:border-emerald-600/40 dark:bg-emerald-950/20'
          : mode === 'auto'
            ? 'border-blue-400/60 bg-blue-50/20 dark:border-blue-600/40 dark:bg-blue-950/20'
            : 'border-neutral-200 dark:border-[#3c4043] hover:bg-neutral-50/60 dark:hover:bg-[#28292a]',
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 min-w-0 flex-1">
          <span className="text-sm font-medium truncate text-[#1f1f1f] dark:text-[#f1f3f4]">
            {skill.name}
          </span>
          {autoLoaded && (
            <span
              className="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-px rounded bg-blue-100 dark:bg-blue-950/40 text-blue-700 dark:text-blue-300 shrink-0"
              title="模型在对话中按需自主加载"
            >
              <Sparkles className="w-2.5 h-2.5" />
              已动态加载
            </span>
          )}
          {skill.confidential && (
            <span
              className="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-px rounded bg-amber-100 dark:bg-amber-950/40 text-amber-700 dark:text-amber-300 shrink-0"
              title={
                skill.can_view === false
                  ? '机密：仅所有者可预览/下载，你可以直接启用使用'
                  : '机密：仅你可预览/下载'
              }
            >
              <Lock className="w-2.5 h-2.5" />
              机密
            </span>
          )}
        </div>

        {/* 方案 B: 三态分段开关 [ 关 | 按需 | 常驻 ] */}
        <div className="inline-flex items-center rounded-lg bg-neutral-100 dark:bg-[#28292a] p-0.5 text-[11px] border border-neutral-200 dark:border-[#3c4043] shrink-0 select-none">
          <button
            type="button"
            onClick={() => onChangeMode('disabled')}
            className={cn(
              'px-2 py-0.5 rounded-md transition-all font-medium',
              mode === 'disabled'
                ? 'bg-white dark:bg-[#3c4043] text-neutral-800 dark:text-neutral-200 shadow-xs'
                : 'text-neutral-400 dark:text-[#747775] hover:text-neutral-700 dark:hover:text-neutral-300',
            )}
            title="关闭：不加载此技能"
          >
            关
          </button>
          <button
            type="button"
            onClick={() => onChangeMode('auto')}
            className={cn(
              'px-2 py-0.5 rounded-md transition-all font-medium',
              mode === 'auto'
                ? 'bg-[#1a73e8] dark:bg-[#8ab4f8] text-white dark:text-[#041e49] shadow-xs'
                : 'text-neutral-400 dark:text-[#747775] hover:text-neutral-700 dark:hover:text-neutral-300',
            )}
            title="智能按需：注入摘要到提示词，沙箱不预挂载，模型按需自主调用"
          >
            按需
          </button>
          <button
            type="button"
            onClick={() => onChangeMode('full')}
            className={cn(
              'px-2 py-0.5 rounded-md transition-all font-medium',
              mode === 'full'
                ? 'bg-emerald-600 dark:bg-emerald-500 text-white shadow-xs'
                : 'text-neutral-400 dark:text-[#747775] hover:text-neutral-700 dark:hover:text-neutral-300',
            )}
            title="常驻全量：对话前常驻挂载沙箱与机密，提示词全量声明"
          >
            常驻
          </button>
        </div>
      </div>

      <div className="flex items-start justify-between gap-2">
        {skill.description ? (
          <div className="text-xs text-neutral-500 dark:text-[#9aa0a6] line-clamp-2 flex-1">
            {skill.description}
          </div>
        ) : (
          <div className="text-xs text-neutral-400 dark:text-[#747775] italic flex-1">
            暂无描述
          </div>
        )}
        <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition shrink-0 ml-1">
          {skill.can_view !== false && (
            <>
              <button
                type="button"
                className="p-1 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#333537] rounded"
                onClick={onView}
                title="查看详情 / 编辑机密"
              >
                <Eye className="w-3.5 h-3.5" />
              </button>
              <button
                type="button"
                className="p-1 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#333537] rounded"
                onClick={onDownload}
                title="下载 skill 包（不含机密）"
              >
                <Download className="w-3.5 h-3.5" />
              </button>
            </>
          )}
          {(skill.is_owner || !skill.confidential) && (
            <button
              type="button"
              className="p-1 text-red-500 hover:bg-red-50 dark:hover:bg-red-950/40 rounded"
              onClick={onDelete}
              title="删除此技能"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
        </div>
      </div>

      {mode !== 'disabled' && (
        <VersionPicker
          skillCurrentVersion={skill.current_version_number ?? null}
          pinned={pinned}
          versions={versions}
          onLoadVersions={onLoadVersions}
          onChange={onSetPin}
        />
      )}
    </div>
  )
}

/** 版本选择器：展示当前用的版本 + 下拉选锁定版本 */
function VersionPicker({
  skillCurrentVersion,
  pinned,
  versions,
  onLoadVersions,
  onChange,
}: {
  skillCurrentVersion: number | null
  pinned: number | null
  versions: SkillVersion[] | undefined
  onLoadVersions: () => void
  onChange: (v: number | null) => void
}) {
  const label = pinned
    ? `锁定到 v${pinned}（测试）`
    : `跟随当前${skillCurrentVersion ? ` (v${skillCurrentVersion})` : ''}`

  return (
    <div className="flex items-center gap-1.5 text-xs pt-1 border-t border-neutral-100 dark:border-[#3c4043]/60">
      <GitBranch className="w-3 h-3 text-neutral-400 dark:text-[#9aa0a6] shrink-0" />
      <select
        value={pinned ?? ''}
        onChange={(e) => {
          const v = e.target.value
          onChange(v === '' ? null : Number(v))
        }}
        onFocus={onLoadVersions}
        onClick={(e) => e.stopPropagation()}
        className={cn(
          'flex-1 min-w-0 px-2 py-0.5 rounded border text-xs bg-white dark:bg-[#1e1f20]',
          pinned
            ? 'border-amber-300 dark:border-amber-700 text-amber-700 dark:text-amber-300'
            : 'border-neutral-200 dark:border-[#3c4043] text-neutral-600 dark:text-[#c4c7c5]',
        )}
        title={
          pinned
            ? '当前会话锁定使用此版本，发布新版本不会影响这里'
            : '跟随 skill 的当前激活版本——admin 切换激活版本时这里会跟着变'
        }
      >
        <option value="">{label}</option>
        {versions === undefined && pinned != null && (
          <option value={pinned}>v{pinned}（当前）</option>
        )}
        {versions?.map((v) => (
          <option key={v.id} value={v.version_number}>
            v{v.version_number}
            {v.is_current ? '（当前）' : ''}
            {v.notes ? ` — ${v.notes.slice(0, 24)}` : ''}
          </option>
        ))}
      </select>
    </div>
  )
}
