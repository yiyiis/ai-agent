import { useEffect, useState } from 'react'
import {
  Check,
  Download,
  GitCompare,
  Loader2,
  RotateCcw,
  Star,
  Trash2,
  Upload,
  User,
  X,
} from 'lucide-react'
import { api } from '../api/client'
import type { Skill, SkillFileDiff, SkillVersion } from '../types'
import { cn } from '../lib/utils'

interface Props {
  skill: Skill
  /** 重启动文件编辑器（恢复版本后需要重新加载工作副本）的回调 */
  onRestored?: () => void
}

export function SkillVersionsPanel({ skill, onRestored }: Props) {
  const [versions, setVersions] = useState<SkillVersion[]>([])
  const [loading, setLoading] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [publishNotes, setPublishNotes] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<number | null>(null)
  // diff 弹窗：null = 关闭
  const [diffing, setDiffing] = useState<{
    versionId: number
    versionNumber: number
  } | null>(null)
  // "工作副本 vs 当前激活版本"对比——发布前最想看的那个 diff
  const [workingDiffOpen, setWorkingDiffOpen] = useState(false)

  const refresh = async () => {
    setLoading(true)
    try {
      setVersions(await api.listSkillVersions(skill.id))
    } catch (e) {
      setError(String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [skill.id])

  const publish = async () => {
    setError(null)
    setPublishing(true)
    try {
      await api.publishSkillVersion(skill.id, publishNotes.trim() || null)
      setPublishNotes('')
      await refresh()
    } catch (e) {
      setError(String(e))
    } finally {
      setPublishing(false)
    }
  }

  const activate = async (v: SkillVersion) => {
    setError(null)
    setBusyId(v.id)
    try {
      await api.activateSkillVersion(skill.id, v.id)
      await refresh()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusyId(null)
    }
  }

  const restore = async (v: SkillVersion) => {
    if (
      !confirm(
        `把 v${v.version_number} 的文件复制到工作副本？\n工作副本里所有未发布的修改会被覆盖丢失。\n（这一步只动编辑区，不影响在跑的会话使用的版本）`,
      )
    )
      return
    setError(null)
    setBusyId(v.id)
    try {
      await api.restoreSkillVersion(skill.id, v.id)
      onRestored?.()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusyId(null)
    }
  }

  const remove = async (v: SkillVersion) => {
    if (!confirm(`删除 v${v.version_number}？该版本目录会被清理`)) return
    setError(null)
    setBusyId(v.id)
    try {
      await api.deleteSkillVersion(skill.id, v.id)
      await refresh()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusyId(null)
    }
  }

  const download = async (v: SkillVersion) => {
    setError(null)
    try {
      await api.downloadSkillVersion(skill.id, v.id, skill.name, v.version_number)
    } catch (e) {
      setError(String(e))
    }
  }

  return (
    <div className="space-y-4">
      <div className="border border-neutral-200 dark:border-[#3c4043] rounded-md p-3 bg-neutral-50 dark:bg-[#1e1f20]">
        <div className="text-sm font-medium mb-1 text-neutral-900 dark:text-[#f1f3f4]">发布当前工作副本</div>
        <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] mb-2">
          把工作副本完整快照成新版本，并设为激活版本。下一轮会话立即使用新版本。
        </p>
        <textarea
          value={publishNotes}
          onChange={(e) => setPublishNotes(e.target.value)}
          placeholder="备注（选填，比如：修了 retry 逻辑）"
          rows={2}
          className="w-full px-2 py-1.5 border border-neutral-300 dark:border-[#3c4043] bg-white dark:bg-[#28292a] text-neutral-900 dark:text-[#f1f3f4] placeholder:text-neutral-400 dark:placeholder:text-[#9aa0a6] rounded text-sm resize-none focus:outline-none focus:ring-1 focus:ring-[#1a73e8] dark:focus:ring-[#8ab4f8]"
        />
        <div className="flex justify-end items-center gap-2 mt-2">
          {/* 发布是不可逆动作，先让用户看清这次到底改了什么 */}
          <button
            onClick={() => setWorkingDiffOpen(true)}
            className="px-3 py-1.5 border border-neutral-300 dark:border-[#3c4043] bg-white dark:bg-[#28292a] text-neutral-700 dark:text-[#f1f3f4] rounded text-sm flex items-center gap-1 hover:bg-neutral-50 dark:hover:bg-[#333537] transition-colors"
            title="对比工作副本与当前激活版本"
          >
            <GitCompare className="w-3.5 h-3.5" />
            查看本次改动
          </button>
          <button
            onClick={publish}
            disabled={publishing}
            className="px-3 py-1.5 bg-neutral-900 dark:bg-[#8ab4f8] text-white dark:text-[#202124] font-medium rounded text-sm flex items-center gap-1 hover:bg-neutral-800 dark:hover:bg-[#a8c7fa] disabled:opacity-50 transition-colors"
          >
            {publishing ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
            ) : (
              <Upload className="w-3.5 h-3.5" />
            )}
            发布新版本
          </button>
        </div>
      </div>

      {error && (
        <div className="px-3 py-2 bg-red-50 dark:bg-red-950/40 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900/50">
          {error}
        </div>
      )}

      <div>
        <div className="text-xs text-neutral-500 dark:text-[#9aa0a6] mb-3 space-y-1 leading-relaxed">
          <div className="flex items-center gap-1">
            <Star className="w-3 h-3" /> 标记的为当前激活版本（在跑的会话使用它）
          </div>
          <div>
            <span className="font-medium text-neutral-700 dark:text-[#e3e3e3]">设为当前</span>
            <span className="text-neutral-500 dark:text-[#9aa0a6]">
              ：让会话立刻切换到这个版本，不动你正在编辑的工作副本
            </span>
          </div>
          <div>
            <span className="font-medium text-neutral-700 dark:text-[#e3e3e3]">复制到工作副本</span>
            <span className="text-neutral-500 dark:text-[#9aa0a6]">
              ：把这个版本的文件覆盖到工作副本，方便基于它继续编辑后再发新版（会丢失工作副本里未发布的修改）
            </span>
          </div>
        </div>
        {loading && (
          <div className="text-xs text-neutral-400 dark:text-[#9aa0a6] flex items-center gap-1">
            <Loader2 className="w-3 h-3 animate-spin" />
            加载中…
          </div>
        )}
        {!loading && versions.length === 0 && (
          <div className="text-sm text-neutral-400 dark:text-[#9aa0a6] text-center py-6">
            还没有任何版本
          </div>
        )}
        <div className="space-y-1.5">
          {versions.map((v) => (
            <div
              key={v.id}
              className={cn(
                'border rounded-md p-3 transition-colors',
                v.is_current
                  ? 'border-neutral-900 bg-neutral-50 dark:border-[#8ab4f8] dark:bg-[#1e1f20]'
                  : 'border-neutral-200 dark:border-[#3c4043] bg-white dark:bg-[#1e1f20]',
              )}
            >
              <div className="flex items-center justify-between mb-1">
                <div className="flex items-center gap-2 flex-wrap">
                  {v.is_current && (
                    <Star className="w-3.5 h-3.5 fill-yellow-400 text-yellow-500" />
                  )}
                  <span className="font-medium text-sm text-neutral-900 dark:text-[#f1f3f4]">v{v.version_number}</span>
                  <SourceBadge source={v.source} />
                  <span className="text-xs text-neutral-400 dark:text-[#9aa0a6]">
                    {new Date(v.created_at).toLocaleString()}
                  </span>
                  {v.created_by > 0 && (
                    <span
                      className="text-xs text-neutral-500 dark:text-[#9aa0a6] flex items-center gap-0.5"
                      title={
                        v.created_by_name
                          ? `发布者 ${v.created_by_name} (user_id=${v.created_by})`
                          : `发布者 user_id=${v.created_by}（JWT 里没带展示名）`
                      }
                    >
                      <User className="w-3 h-3" />
                      {v.created_by_name || `u${v.created_by}`}
                    </span>
                  )}
                </div>
                <div className="flex items-center gap-1">
                  {v.version_number > 1 && (
                    <button
                      onClick={() =>
                        setDiffing({
                          versionId: v.id,
                          versionNumber: v.version_number,
                        })
                      }
                      className="px-2 py-0.5 text-xs rounded hover:bg-neutral-200 dark:hover:bg-[#333537] text-neutral-700 dark:text-[#e3e3e3] flex items-center gap-1 transition-colors"
                      title={`对比 v${v.version_number - 1}`}
                    >
                      <GitCompare className="w-3 h-3" />
                      对比上一版
                    </button>
                  )}
                  {!v.is_current && (
                    <button
                      onClick={() => activate(v)}
                      disabled={busyId === v.id}
                      className="px-2 py-0.5 text-xs rounded hover:bg-neutral-200 dark:hover:bg-[#333537] text-neutral-700 dark:text-[#e3e3e3] flex items-center gap-1 disabled:opacity-50 transition-colors"
                      title="设为当前激活版本"
                    >
                      <Check className="w-3 h-3" />
                      设为当前
                    </button>
                  )}
                  <button
                    onClick={() => restore(v)}
                    disabled={busyId === v.id}
                    className="px-2 py-0.5 text-xs rounded hover:bg-neutral-200 dark:hover:bg-[#333537] text-neutral-700 dark:text-[#e3e3e3] flex items-center gap-1 disabled:opacity-50 transition-colors"
                    title="把这个版本的文件覆盖到工作副本（便于基于它编辑）"
                  >
                    <RotateCcw className="w-3 h-3" />
                    复制到工作副本
                  </button>
                  <button
                    onClick={() => download(v)}
                    className="p-1 rounded hover:bg-neutral-200 dark:hover:bg-[#333537] text-neutral-700 dark:text-[#e3e3e3] transition-colors"
                    title="下载 zip"
                  >
                    <Download className="w-3 h-3" />
                  </button>
                  {!v.is_current && (
                    <button
                      onClick={() => remove(v)}
                      disabled={busyId === v.id}
                      className="p-1 rounded hover:bg-red-50 dark:hover:bg-red-950/30 text-red-500 dark:text-red-400 disabled:opacity-50 transition-colors"
                      title="删除版本"
                    >
                      <Trash2 className="w-3 h-3" />
                    </button>
                  )}
                </div>
              </div>
              {v.notes && (
                <div className="text-xs text-neutral-600 dark:text-[#c4c7c5] mt-1 whitespace-pre-wrap">
                  {v.notes}
                </div>
              )}
            </div>
          ))}
        </div>
      </div>

      {diffing && (
        <DiffModal
          load={() => api.diffSkillVersion(skill.id, diffing.versionId)}
          titleFor={(from) =>
            from != null
              ? `v${from} → v${diffing.versionNumber}`
              : `v${diffing.versionNumber}（首版）`
          }
          onClose={() => setDiffing(null)}
        />
      )}

      {workingDiffOpen && (
        <DiffModal
          load={() => api.diffSkillWorkingCopy(skill.id)}
          titleFor={(from) =>
            from != null ? `v${from} → 工作副本（未发布）` : '工作副本（尚无已发布版本）'
          }
          onClose={() => setWorkingDiffOpen(false)}
        />
      )}
    </div>
  )
}

