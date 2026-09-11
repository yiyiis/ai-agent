import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertTriangle,
  ArrowUpDown,
  ChevronLeft,
  ChevronRight,
  Code2,
  Cpu,
  Download,
  FilePlus2,
  Loader2,
  Lock,
  PencilLine,
  Search,
  Trash2,
  Unlock,
  Upload,
  Users,
  X,
  Zap,
} from 'lucide-react'
import { api } from '../api/client'
import type { Skill } from '../types'
import { cn } from '../lib/utils'
import { SkillDetailModal, type SkillTab } from './SkillDetailModal'
import { Toast, type ToastSpec } from './Toast'

type SortKey = 'updated' | 'name' | 'created' | 'usage'

/** 只筛某一类状态的 skill——比在一堆卡片里用眼睛找快得多 */
type StatusFilter = 'all' | 'api' | 'unpublished' | 'broken'

const PAGE_SIZES = [12, 24, 48] as const

/**
 * Skills 广场——所有 skill 的全局视图。
 *
 * 和会话抽屉的分工：
 *   抽屉：只管"当前会话开关哪些 skill"
 *   广场：浏览、创建、编辑、版本、删除、下载
 */
export function SkillsPlaza() {
  const [skills, setSkills] = useState<Skill[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('updated')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [activeTags, setActiveTags] = useState<string[]>([])
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<number>(PAGE_SIZES[0])
  const [pendingDelete, setPendingDelete] = useState<Skill | null>(null)

  // 打开详情 modal 的目标 skill + 默认 tab
  const [opened, setOpened] = useState<{ skill: Skill; tab: SkillTab } | null>(
    null,
  )
  const [newOpen, setNewOpen] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [uploadConfidential, setUploadConfidential] = useState(false)
  const [toast, setToast] = useState<ToastSpec | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  const refresh = async () => {
    setError(null)
    setLoading(true)
    try {
      setSkills(await api.listSkills())
    } catch (e) {
      setError(String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
  }, [])

  /** 全库出现过的标签 + 各自数量，用来渲染筛选条 */
  const tagCounts = useMemo(() => {
    const m = new Map<string, number>()
    for (const s of skills) {
      for (const t of s.tags ?? []) m.set(t, (m.get(t) ?? 0) + 1)
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  }, [skills])

  const statusCounts = useMemo(
    () => ({
      all: skills.length,
      api: skills.filter((s) => s.api_enabled).length,
      unpublished: skills.filter((s) => s.has_unpublished_changes).length,
      broken: skills.filter((s) => s.manifest_error).length,
    }),
    [skills],
  )

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    let arr = q
      ? skills.filter(
          (s) =>
            s.name.toLowerCase().includes(q) ||
            (s.description ?? '').toLowerCase().includes(q) ||
            (s.owner_name ?? '').toLowerCase().includes(q) ||
            (s.tags ?? []).some((t) => t.toLowerCase().includes(q)),
        )
      : [...skills]

    if (status === 'api') arr = arr.filter((s) => s.api_enabled)
    else if (status === 'unpublished')
      arr = arr.filter((s) => s.has_unpublished_changes)
    else if (status === 'broken') arr = arr.filter((s) => s.manifest_error)

    // 多选标签取交集——「工具 + 搜索」应该是"两个标签都有"
    if (activeTags.length > 0) {
      arr = arr.filter((s) =>
        activeTags.every((t) => (s.tags ?? []).includes(t)),
      )
    }

    arr.sort((a, b) => {
      switch (sortKey) {
        case 'name':
          return a.name.localeCompare(b.name)
        case 'created':
          return b.created_at.localeCompare(a.created_at)
        case 'usage':
          return (b.enabled_session_count ?? 0) - (a.enabled_session_count ?? 0)
        case 'updated':
        default: {
          const at = a.updated_at ?? a.created_at
          const bt = b.updated_at ?? b.created_at
          return bt.localeCompare(at)
        }
      }
    })
    return arr
  }, [skills, query, sortKey, status, activeTags])

  // 筛选条件一变就回到第一页，否则会停在一个空白页上
  useEffect(() => {
    setPage(1)
  }, [query, sortKey, status, activeTags, pageSize])

  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize))
  const safePage = Math.min(page, totalPages)
  const pageItems = useMemo(
    () => filtered.slice((safePage - 1) * pageSize, safePage * pageSize),
    [filtered, safePage, pageSize],
  )

  const toggleTag = (t: string) =>
    setActiveTags((prev) =>
      prev.includes(t) ? prev.filter((x) => x !== t) : [...prev, t],
    )

  const onUpload = async (file: File) => {
    setError(null)
    setUploading(true)
    try {
      const result = await api.uploadSkill(file, uploadConfidential)
      await refresh()
      const v = result.created_version_number
      if (result.activated) {
        // 首次上传：v1 已自动激活
        setToast({
          variant: 'success',
          title: `已上传「${result.skill.name}」`,
          description: `已发布 v${v} 并设为当前版本，会话可直接使用。`,
          durationMs: 5000,
        })
      } else {
        // 同名升级：新版本默认不激活
        setToast({
          variant: 'success',
          title: `已上传「${result.skill.name}」，发布为 v${v}`,
          description: `新版本未激活——会话仍在用旧版。如需切换，请在弹出的版本页点「设为当前」。`,
          durationMs: 8000,
        })
      }
      // 自动打开详情 modal 的版本 tab，方便用户立刻操作
      setOpened({ skill: result.skill, tab: 'versions' })
    } catch (e) {
      setToast({
        variant: 'error',
        title: '上传失败',
        description: String(e),
        durationMs: 6000,
      })
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  /** 统一的失败反馈——之前删除/下载走顶部红条、上传走 Toast，两套并存 */
  const fail = (title: string, e: unknown) =>
    setToast({
      variant: 'error',
      title,
      description: String(e),
      durationMs: 6000,
    })

  const onDelete = async (s: Skill) => {
    try {
      await api.deleteSkill(s.id)
      setPendingDelete(null)
      await refresh()
      setToast({
        variant: 'success',
        title: `已删除「${s.name}」`,
        durationMs: 4000,
      })
    } catch (e) {
      fail('删除失败', e)
    }
  }

  const onDownload = async (s: Skill) => {
    try {
      await api.downloadSkill(s.id, s.name)
    } catch (e) {
      fail('下载失败', e)
    }
  }

  const onToggleConfidential = async (s: Skill) => {
    try {
      await api.updateSkill(s.id, { confidential: !s.confidential })
      await refresh()
    } catch (e) {
      fail('切换机密状态失败', e)
    }
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden bg-slate-50/40 dark:bg-[#131314]">
      {/* 顶部栏 */}
      <div className="border-b border-slate-200/80 dark:border-[#3c4043] bg-white/80 dark:bg-[#1e1f20]/90 backdrop-blur-md px-8 py-4 flex items-center gap-4">
        <div>
          <h1 className="text-lg font-bold text-slate-900 dark:text-[#f1f3f4] tracking-tight">Skills 技能广场</h1>
          <p className="text-xs text-slate-500 dark:text-[#9aa0a6] mt-0.5">
            共 {skills.length} 个技能 · 在这里统一管理代码、能力发布与机密配置
          </p>
        </div>
        <div className="flex-1" />
        <div className="relative">
          <Search className="w-3.5 h-3.5 absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-[#9aa0a6]" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索名称 / 描述 / 标签 / 作者…"
            className="pl-8 pr-3 py-1.5 w-64 border border-slate-200 dark:border-[#3c4043] rounded-xl text-xs bg-slate-50/60 dark:bg-[#28292a] text-slate-900 dark:text-[#f1f3f4] focus:bg-white dark:focus:bg-[#1e1f20] focus:outline-none focus:border-[#1a73e8] dark:focus:border-[#8ab4f8] focus:ring-2 focus:ring-[#d3e3fd] dark:focus:ring-[#004a77] transition-all placeholder:text-slate-400 dark:placeholder:text-[#9aa0a6]"
          />
        </div>
        <div className="relative">
          <ArrowUpDown className="w-3.5 h-3.5 absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-[#9aa0a6] pointer-events-none" />
          <select
            value={sortKey}
            onChange={(e) => setSortKey(e.target.value as SortKey)}
            className="pl-8 pr-3 py-1.5 border border-slate-200 dark:border-[#3c4043] rounded-xl text-xs focus:outline-none focus:border-[#1a73e8] dark:focus:border-[#8ab4f8] bg-slate-50/60 dark:bg-[#28292a] focus:bg-white dark:focus:bg-[#1e1f20] transition-all text-slate-700 dark:text-[#f1f3f4]"
          >
            <option value="updated">最近更新</option>
            <option value="created">最近创建</option>
            <option value="name">技能名称</option>
            <option value="usage">使用频次</option>
          </select>
        </div>
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
        <label
          className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-[#c4c7c5] cursor-pointer select-none px-2 py-1 rounded-lg hover:bg-slate-100/60 dark:hover:bg-[#28292a] transition-colors"
          title="勾选后上传的 skill 为机密：除你之外他人不可预览/下载，只能使用"
        >
          <input
            type="checkbox"
            className="rounded text-[#1a73e8] focus:ring-[#1a73e8]"
            checked={uploadConfidential}
            onChange={(e) => setUploadConfidential(e.target.checked)}
          />
          <Lock className="w-3 h-3 text-slate-400 dark:text-[#9aa0a6]" />
          <span>机密</span>
        </label>
        <button
          disabled={uploading}
          onClick={() => fileRef.current?.click()}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium border border-slate-200 dark:border-[#3c4043] rounded-xl bg-white dark:bg-[#28292a] hover:bg-slate-50 dark:hover:bg-[#333537] text-slate-700 dark:text-[#f1f3f4] shadow-card hover:border-slate-300 dark:hover:border-[#5e6368] disabled:opacity-50 transition-all"
        >
          {uploading ? (
            <Loader2 className="w-3.5 h-3.5 animate-spin text-[#1a73e8]" />
          ) : (
            <Upload className="w-3.5 h-3.5 text-slate-500 dark:text-[#9aa0a6]" />
          )}
          上传 zip
        </button>
        <button
          onClick={() => setNewOpen(true)}
          className="flex items-center gap-1.5 px-4 py-1.5 text-xs font-medium bg-[#1a73e8] hover:bg-[#1557b0] text-white rounded-full shadow-sm hover:shadow transition-all"
        >
          <FilePlus2 className="w-3.5 h-3.5" />
          从空新建
        </button>
      </div>

      {/* 筛选条：状态 + 标签。都是"缩小范围"的工具，放一行里 */}
      {(skills.length > 0 || activeTags.length > 0) && (
        <div className="border-b border-neutral-200 dark:border-[#3c4043] bg-white dark:bg-[#1e1f20] px-8 py-2 flex items-center gap-2 flex-wrap">
          {(
            [
              ['all', '全部', statusCounts.all],
              ['api', '已开放 API', statusCounts.api],
              ['unpublished', '有未发布改动', statusCounts.unpublished],
              ['broken', 'manifest 异常', statusCounts.broken],
            ] as [StatusFilter, string, number][]
          ).map(([key, label, count]) =>
            // 「异常」这类为 0 时不占位置，避免筛选条变成噪音
            count === 0 && key !== 'all' && status !== key ? null : (
              <button
                key={key}
                onClick={() => setStatus(key)}
                className={cn(
                  'px-2.5 py-1 rounded-full text-xs border transition',
                  status === key
                    ? 'bg-neutral-900 text-white border-neutral-900 dark:bg-white dark:text-neutral-900 dark:border-white'
                    : 'bg-white dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5] border-neutral-200 dark:border-[#3c4043] hover:border-neutral-400 dark:hover:border-[#5e6368]',
                  key === 'broken' &&
                    status !== key &&
                    count > 0 &&
                    'text-red-600 border-red-200 dark:text-red-400 dark:border-red-900',
                )}
              >
                {label} {count}
              </button>
            ),
          )}

          {tagCounts.length > 0 && (
            <div className="w-px h-4 bg-neutral-200 dark:bg-[#3c4043] mx-1" />
          )}
          {tagCounts.map(([tag, count]) => (
            <button
              key={tag}
              onClick={() => toggleTag(tag)}
              className={cn(
                'px-2.5 py-1 rounded-full text-xs border transition',
                activeTags.includes(tag)
                  ? 'bg-sky-600 text-white border-sky-600 dark:bg-[#004a77] dark:text-[#c2e7ff] dark:border-[#004a77]'
                  : 'bg-white dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5] border-neutral-200 dark:border-[#3c4043] hover:border-neutral-400 dark:hover:border-[#5e6368]',
              )}
            >
              #{tag} {count}
            </button>
          ))}
          {activeTags.length > 0 && (
            <button
              onClick={() => setActiveTags([])}
              className="text-xs text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] px-1"
            >
              清除标签
            </button>
          )}
        </div>
      )}

      {/* 错误提示 */}
      {error && (
        <div className="mx-8 mt-4 px-3 py-2 bg-red-50 text-red-600 text-sm rounded border border-red-100">
          {error}
        </div>
      )}

      {/* 主区域 */}
      <div className="flex-1 overflow-y-auto scrollbar-thin px-8 py-6">
        {loading && skills.length === 0 ? (
          <div className="text-center text-neutral-400 py-20 flex items-center justify-center gap-2 text-sm">
            <Loader2 className="w-4 h-4 animate-spin" /> 加载中…
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState
            hasQuery={!!query.trim()}
            onUpload={() => fileRef.current?.click()}
            onNew={() => setNewOpen(true)}
          />
        ) : (
          <>
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
              {pageItems.map((s) => (
                <SkillCard
                  key={s.id}
                  skill={s}
                  onOpen={(tab) => setOpened({ skill: s, tab })}
                  onDelete={() => setPendingDelete(s)}
                  onDownload={() => onDownload(s)}
                  onToggleConfidential={() => onToggleConfidential(s)}
                  onTagClick={toggleTag}
                  onBlockedView={() =>
                    setToast({
                      variant: 'error',
                      title: '该 skill 为机密',
                      description:
                        '仅所有者可预览 / 下载；你可以在会话中直接启用使用。',
                      durationMs: 4000,
                    })
                  }
                />
              ))}
            </div>

            <Pagination
              page={safePage}
              totalPages={totalPages}
              total={filtered.length}
              pageSize={pageSize}
              onPage={setPage}
              onPageSize={setPageSize}
            />
          </>
        )}
      </div>

      {opened && (
        <SkillDetailModal
          skill={opened.skill}
          initialTab={opened.tab}
          onClose={() => {
            setOpened(null)
            refresh() // 编辑完返回——可能版本/描述变了
          }}
        />
      )}

      {newOpen && (
        <NewSkillModal
          onClose={() => setNewOpen(false)}
          onCreated={(skill) => {
            setNewOpen(false)
            refresh()
            // 新建完直接进编辑器，让用户起手
            setOpened({ skill, tab: 'files' })
          }}
        />
      )}

      {pendingDelete && (
        <DeleteSkillModal
          skill={pendingDelete}
          onCancel={() => setPendingDelete(null)}
          onConfirm={() => onDelete(pendingDelete)}
        />
      )}

      {toast && <Toast {...toast} onClose={() => setToast(null)} />}
    </div>
  )
}

// ============================================================
// 分页
// ============================================================

function Pagination({
  page,
  totalPages,
  total,
  pageSize,
  onPage,
  onPageSize,
}: {
  page: number
  totalPages: number
  total: number
  pageSize: number
  onPage: (p: number) => void
  onPageSize: (n: number) => void
}) {
  // 只有一页且没超过最小页长时，分页条纯属噪音
  if (totalPages <= 1 && total <= PAGE_SIZES[0]) return null

  const from = (page - 1) * pageSize + 1
  const to = Math.min(page * pageSize, total)

  return (
    <div className="flex items-center justify-between gap-4 mt-6 pt-4 border-t border-neutral-200 dark:border-[#3c4043] text-xs text-neutral-500 dark:text-[#9aa0a6]">
      <div>
        第 {from}–{to} 个，共 {total} 个
      </div>
      <div className="flex items-center gap-3">
        <label className="flex items-center gap-1.5">
          每页
          <select
            value={pageSize}
            onChange={(e) => onPageSize(Number(e.target.value))}
            className="px-1.5 py-0.5 border border-neutral-300 dark:border-[#3c4043] rounded bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4]"
          >
            {PAGE_SIZES.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <div className="flex items-center gap-1">
          <button
            disabled={page <= 1}
            onClick={() => onPage(page - 1)}
            className="p-1 rounded border border-neutral-200 dark:border-[#3c4043] hover:bg-neutral-50 dark:hover:bg-[#28292a] disabled:opacity-40 disabled:hover:bg-transparent text-[#1f1f1f] dark:text-[#f1f3f4]"
          >
            <ChevronLeft className="w-3.5 h-3.5" />
          </button>
          <span className="px-1 tabular-nums">
            {page} / {totalPages}
          </span>
          <button
            disabled={page >= totalPages}
            onClick={() => onPage(page + 1)}
            className="p-1 rounded border border-neutral-200 dark:border-[#3c4043] hover:bg-neutral-50 dark:hover:bg-[#28292a] disabled:opacity-40 disabled:hover:bg-transparent text-[#1f1f1f] dark:text-[#f1f3f4]"
          >
            <ChevronRight className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    </div>
  )
}

// ============================================================
// 删除确认
// ============================================================

/**
 * 删除前把影响面摆出来。
 *
 * 之前是一句 confirm()，看不出"这个 skill 正被 12 个会话使用"或"外部系统正在
 * 调它的 API"——而这两件事恰恰是删完才会爆的。已开放 API 的还要求手输名字，
 * 因为那类删除会直接打断外部调用方。
 */
function DeleteSkillModal({
  skill,
  onCancel,
  onConfirm,
}: {
  skill: Skill
  onCancel: () => void
  onConfirm: () => void
}) {
  const [typed, setTyped] = useState('')
  const sessions = skill.enabled_session_count ?? 0
  const needsTyping = skill.api_enabled === true
  const canDelete = !needsTyping || typed.trim() === skill.name

  return (
    <>
      <div className="fixed inset-0 bg-black/40 z-[60]" onClick={onCancel} />
      <div className="fixed inset-0 z-[60] flex items-center justify-center p-6 pointer-events-none">
        <div className="bg-white dark:bg-[#1e1f20] border border-transparent dark:border-[#3c4043] w-full max-w-md rounded-xl shadow-2xl pointer-events-auto p-5 space-y-4 text-[#1f1f1f] dark:text-[#f1f3f4]">
          <div className="flex items-start gap-3">
            <div className="w-9 h-9 rounded-lg bg-red-100 dark:bg-red-950/50 text-red-600 dark:text-red-400 flex items-center justify-center shrink-0">
              <Trash2 className="w-4 h-4" />
            </div>
            <div className="min-w-0">
              <div className="font-medium text-[#1f1f1f] dark:text-[#f1f3f4]">删除「{skill.name}」</div>
              <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] mt-0.5">
                所有版本、机密配置、会话关联都会被一并清除，不可恢复。
              </p>
            </div>
          </div>

          <div className="space-y-1.5 text-xs">
            {skill.api_enabled && (
              <div className="flex items-start gap-2 px-3 py-2 bg-red-50 dark:bg-red-950/30 border border-red-100 dark:border-red-900 text-red-700 dark:text-red-400 rounded">
                <Zap className="w-3.5 h-3.5 shrink-0 mt-px" />
                <span>
                  已对外开放 API——删除后所有外部调用会立刻开始报错。
                </span>
              </div>
            )}
            {sessions > 0 && (
              <div className="flex items-start gap-2 px-3 py-2 bg-amber-50 dark:bg-amber-950/30 border border-amber-100 dark:border-amber-900 text-amber-700 dark:text-amber-400 rounded">
                <Users className="w-3.5 h-3.5 shrink-0 mt-px" />
                <span>
                  当前有 <b>{sessions}</b> 个会话启用了它，这些会话将失去该能力。
                </span>
              </div>
            )}
            {!skill.api_enabled && sessions === 0 && (
              <div className="px-3 py-2 bg-neutral-50 dark:bg-[#28292a] border border-neutral-200 dark:border-[#3c4043] text-neutral-500 dark:text-[#9aa0a6] rounded">
                没有会话在使用，也未对外开放 API。
              </div>
            )}
          </div>

          {needsTyping && (
            <div>
              <label className="text-xs text-neutral-600 dark:text-[#c4c7c5]">
                请输入 <b className="font-mono">{skill.name}</b> 以确认
              </label>
              <input
                autoFocus
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                className="mt-1 w-full px-2.5 py-1.5 border border-neutral-300 dark:border-[#3c4043] rounded text-sm font-mono bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] focus:outline-none focus:border-neutral-900 dark:focus:border-[#8ab4f8]"
              />
            </div>
          )}

          <div className="flex justify-end gap-2 pt-1">
            <button
              onClick={onCancel}
              className="px-3 py-1.5 text-sm border border-neutral-300 dark:border-[#3c4043] rounded text-neutral-700 dark:text-[#c4c7c5] hover:bg-neutral-50 dark:hover:bg-[#28292a]"
            >
              取消
            </button>
            <button
              disabled={!canDelete}
              onClick={onConfirm}
              className="px-3 py-1.5 text-sm bg-red-600 text-white rounded hover:bg-red-700 disabled:opacity-40 disabled:hover:bg-red-600"
            >
              确认删除
            </button>
          </div>
        </div>
      </div>
    </>
  )
}

// ============================================================
// 卡片
// ============================================================

/** 根据 name 生成一个稳定的浅色背景——给卡片头部 avatar 用 */
function colorFromName(name: string): string {
  // 8 种克制的浅色，避免广场看上去像彩虹
  const palette = [
    'bg-rose-100 text-rose-700',
    'bg-amber-100 text-amber-700',
    'bg-lime-100 text-lime-700',
    'bg-emerald-100 text-emerald-700',
    'bg-cyan-100 text-cyan-700',
    'bg-sky-100 text-sky-700',
    'bg-blue-100 text-blue-700',
    'bg-teal-100 text-teal-700',
  ]
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) | 0
  return palette[Math.abs(h) % palette.length]
}

function relTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (isNaN(t)) return ''
  const diff = Date.now() - t
  const day = 24 * 3600 * 1000
  if (diff < 60_000) return '刚刚'
  if (diff < 3600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < day) return `${Math.floor(diff / 3600_000)} 小时前`
  if (diff < 7 * day) return `${Math.floor(diff / day)} 天前`
  return new Date(iso).toLocaleDateString()
}

function SkillCard({
  skill,
  onOpen,
  onDelete,
  onDownload,
  onToggleConfidential,
  onTagClick,
  onBlockedView,
}: {
  skill: Skill
  onOpen: (tab: SkillTab) => void
  onDelete: () => void
  onDownload: () => void
  onToggleConfidential: () => void
  onTagClick: (tag: string) => void
  onBlockedView: () => void
}) {
  const initial = skill.name.trim().charAt(0).toUpperCase() || '?'
  // 机密且非所有者：只能使用，不可预览/下载/编辑
  const locked = skill.confidential === true && skill.can_view === false
  const isOwner = skill.is_owner === true
  return (
    <div
      className={cn(
        'group bg-white dark:bg-[#1e1f20] border rounded-2xl p-5 shadow-card hover:shadow-card-hover transition-all duration-200 cursor-pointer flex flex-col',
        locked
          ? 'border-slate-200/80 dark:border-[#3c4043] hover:border-slate-300 dark:hover:border-[#5e6368]'
          : 'border-slate-200/80 dark:border-[#3c4043] hover:border-[#b4d7fe] dark:hover:border-[#1a73e8] hover:-translate-y-0.5',
      )}
      onClick={() => (locked ? onBlockedView() : onOpen('files'))}
    >
      <div className="flex items-start gap-3 mb-2.5">
        <div
          className={cn(
            'w-10 h-10 rounded-xl flex items-center justify-center font-bold text-sm shrink-0 shadow-sm',
            colorFromName(skill.name),
          )}
        >
          {initial}
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <h3 className="font-medium truncate text-[#1f1f1f] dark:text-[#f1f3f4]">{skill.name}</h3>
            {skill.confidential && (
              <span
                className="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-0.5 rounded bg-amber-100 dark:bg-amber-950/50 text-amber-700 dark:text-amber-300 shrink-0"
                title={
                  isOwner
                    ? '机密：仅你可预览/下载，他人只能使用'
                    : '机密：仅所有者可预览/下载，你可以直接使用'
                }
              >
                <Lock className="w-2.5 h-2.5" />
                机密
              </span>
            )}
            {skill.current_version_number != null && (
              <span className="text-[10px] px-1.5 py-0.5 rounded bg-neutral-100 dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5] font-mono shrink-0">
                v{skill.current_version_number}
              </span>
            )}
            {skill.has_unpublished_changes && (
              <span
                className="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-0.5 rounded bg-orange-100 dark:bg-orange-950/50 text-orange-700 dark:text-orange-300 shrink-0"
                title="工作副本里有改动还没发布——会话和 API 用的仍是当前激活版本"
              >
                <PencilLine className="w-2.5 h-2.5" />
                未发布
              </span>
            )}
          </div>
          <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] mt-1 line-clamp-2 min-h-[2em]">
            {skill.description || '（无描述）'}
          </p>
        </div>
      </div>

      {/* manifest 坏了要最显眼——这个 skill 已经从对外接口消失了，
          而以前界面上没有任何迹象 */}
      {skill.manifest_error && (
        <button
          onClick={(e) => {
            e.stopPropagation()
            onOpen('settings')
          }}
          className="flex items-start gap-1.5 w-full text-left px-2 py-1.5 mb-2 rounded bg-red-50 dark:bg-red-950/30 border border-red-100 dark:border-red-900 text-red-700 dark:text-red-400 text-[11px] hover:bg-red-100 dark:hover:bg-red-950/50 transition"
          title={skill.manifest_error}
        >
          <AlertTriangle className="w-3 h-3 shrink-0 mt-px" />
          <span className="truncate">
            manifest.json 有误，已无法被 API 调用
          </span>
        </button>
      )}

      {/* 能力徽章 + 标签 */}
      {(skill.api_enabled ||
        skill.model ||
        (skill.tags ?? []).length > 0) && (
        <div className="flex items-center gap-1 flex-wrap mb-2">
          {skill.api_enabled && (
            <span
              className="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-0.5 rounded bg-emerald-100 dark:bg-emerald-950/50 text-emerald-700 dark:text-emerald-300"
              title="有 manifest.json 且 api_enabled=true，可被外部接口调用"
            >
              <Zap className="w-2.5 h-2.5" />
              API
            </span>
          )}
          {skill.llm_enabled && (
            <span
              className="text-[10px] px-1.5 py-0.5 rounded bg-sky-100 dark:bg-sky-950/50 text-sky-700 dark:text-sky-300"
              title="支持 mode=llm：由 LLM 主导执行，而不是直接跑脚本"
            >
              LLM
            </span>
          )}
          {skill.model && (
            <span
              className="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-0.5 rounded bg-neutral-100 dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5] font-mono max-w-[12rem] truncate"
              title={`API 调用固定使用模型：${skill.model}`}
            >
              <Cpu className="w-2.5 h-2.5 shrink-0" />
              {skill.model}
            </span>
          )}
          {(skill.tags ?? []).map((t) => (
            <button
              key={t}
              onClick={(e) => {
                e.stopPropagation()
                onTagClick(t)
              }}
              className="text-[10px] px-1.5 py-0.5 rounded bg-sky-50 dark:bg-[#004a77]/30 text-sky-700 dark:text-[#c2e7ff] border border-sky-100 dark:border-[#004a77]/50 hover:bg-sky-100 dark:hover:bg-[#004a77]/50 transition"
              title={`筛选标签 #${t}`}
            >
              #{t}
            </button>
          ))}
        </div>
      )}

      <div className="flex items-center justify-between text-[11px] text-neutral-400 dark:text-[#9aa0a6] mt-auto pt-3 border-t border-neutral-100 dark:border-[#28292a]">
        <div className="flex items-center gap-3">
          <span title="最近更新">{relTime(skill.updated_at)}</span>
          {skill.owner_name ? (
            <span className="truncate max-w-[7rem]" title={`所有者：${skill.owner_name}`}>
              · {skill.owner_name}
            </span>
          ) : null}
          {(skill.enabled_session_count ?? 0) > 0 && (
            <span className="flex items-center gap-0.5">
              · <Users className="w-3 h-3" /> {skill.enabled_session_count}
            </span>
          )}
        </div>
        <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition">
          {isOwner && (
            <IconBtn
              title={skill.confidential ? '取消机密' : '设为机密'}
              onClick={(e) => {
                e.stopPropagation()
                onToggleConfidential()
              }}
            >
              {skill.confidential ? (
                <Unlock className="w-3.5 h-3.5" />
              ) : (
                <Lock className="w-3.5 h-3.5" />
              )}
            </IconBtn>
          )}
          {locked ? (
            <span
              className="p-1 text-neutral-300 dark:text-[#5e6368]"
              title="机密：仅所有者可预览/下载，可直接在会话中使用"
            >
              <Lock className="w-3.5 h-3.5" />
            </span>
          ) : (
            <>
              <IconBtn
                title="在编辑器中打开"
                onClick={(e) => {
                  e.stopPropagation()
                  onOpen('files')
                }}
              >
                <Code2 className="w-3.5 h-3.5" />
              </IconBtn>
              <IconBtn
                title="下载 zip"
                onClick={(e) => {
                  e.stopPropagation()
                  onDownload()
                }}
              >
                <Download className="w-3.5 h-3.5" />
              </IconBtn>
            </>
          )}
          {(isOwner || !skill.confidential) && (
            <IconBtn
              title="删除"
              danger
              onClick={(e) => {
                e.stopPropagation()
                onDelete()
              }}
            >
              <Trash2 className="w-3.5 h-3.5" />
            </IconBtn>
          )}
        </div>
      </div>
    </div>
  )
}

function IconBtn({
  children,
  onClick,
  title,
  danger,
}: {
  children: React.ReactNode
  onClick: (e: React.MouseEvent) => void
  title: string
  danger?: boolean
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      className={cn(
        'p-1 rounded',
        danger
          ? 'text-red-500 hover:bg-red-50 dark:hover:bg-red-950/40'
          : 'text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#28292a]',
      )}
    >
      {children}
    </button>
  )
}

// ============================================================
// 空态
// ============================================================

function EmptyState({
  hasQuery,
  onUpload,
  onNew,
}: {
  hasQuery: boolean
  onUpload: () => void
  onNew: () => void
}) {
  if (hasQuery) {
    return (
      <div className="text-center text-neutral-400 dark:text-[#9aa0a6] py-20 text-sm">
        没有匹配的 skill
      </div>
    )
  }
  return (
    <div className="text-center text-neutral-500 dark:text-[#9aa0a6] py-20">
      <div className="text-lg mb-2 text-[#1f1f1f] dark:text-[#f1f3f4]">还没有任何 skill</div>
      <div className="text-sm text-neutral-400 dark:text-[#747775] mb-6">
        上传一个已有的 zip 包，或从空起手新建一个
      </div>
      <div className="flex items-center justify-center gap-3">
        <button
          onClick={onUpload}
          className="px-4 py-2 text-sm border border-neutral-300 dark:border-[#3c4043] rounded-md hover:bg-neutral-50 dark:hover:bg-[#28292a] text-neutral-700 dark:text-[#c4c7c5]"
        >
          上传 zip
        </button>
        <button
          onClick={onNew}
          className="px-4 py-2 text-sm bg-neutral-900 dark:bg-white text-white dark:text-neutral-900 rounded-md hover:bg-neutral-800 dark:hover:bg-neutral-100 font-medium"
        >
          从空新建
        </button>
      </div>
    </div>
  )
}

// ============================================================
// "从空新建" 弹窗
// ============================================================

function NewSkillModal({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: (skill: Skill) => void
}) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    const trimmed = name.trim()
    if (!trimmed) {
      setError('名字不能为空')
      return
    }
    setError(null)
    setSubmitting(true)
    try {
      const s = await api.createEmptySkill({
        name: trimmed,
        description: description.trim() || null,
      })
      onCreated(s)
    } catch (e) {
      setError(String(e))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <>
      <div className="fixed inset-0 bg-black/40 z-50" onClick={onClose} />
      <div className="fixed inset-0 z-50 flex items-center justify-center p-6 pointer-events-none">
        <div className="bg-white dark:bg-[#1e1f20] border border-transparent dark:border-[#3c4043] w-full max-w-md rounded-xl shadow-2xl pointer-events-auto text-[#1f1f1f] dark:text-[#f1f3f4]">
          <header className="flex items-center justify-between px-5 py-3 border-b border-neutral-200 dark:border-[#3c4043]">
            <div className="font-medium">从空新建 skill</div>
            <button
              className="p-1 hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded text-neutral-500 dark:text-[#9aa0a6]"
              onClick={onClose}
            >
              <X className="w-4 h-4" />
            </button>
          </header>
          <div className="p-5 space-y-4">
            <div>
              <label className="block text-xs text-neutral-600 dark:text-[#c4c7c5] mb-1">名字</label>
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') submit()
                }}
                placeholder="如 my-pdf-tools"
                className="w-full px-3 py-2 border border-neutral-300 dark:border-[#3c4043] rounded-md text-sm bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] focus:outline-none focus:border-neutral-900 dark:focus:border-[#8ab4f8]"
              />
              <p className="text-[11px] text-neutral-400 dark:text-[#9aa0a6] mt-1">
                这个名字会写到 SKILL.md 的 frontmatter 里；不能与已有 skill 重名
              </p>
            </div>
            <div>
              <label className="block text-xs text-neutral-600 dark:text-[#c4c7c5] mb-1">
                描述（选填）
              </label>
              <textarea
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={3}
                placeholder="LLM 会看到这段描述来决定何时调用"
                className="w-full px-3 py-2 border border-neutral-300 dark:border-[#3c4043] rounded-md text-sm bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] resize-none focus:outline-none focus:border-neutral-900 dark:focus:border-[#8ab4f8]"
              />
            </div>
            {error && (
              <div className="px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
                {error}
              </div>
            )}
            <p className="text-xs text-neutral-500 dark:text-[#9aa0a6]">
              新建后会自动写一份最小 SKILL.md 并发布 v1，随后直接打开「文件」tab 让你起手。
            </p>
          </div>
          <footer className="flex items-center justify-end gap-2 px-5 py-3 border-t border-neutral-200 dark:border-[#3c4043]">
            <button
              onClick={onClose}
              className="px-3 py-1.5 text-sm rounded-md text-neutral-700 dark:text-[#c4c7c5] hover:bg-neutral-100 dark:hover:bg-[#28292a]"
            >
              取消
            </button>
            <button
              onClick={submit}
              disabled={submitting}
              className="px-3 py-1.5 bg-neutral-900 dark:bg-[#1a73e8] text-white rounded-md text-sm flex items-center gap-1 disabled:opacity-50 hover:bg-neutral-800 dark:hover:bg-[#1557b0]"
            >
              {submitting && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
              创建
            </button>
          </footer>
        </div>
      </div>
    </>
  )
}
