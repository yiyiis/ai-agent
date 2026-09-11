import { type ReactNode, useEffect, useState } from 'react'
import {
  Activity,
  AlertTriangle,
  Cpu,
  FileCode,
  GitBranch,
  KeyRound,
  Plus,
  Settings,
  Trash2,
  X,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import { api } from '../api/client'
import type { Model, Skill, SkillBody } from '../types'
import { cn } from '../lib/utils'
import { SkillFileEditor } from './SkillFileEditor'
import { SkillRunsPanel } from './SkillRunsPanel'
import { SkillVersionsPanel } from './SkillVersionsPanel'

export type SkillTab =
  | 'doc'
  | 'files'
  | 'versions'
  | 'secrets'
  | 'runs'
  | 'settings'

interface Props {
  skill: Skill
  onClose: () => void
  /** 默认打开哪个 tab；广场点卡片进来时传 'files' 直奔编辑 */
  initialTab?: SkillTab
}

/**
 * Skill 详情大弹窗。被两个地方使用：
 * 1. 抽屉里 Eye 按钮（默认 doc tab，老用法）
 * 2. 广场点卡片（默认 files tab，编辑友好）
 */
export function SkillDetailModal({ skill, onClose, initialTab = 'doc' }: Props) {
  const [body, setBody] = useState<SkillBody | null>(null)
  const [secrets, setSecrets] = useState<{ key: string; value: string }[]>([])
  const [savingSecrets, setSavingSecrets] = useState(false)
  const [secretsErr, setSecretsErr] = useState<string | null>(null)
  const [secretsMsg, setSecretsMsg] = useState<string | null>(null)
  const [tab, setTab] = useState<SkillTab>(initialTab)
  const [filesDirty, setFilesDirty] = useState(false)
  const [fileEditorKey, setFileEditorKey] = useState(0)
  // 「设置」tab：API 跑 LLM 模式时用哪个模型
  const [models, setModels] = useState<Model[]>([])
  const [skillModel, setSkillModel] = useState<string>(skill.model ?? '')
  const [savingModel, setSavingModel] = useState(false)
  const [modelErr, setModelErr] = useState<string | null>(null)
  const [modelMsg, setModelMsg] = useState<string | null>(null)
  // 标签
  const [tags, setTags] = useState<string[]>(skill.tags ?? [])
  const [tagInput, setTagInput] = useState('')

  const guardedClose = () => {
    if (filesDirty && !confirm('有未保存的文件修改，确定关闭吗？')) return
    onClose()
  }

  useEffect(() => {
    Promise.all([
      api.getSkillBody(skill.id),
      api.getSkillSecrets(skill.id),
    ]).then(([b, kv]) => {
      setBody(b)
      setSecrets(Object.entries(kv).map(([k, v]) => ({ key: k, value: v })))
    })
    setSkillModel(skill.model ?? '')
    setTags(skill.tags ?? [])
  }, [skill.id])

  const saveTags = async (next: string[]) => {
    const prev = tags
    setTags(next)
    setModelErr(null)
    try {
      const updated = await api.updateSkill(skill.id, { tags: next })
      // 后端会去重/截断/限量，以它返回的为准，免得界面和库里不一致
      setTags(updated.tags ?? [])
    } catch (e) {
      setTags(prev)
      setModelErr(String(e))
    }
  }

  const addTag = () => {
    const t = tagInput.trim()
    if (!t || tags.includes(t)) {
      setTagInput('')
      return
    }
    setTagInput('')
    saveTags([...tags, t])
  }

  useEffect(() => {
    api.listModels().then(setModels).catch(() => setModels([]))
  }, [])

  const saveModel = async (next: string) => {
    setModelErr(null)
    setModelMsg(null)
    setSavingModel(true)
    const prev = skillModel
    setSkillModel(next)
    try {
      await api.updateSkill(skill.id, { model: next })
      setModelMsg(next ? '已保存' : '已清空，跟随默认')
      setTimeout(() => setModelMsg(null), 1800)
    } catch (e) {
      setSkillModel(prev)
      setModelErr(String(e))
    } finally {
      setSavingModel(false)
    }
  }

  const addRow = () =>
    setSecrets((rows) => [...rows, { key: '', value: '' }])

  const updateRow = (i: number, patch: Partial<{ key: string; value: string }>) =>
    setSecrets((rows) => rows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))

  const deleteRow = (i: number) =>
    setSecrets((rows) => rows.filter((_, idx) => idx !== i))

  const saveSecrets = async () => {
    setSecretsErr(null)
    setSecretsMsg(null)
    setSavingSecrets(true)
    try {
      const payload: Record<string, string> = {}
      for (const r of secrets) {
        if (!r.key.trim()) continue
        payload[r.key.trim()] = r.value
      }
      await api.updateSkillSecrets(skill.id, payload)
      const [b, kv] = await Promise.all([
        api.getSkillBody(skill.id),
        api.getSkillSecrets(skill.id),
      ])
      setBody(b)
      setSecrets(Object.entries(kv).map(([k, v]) => ({ key: k, value: v })))
      setSecretsMsg('已保存')
      setTimeout(() => setSecretsMsg(null), 1800)
    } catch (e) {
      setSecretsErr(String(e))
    } finally {
      setSavingSecrets(false)
    }
  }

  const tabBtn = (
    key: SkillTab,
    label: ReactNode,
  ) => (
    <button
      key={key}
      onClick={() => setTab(key)}
      className={cn(
        'px-3 py-1.5 text-sm border-b-2 -mb-px flex items-center gap-1 transition-colors',
        tab === key
          ? 'border-neutral-900 dark:border-[#8ab4f8] text-[#1f1f1f] dark:text-[#f1f3f4] font-medium'
          : 'border-transparent text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-800 dark:hover:text-[#f1f3f4]',
      )}
    >
      {label}
    </button>
  )

  return (
    <>
      <div className="fixed inset-0 bg-black/40 z-50" onClick={guardedClose} />
      <div className="fixed inset-0 z-50 flex items-center justify-center p-6 pointer-events-none">
        <div className="bg-white dark:bg-[#1e1f20] border border-transparent dark:border-[#3c4043] w-full max-w-5xl h-[85vh] rounded-xl shadow-2xl flex flex-col pointer-events-auto text-[#1f1f1f] dark:text-[#f1f3f4]">
          <header className="flex items-center justify-between px-5 py-3 border-b border-neutral-200 dark:border-[#3c4043]">
            <div>
              <div className="font-medium text-[#1f1f1f] dark:text-[#f1f3f4]">{skill.name}</div>
              {skill.description && (
                <div className="text-xs text-neutral-500 dark:text-[#9aa0a6] mt-0.5">
                  {skill.description}
                </div>
              )}
            </div>
            <button
              className="p-1 hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded text-neutral-500 dark:text-[#9aa0a6]"
              onClick={guardedClose}
            >
              <X className="w-4 h-4" />
            </button>
          </header>

          <div className="flex gap-2 px-5 pt-3 border-b border-neutral-100 dark:border-[#3c4043]">
            {tabBtn('doc', 'SKILL.md')}
            {tabBtn(
              'files',
              <>
                <FileCode className="w-3 h-3" />
                文件 {filesDirty && <span className="text-orange-500">●</span>}
              </>,
            )}
            {tabBtn(
              'versions',
              <>
                <GitBranch className="w-3 h-3" />
                版本
              </>,
            )}
            {tabBtn(
              'secrets',
              <>
                <KeyRound className="w-3 h-3" />
                机密 {body?.has_secrets && `(${body.secret_keys.length})`}
              </>,
            )}
            {/* 只有能被 API 调用的 skill 才有调用历史，否则这个 tab 永远是空的 */}
            {body?.api_enabled && (
              tabBtn(
                'runs',
                <>
                  <Activity className="w-3 h-3" />
                  调用
                </>,
              )
            )}
            {tabBtn(
              'settings',
              <>
                <Settings className="w-3 h-3" />
                设置
              </>,
            )}
          </div>

          <div
            className={cn(
              'flex-1 min-h-0',
              tab === 'files'
                ? 'p-5 flex overflow-hidden'
                : 'overflow-y-auto scrollbar-thin p-5',
            )}
          >
            {tab === 'doc' &&
              (body ? (
                <div className="markdown-body text-sm">
                  <ReactMarkdown
                    remarkPlugins={[remarkGfm]}
                    rehypePlugins={[rehypeHighlight]}
                  >
                    {body.body || '_（SKILL.md 正文为空）_'}
                  </ReactMarkdown>
                </div>
              ) : (
                <div className="text-sm text-neutral-400 dark:text-[#9aa0a6]">加载中…</div>
              ))}

            {tab === 'files' && (
              <SkillFileEditor
                key={fileEditorKey}
                skillId={skill.id}
                onDirtyChange={setFilesDirty}
              />
            )}

            {tab === 'versions' && (
              <SkillVersionsPanel
                skill={skill}
                onRestored={() => {
                  setFileEditorKey((k) => k + 1)
                  setFilesDirty(false)
                  setTab('files')
                }}
              />
            )}

            {tab === 'secrets' && (
              <div className="space-y-3">
                <p className="text-xs text-neutral-500 dark:text-[#9aa0a6]">
                  这些键值会作为环境变量注入沙箱，可在 bash 用 $KEY、在
                  python_exec 用 os.environ[KEY] 读取。删行后保存即移除该项。
                </p>
                {secrets.length === 0 && (
                  <div className="text-sm text-neutral-400 dark:text-[#747775] text-center py-6">
                    还没有机密项
                  </div>
                )}
                {secrets.map((r, i) => (
                  <div key={i} className="flex gap-2">
                    <input
                      value={r.key}
                      onChange={(e) => updateRow(i, { key: e.target.value })}
                      placeholder="KEY"
                      className="flex-1 px-2 py-1.5 border border-neutral-300 dark:border-[#3c4043] rounded text-sm font-mono bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4]"
                    />
                    <input
                      value={r.value}
                      onChange={(e) => updateRow(i, { value: e.target.value })}
                      placeholder="value"
                      type="password"
                      className="flex-1 px-2 py-1.5 border border-neutral-300 dark:border-[#3c4043] rounded text-sm font-mono bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4]"
                    />
                    <button
                      onClick={() => deleteRow(i)}
                      className="p-1.5 text-red-500 hover:bg-red-50 dark:hover:bg-red-950/40 rounded"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                ))}
                <button
                  onClick={addRow}
                  className="flex items-center gap-1 text-xs text-neutral-600 dark:text-[#c4c7c5] hover:text-neutral-900 dark:hover:text-[#f1f3f4]"
                >
                  <Plus className="w-3.5 h-3.5" />
                  添加一项
                </button>

                {secretsErr && (
                  <div className="px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
                    {secretsErr}
                  </div>
                )}
                <div className="flex items-center justify-end gap-2 pt-2 border-t border-neutral-100 dark:border-[#3c4043]">
                  {secretsMsg && (
                    <span className="text-xs text-green-600 dark:text-green-400">{secretsMsg}</span>
                  )}
                  <button
                    onClick={saveSecrets}
                    disabled={savingSecrets}
                    className="px-3 py-1.5 bg-neutral-900 dark:bg-[#1a73e8] text-white rounded text-sm hover:bg-neutral-800 dark:hover:bg-[#1557b0] disabled:opacity-50"
                  >
                    {savingSecrets ? '保存中…' : '保存机密'}
                  </button>
                </div>
              </div>
            )}

            {tab === 'runs' && <SkillRunsPanel skillId={skill.id} />}

            {tab === 'settings' && (
              <div className="space-y-6 max-w-2xl">
                {/* manifest 解析失败要放在最顶上：这意味着该 skill 已从
                    GET /api/v1/skills 消失、所有外部调用都在 400 */}
                {body?.manifest_error && (
                  <div className="px-3 py-2.5 bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900 rounded">
                    <div className="flex items-center gap-1.5 text-sm font-medium text-red-700 dark:text-red-400">
                      <AlertTriangle className="w-3.5 h-3.5" />
                      manifest.json 解析失败
                    </div>
                    <p className="text-xs text-red-600 dark:text-red-300 mt-1 leading-relaxed">
                      这个 skill 目前<b>不会</b>出现在 <code>GET /api/v1/skills</code>{' '}
                      里，所有外部调用都会 400。修好后重新发布版本即可恢复。
                    </p>
                    <pre className="mt-2 px-2 py-1.5 bg-white/70 dark:bg-[#1e1f20]/70 border border-red-100 dark:border-red-900 rounded text-[11px] text-red-700 dark:text-red-300 whitespace-pre-wrap break-all">
                      {body.manifest_error}
                    </pre>
                  </div>
                )}

                <div>
                  <div className="text-sm font-medium">分类标签</div>
                  <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] mt-1">
                    用于在广场里筛选。上传时会自动读取 SKILL.md frontmatter 里的{' '}
                    <code>tags:</code> 作为初始值，之后以这里为准。
                  </p>
                  <div className="flex items-center gap-1.5 flex-wrap mt-2">
                    {tags.map((t) => (
                      <span
                        key={t}
                        className="inline-flex items-center gap-1 text-xs px-2 py-1 rounded bg-sky-50 dark:bg-[#004a77]/30 text-sky-700 dark:text-[#c2e7ff] border border-sky-100 dark:border-[#004a77]/50"
                      >
                        #{t}
                        <button
                          onClick={() => saveTags(tags.filter((x) => x !== t))}
                          className="hover:text-sky-900 dark:hover:text-sky-200"
                          title="移除"
                        >
                          <X className="w-3 h-3" />
                        </button>
                      </span>
                    ))}
                    <input
                      value={tagInput}
                      onChange={(e) => setTagInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault()
                          addTag()
                        }
                      }}
                      onBlur={addTag}
                      placeholder="+ 添加标签，回车确认"
                      className="px-2 py-1 border border-neutral-300 dark:border-[#3c4043] rounded text-xs w-40 bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] focus:outline-none focus:border-neutral-900 dark:focus:border-[#8ab4f8]"
                    />
                  </div>
                </div>

                <div className="border-t border-neutral-100 dark:border-[#3c4043]" />

                <div>
                  <div className="flex items-center gap-1.5 text-sm font-medium">
                    <Cpu className="w-3.5 h-3.5 text-neutral-500 dark:text-[#9aa0a6]" />
                    API 调用时使用的模型
                  </div>
                  <p className="text-xs text-neutral-500 dark:text-[#9aa0a6] mt-1 leading-relaxed">
                    外部接口以 LLM 模式跑这个 skill 时用哪个模型。不选则跟随
                    manifest.json 里的声明，都没有就用后端默认模型。
                    <br />
                    只影响 API 调用；网页聊天里用哪个模型由会话顶部的选择器决定。
                  </p>
                </div>

                <select
                  value={skillModel}
                  disabled={savingModel}
                  onChange={(e) => saveModel(e.target.value)}
                  className="w-full px-3 py-2 border border-neutral-300 dark:border-[#3c4043] rounded text-sm bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] disabled:opacity-50"
                >
                  <option value="">跟随默认（不指定）</option>
                  {models.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name} — {m.id}
                    </option>
                  ))}
                </select>

                {/* 上下文提示：设置了但不生效是最容易困惑的情况，提前说清楚。
                    manifest 坏掉的情况已经在顶部单独报了，这里不重复。 */}
                {body && !body.api_enabled && !body.manifest_error && (
                  <div className="px-3 py-2 bg-amber-50 dark:bg-amber-950/30 border border-amber-100 dark:border-amber-900 text-amber-700 dark:text-amber-400 text-xs rounded leading-relaxed">
                    {body.manifest_present
                      ? '这个 skill 的 manifest.json 里 api_enabled 不为 true，还不能被外部接口调用，这里的设置暂时不会生效。'
                      : '这个 skill 还没有 manifest.json，不能被外部接口调用，这里的设置暂时不会生效。补好 manifest 并发布新版本后即可。'}
                  </div>
                )}
                {body && body.api_enabled && !body.llm_enabled && (
                  <div className="px-3 py-2 bg-neutral-50 dark:bg-[#28292a] border border-neutral-200 dark:border-[#3c4043] text-neutral-600 dark:text-[#c4c7c5] text-xs rounded leading-relaxed">
                    这个 skill 的 manifest 里 <code>llm_enabled</code> 为 false，
                    API 调用走的是直接执行脚本的方式，不经过 LLM——
                    这里的设置只在 <code>mode=llm</code> 时才起作用。
                  </div>
                )}
                {body?.manifest_model && (
                  <div className="px-3 py-2 bg-neutral-50 dark:bg-[#28292a] border border-neutral-200 dark:border-[#3c4043] text-neutral-600 dark:text-[#c4c7c5] text-xs rounded leading-relaxed">
                    manifest.json 里声明了{' '}
                    <code className="font-mono">{body.manifest_model}</code>
                    {skillModel
                      ? ' — 当前被上面的设置覆盖。'
                      : ' — 未在上面指定时按它执行。'}
                  </div>
                )}

                {modelErr && (
                  <div className="px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
                    {modelErr}
                  </div>
                )}
                {modelMsg && (
                  <div className="text-xs text-green-600 dark:text-green-400">{modelMsg}</div>
                )}
              </div>
            )}
          </div>
        </div>
      </div>
    </>
  )
}