// ============================================================
// 来源标签
// ============================================================

function SourceBadge({ source }: { source: string }) {
  const map: Record<string, { label: string; cls: string }> = {
    upload: { label: '上传', cls: 'bg-sky-100 dark:bg-sky-950/50 text-sky-700 dark:text-sky-300' },
    publish: { label: '发布', cls: 'bg-emerald-100 dark:bg-emerald-950/50 text-emerald-700 dark:text-emerald-300' },
    create: { label: '新建', cls: 'bg-blue-100 dark:bg-blue-950/50 text-blue-700 dark:text-blue-300' },
    restore: { label: '恢复', cls: 'bg-amber-100 dark:bg-amber-950/50 text-amber-700 dark:text-amber-300' },
  }
  const entry = map[source] ?? { label: source, cls: 'bg-neutral-100 dark:bg-[#28292a] text-neutral-700 dark:text-[#c4c7c5]' }
  return (
    <span
      className={cn(
        'text-[10px] px-1.5 py-0.5 rounded font-medium',
        entry.cls,
      )}
    >
      {entry.label}
    </span>
  )
}

// ============================================================
// 版本对比弹窗
// ============================================================

/** 版本间对比和"工作副本 vs 激活版本"对比共用这个壳，只是数据来源不同。 */
function DiffModal({
  load,
  titleFor,
  onClose,
}: {
  load: () => Promise<{ from_version: number | null; files: SkillFileDiff[] }>
  /** 拿到 from_version 后生成标题，比如 "v2 → v3" / "v3 → 工作副本" */
  titleFor: (fromVersion: number | null) => string
  onClose: () => void
}) {
  const [diff, setDiff] = useState<{
    from_version: number | null
    files: SkillFileDiff[]
  } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)

  useEffect(() => {
    load()
      .then((d) => {
        setDiff(d)
        if (d.files.length > 0) setSelected(d.files[0].path)
      })
      .catch((e) => setError(String(e)))
    // load 每次渲染都是新函数，依赖它会死循环；调用方保证 modal 打开时参数不变
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const sel = diff?.files.find((f) => f.path === selected)

  return (
    <>
      <div className="fixed inset-0 bg-black/40 backdrop-blur-sm z-[60]" onClick={onClose} />
      <div className="fixed inset-0 z-[60] flex items-center justify-center p-6 pointer-events-none">
        <div className="bg-white dark:bg-[#1e1f20] border border-neutral-200 dark:border-[#3c4043] w-full max-w-5xl h-[80vh] rounded-xl shadow-2xl flex flex-col pointer-events-auto overflow-hidden">
          <header className="flex items-center justify-between px-5 py-3 border-b border-neutral-200 dark:border-[#3c4043]">
            <div>
              <div className="font-medium text-sm text-neutral-900 dark:text-[#f1f3f4]">
                {diff ? titleFor(diff.from_version) : '加载中…'}
              </div>
              <div className="text-xs text-neutral-500 dark:text-[#9aa0a6] mt-0.5">
                {diff
                  ? `${diff.files.length} 个文件变更`
                  : '加载中…'}
              </div>
            </div>
            <button
              className="p-1 hover:bg-neutral-100 dark:hover:bg-[#28292a] text-neutral-500 dark:text-[#9aa0a6] rounded transition-colors"
              onClick={onClose}
            >
              <X className="w-4 h-4" />
            </button>
          </header>

          {error && (
            <div className="mx-5 mt-3 px-3 py-2 bg-red-50 dark:bg-red-950/40 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900/50">
              {error}
            </div>
          )}

          <div className="flex-1 min-h-0 flex">
            {/* 文件列表 */}
            <div className="w-64 shrink-0 border-r border-neutral-200 dark:border-[#3c4043] overflow-y-auto scrollbar-thin">
              {!diff && (
                <div className="text-xs text-neutral-400 dark:text-[#9aa0a6] px-3 py-3 flex items-center gap-1">
                  <Loader2 className="w-3 h-3 animate-spin" />
                  加载中…
                </div>
              )}
              {diff && diff.files.length === 0 && (
                <div className="text-xs text-neutral-400 dark:text-[#9aa0a6] px-3 py-3">
                  没有变更
                </div>
              )}
              {diff?.files.map((f) => (
                <div
                  key={f.path}
                  onClick={() => setSelected(f.path)}
                  className={cn(
                    'flex items-center gap-1.5 px-3 py-1.5 text-xs cursor-pointer transition-colors',
                    f.path === selected
                      ? 'bg-neutral-900 dark:bg-[#28292a] text-white dark:text-[#8ab4f8]'
                      : 'text-neutral-700 dark:text-[#c4c7c5] hover:bg-neutral-50 dark:hover:bg-[#28292a]/50',
                  )}
                  title={f.path}
                >
                  <StatusDot status={f.status} />
                  <span className="font-mono truncate">{f.path}</span>
                </div>
              ))}
            </div>

            {/* unified diff */}
            <div className="flex-1 min-w-0 overflow-auto bg-neutral-50 dark:bg-[#131314]">
              {sel ? (
                <pre className="text-xs font-mono p-4 whitespace-pre-wrap break-all">
                  {colorizeDiff(sel.diff)}
                </pre>
              ) : (
                <div className="text-sm text-neutral-400 dark:text-[#9aa0a6] text-center py-12">
                  从左侧选一个文件查看变更
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </>
  )
}

function StatusDot({ status }: { status: string }) {
  const map: Record<string, string> = {
    added: 'bg-emerald-500',
    removed: 'bg-red-500',
    modified: 'bg-amber-500',
  }
  return (
    <span
      className={cn('w-1.5 h-1.5 rounded-full shrink-0', map[status] ?? 'bg-neutral-300 dark:bg-neutral-600')}
      title={status}
    />
  )
}

/** 给 unified diff 文本上色：+ 行绿、- 行红、@@ 行蓝、其他灰 */
function colorizeDiff(text: string) {
  const lines = text.split('\n')
  return lines.map((ln, i) => {
    let cls = 'text-neutral-700 dark:text-[#c4c7c5]'
    if (ln.startsWith('+++') || ln.startsWith('---')) cls = 'text-neutral-500 dark:text-[#9aa0a6] font-semibold'
    else if (ln.startsWith('@@')) cls = 'text-sky-600 dark:text-sky-400'
    else if (ln.startsWith('+')) cls = 'text-emerald-700 dark:text-emerald-300 bg-emerald-50 dark:bg-emerald-950/30'
    else if (ln.startsWith('-')) cls = 'text-red-700 dark:text-red-300 bg-red-50 dark:bg-red-950/30'
    return (
      <div key={i} className={cls}>
        {ln || ' '}
      </div>
    )
  })
}
