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
  sessionId: string
  /** 已启用的 skill + 各自锁定的版本（null = 跟随当前） */
  enabledEntries: EnabledSkillEntry[]
  onEnabledChange: (entries: EnabledSkillEntry[]) => void
  /** 是否允许模型按用户意图自动加载未勾选的 skill */
  autoSkill: boolean
  onAutoSkillChange: (next: boolean) => void
}

export function SkillsPanel({
  open,
  onClose,
  sessionId,
  enabledEntries,
  onEnabledChange,
  autoSkill,
  onAutoSkillChange,
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

  const isEnabled = (id: number) =>
    enabledEntries.some((e) => e.skill_id === id)
  const pinOf = (id: number): number | null =>
    enabledEntries.find((e) => e.skill_id === id)?.pinned_version_number ?? null
  const isAutoLoaded = (id: number) =>
    enabledEntries.find((e) => e.skill_id === id)?.source === 'auto'

  const commit = async (next: EnabledSkillEntry[]) => {
    onEnabledChange(next)
    try {
      await api.setEnabledSkills(sessionId, next)
    } catch (e) {
      setError(String(e))
    }
  }

  const toggle = async (id: number) => {
    const next = isEnabled(id)
      ? enabledEntries.filter((e) => e.skill_id !== id)
      : [...enabledEntries, { skill_id: id, pinned_version_number: null }]
    await commit(next)
  }

  const setPin = async (id: number, pinned: number | null) => {
    const next = enabledEntries.map((e) =>
      e.skill_id === id ? { ...e, pinned_version_number: pinned } : e,
    )
    await commit(next)
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
          <div className="font-medium">Skills</div>
          <button
            className="p-1 hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded text-neutral-500 dark:text-[#9aa0a6]"
            onClick={onClose}
          >
            <X className="w-4 h-4" />
          </button>
        </header>

        <label className="flex items-start gap-2.5 px-4 py-3 border-b border-neutral-200 dark:border-[#3c4043] cursor-pointer hover:bg-neutral-50 dark:hover:bg-[#28292a]">
          <input
            type="checkbox"
            checked={autoSkill}
            onChange={(e) => onAutoSkillChange(e.target.checked)}
            className="mt-0.5 shrink-0"
          />
          <div className="min-w-0">
            <div className="flex items-center gap-1.5 text-sm font-medium">
              <Sparkles className="w-3.5 h-3.5 text-neutral-500 dark:text-[#9aa0a6]" />
              自动选择 skill
            </div>
            <p className="text-xs text-neutral-400 dark:text-[#747775] mt-0.5">
              模型会看到未勾选 skill 的名字和用途摘要，按你的需求自动加载。
              下面手工勾选的始终启用，不受这里影响。
            </p>
          </div>
        </label>

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

        <div className="flex-1 overflow-y-auto scrollbar-thin px-2 py-2">
          {skills.length === 0 && (
            <div className="text-center text-neutral-400 dark:text-[#747775] text-sm py-12">
              还没有 skill，先上传一个
            </div>
          )}
          {skills.map((s) => (
            <SkillRow
              key={s.id}
              skill={s}
              enabled={isEnabled(s.id)}
              autoLoaded={isAutoLoaded(s.id)}
              pinned={pinOf(s.id)}
              versions={versionCache[s.id]}
              onToggle={() => toggle(s.id)}
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
  enabled,
  autoLoaded,
  pinned,
  versions,
  onToggle,
  onSetPin,
  onLoadVersions,
  onView,
  onDownload,
  onDelete,
}: {
  skill: Skill
  enabled: boolean
  autoLoaded: boolean
  pinned: number | null
  versions: SkillVersion[] | undefined
  onToggle: () => void
  onSetPin: (v: number | null) => void
  onLoadVersions: () => void
  onView: () => void
  onDownload: () => void
  onDelete: () => void
}) {
  return (
    <div
      className={cn(
        'group px-3 py-2.5 rounded-md border mb-1.5 flex flex-col gap-1.5',
        enabled
          ? 'border-neutral-900 bg-neutral-50 dark:border-[#8ab4f8] dark:bg-[#28292a]'
          : 'border-neutral-200 dark:border-[#3c4043] hover:bg-neutral-50 dark:hover:bg-[#28292a]',
      )}
    >
      <div
        className="flex gap-3 cursor-pointer"
        onClick={onToggle}
      >
        <input
          type="checkbox"
          checked={enabled}
          onChange={onToggle}
          onClick={(e) => e.stopPropagation()}
          className="mt-0.5 shrink-0"
        />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5">
            <span className="text-sm font-medium truncate text-[#1f1f1f] dark:text-[#f1f3f4]">{skill.name}</span>
            {autoLoaded && (
              <span
                className="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-px rounded bg-blue-100 dark:bg-blue-950/40 text-blue-700 dark:text-blue-300 shrink-0"
                title="模型在对话中按你的需求自动加载的；取消勾选即可移除"
              >
                <Sparkles className="w-2.5 h-2.5" />
                自动
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
          {skill.description && (
            <div className="text-xs text-neutral-500 dark:text-[#9aa0a6] mt-0.5 line-clamp-2">
              {skill.description}
            </div>
          )}
        </div>
        <div className="flex items-start gap-0.5 opacity-0 group-hover:opacity-100 transition shrink-0">
          {skill.can_view !== false && (
            <>
              <button
                className="p-1 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#333537] rounded"
                onClick={(e) => {
                  e.stopPropagation()
                  onView()
                }}
                title="查看详情 / 编辑机密"
              >
                <Eye className="w-3.5 h-3.5" />
              </button>
              <button
                className="p-1 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#333537] rounded"
                onClick={(e) => {
                  e.stopPropagation()
                  onDownload()
                }}
                title="下载 skill 包（不含机密）"
              >
                <Download className="w-3.5 h-3.5" />
              </button>
            </>
          )}
          {(skill.is_owner || !skill.confidential) && (
            <button
              className="p-1 text-red-500 hover:bg-red-50 dark:hover:bg-red-950/40 rounded"
              onClick={(e) => {
                e.stopPropagation()
                onDelete()
              }}
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
        </div>
      </div>

      {enabled && (
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
    <div className="ml-6 flex items-center gap-1.5 text-xs">
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
