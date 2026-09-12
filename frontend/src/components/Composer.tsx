import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowUp,
  FileText,
  Image as ImageIcon,
  Loader2,
  Plus,
  Send,
  Square,
  Wrench,
  X,
} from 'lucide-react'
import { api } from '../api/client'
import type { Attachment, Model, Skill } from '../types'
import { ModelPicker } from './ModelPicker'

interface Props {
  onSend: (text: string, attachments: Attachment[]) => void
  disabled?: boolean
  skills: Skill[]
  editingHint?: string | null
  onCancelEdit?: () => void
  initialValue?: string
  /** 与 initialValue 配套：恢复输入时一并带回附件（停止生成/悬空轮还原） */
  initialAttachments?: Attachment[] | null
  models?: Model[]
  currentModel?: string
  onModelChange?: (modelId: string) => void
  placeholder?: string
  streaming?: boolean
  onStop?: () => void
}

interface PendingUpload {
  id: string
  file: File
  status: 'uploading' | 'error'
  error?: string
}

const ICON_BY_TYPE = (ct?: string | null, name?: string) => {
  const lower = (ct || '').toLowerCase()
  const ext = (name || '').toLowerCase().split('.').pop() || ''
  if (lower.startsWith('image/') || ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg'].includes(ext)) {
    return <ImageIcon className="w-3.5 h-3.5" />
  }
  return <FileText className="w-3.5 h-3.5" />
}

const fmtSize = (n?: number | null) => {
  if (!n) return ''
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / 1024 / 1024).toFixed(1)}MB`
}

export function Composer({
  onSend,
  disabled,
  skills,
  editingHint,
  onCancelEdit,
  initialValue,
  initialAttachments,
  models = [],
  currentModel = '',
  onModelChange,
  placeholder = '输入消息，随时开始...',
  streaming = false,
  onStop,
}: Props) {
  const [value, setValue] = useState(initialValue ?? '')
  const [selIdx, setSelIdx] = useState(0)
  const [pickedSkill, setPickedSkill] = useState<Skill | null>(null)
  const [attachments, setAttachments] = useState<Attachment[]>(initialAttachments ?? [])
  const [pending, setPending] = useState<PendingUpload[]>([])
  const ref = useRef<HTMLTextAreaElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  // 外部注入恢复内容时同步输入框（停止生成还原提问 / 悬空轮还原）
  useEffect(() => {
    if (initialValue !== undefined) {
      setValue(initialValue)
      setPickedSkill(null)
      setAttachments(initialAttachments ?? [])
      setPending([])
      requestAnimationFrame(() => {
        const el = ref.current
        if (el) {
          el.focus()
          el.setSelectionRange(initialValue.length, initialValue.length)
        }
      })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialValue])

  // 当组件启用（disabled 变为 false）且有恢复内容时，保证光标聚焦在文本末尾
  useEffect(() => {
    if (!disabled && initialValue !== undefined && ref.current) {
      requestAnimationFrame(() => {
        ref.current?.focus()
        ref.current?.setSelectionRange(ref.current.value.length, ref.current.value.length)
      })
    }
  }, [disabled, initialValue])

  const slashMatch = /^\/([^\s]*)$/.exec(value)
  const menuOpen = slashMatch !== null
  const query = slashMatch?.[1]?.toLowerCase() ?? ''

  const filtered = useMemo(
    () =>
      skills.filter(
        (s) =>
          !query ||
          s.name.toLowerCase().includes(query) ||
          (s.description?.toLowerCase().includes(query) ?? false),
      ),
    [skills, query],
  )

  useEffect(() => {
    setSelIdx(0)
  }, [query, menuOpen])

  useEffect(() => {
    if (ref.current) {
      ref.current.style.height = 'auto'
      ref.current.style.height = Math.min(ref.current.scrollHeight, 180) + 'px'
    }
  }, [value])

  const hasContent = value.trim().length > 0 || pickedSkill || attachments.length > 0
  const uploading = pending.some((p) => p.status === 'uploading')

  const submit = () => {
    if (disabled || !hasContent || uploading) return
    const text = value.trim()
    const payload = pickedSkill
      ? text
        ? `使用 ${pickedSkill.name} 技能：${text}`
        : `使用 ${pickedSkill.name} 技能`
      : text
    onSend(payload, attachments)
    setValue('')
    setPickedSkill(null)
    setAttachments([])
    setPending([])
  }

  const pickSkill = (s: Skill) => {
    setPickedSkill(s)
    setValue('')
    requestAnimationFrame(() => ref.current?.focus())
  }

  const startUpload = async (files: FileList) => {
    const entries: PendingUpload[] = Array.from(files).map((f) => ({
      id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      file: f,
      status: 'uploading',
    }))
    setPending((p) => [...p, ...entries])
    await Promise.all(
      entries.map(async (entry) => {
        try {
          const att = await api.uploadAttachment(entry.file)
          setAttachments((prev) => [...prev, att])
          setPending((prev) => prev.filter((p) => p.id !== entry.id))
        } catch (e) {
          setPending((prev) =>
            prev.map((p) =>
              p.id === entry.id
                ? { ...p, status: 'error', error: String(e) }
                : p,
            ),
          )
        }
      }),
    )
  }

  const onFileInput = (e: React.ChangeEvent<HTMLInputElement>) => {
    const fs = e.target.files
    if (fs && fs.length) startUpload(fs)
    e.target.value = ''
  }

  const removeAttachment = (i: number) =>
    setAttachments((prev) => prev.filter((_, idx) => idx !== i))

  const removePending = (id: string) =>
    setPending((prev) => prev.filter((p) => p.id !== id))

  const handleKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return
    if (menuOpen && filtered.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setSelIdx((i) => (i + 1) % filtered.length)
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setSelIdx((i) => (i - 1 + filtered.length) % filtered.length)
        return
      }
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault()
        pickSkill(filtered[selIdx])
        return
      }
      if (e.key === 'Tab') {
        e.preventDefault()
        pickSkill(filtered[selIdx])
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setValue('')
        return
      }
    }
    const shortcut = localStorage.getItem('ai_agent_send_shortcut') || 'enter'
    if (shortcut === 'ctrl_enter') {
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault()
        submit()
        return
      }
    } else {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault()
        submit()
        return
      }
    }
    if (
      e.key === 'Backspace' &&
      pickedSkill &&
      value.length === 0 &&
      ref.current?.selectionStart === 0
    ) {
      e.preventDefault()
      setPickedSkill(null)
    }
  }

  const hasAttachmentRow = attachments.length > 0 || pending.length > 0

  return (
    <div className="w-full relative font-sans">
      {editingHint && (
        <div className="mb-2.5 flex items-center justify-between px-4 py-2 bg-[#f0f4f9] dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] rounded-full text-xs text-[#1f1f1f] dark:text-[#f1f3f4] shadow-sm">
          <span>{editingHint}</span>
          {onCancelEdit && (
            <button
              onClick={onCancelEdit}
              className="p-1 hover:bg-[#e1e7f0] dark:hover:bg-[#28292a] rounded-full transition-colors"
              title="取消编辑"
            >
              <X className="w-3.5 h-3.5 text-[#747775] dark:text-[#9aa0a6]" />
            </button>
          )}
        </div>
      )}

      {/* / 快捷触发技能浮层 */}
      {menuOpen && (
        <div className="absolute bottom-full left-0 right-0 mb-3 bg-white dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] rounded-3xl shadow-xl overflow-hidden z-30 animate-slide-up">
          <div className="px-4 py-2.5 bg-[#f0f4f9] dark:bg-[#28292a] border-b border-[#e3e3e3] dark:border-[#3c4043] text-[11px] text-[#444746] dark:text-[#c4c7c5] flex items-center justify-between font-medium">
            <span>调用 Skills 技能</span>
            <span className="text-[10px] text-[#747775] dark:text-[#9aa0a6]">↑↓ 选择 · Enter 确认</span>
          </div>
          {filtered.length === 0 ? (
            <div className="px-4 py-6 text-center text-xs text-[#747775] dark:text-[#9aa0a6]">
              {skills.length === 0
                ? '当前没有启用的技能'
                : '没有匹配的技能'}
            </div>
          ) : (
            <div className="max-h-60 overflow-y-auto scrollbar-thin p-1.5 space-y-0.5">
              {filtered.map((s, i) => (
                <button
                  key={s.id}
                  type="button"
                  onMouseDown={(e) => {
                    e.preventDefault()
                    pickSkill(s)
                  }}
                  onMouseEnter={() => setSelIdx(i)}
                  className={
                    'w-full text-left px-3.5 py-2 rounded-2xl flex items-center justify-between gap-3 transition-colors ' +
                    (i === selIdx
                      ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-medium'
                      : 'hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4]')
                  }
                >
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-xs font-semibold text-[#1a73e8] dark:text-[#8ab4f8] font-mono">
                      /{s.name}
                    </span>
                    {s.description && (
                      <span className="text-xs text-[#747775] dark:text-[#9aa0a6] truncate">
                        {s.description}
                      </span>
                    )}
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Gemini 椭圆药丸输入容器 */}
      <div
        className={
          'relative flex flex-col bg-white dark:bg-[#1e1f20] border rounded-[28px] transition-all duration-200 ' +
          'border-[#e3e3e3] dark:border-[#3c4043] shadow-gemini-pill hover:shadow-gemini-hover focus-within:shadow-gemini-hover focus-within:border-[#b4d7fe] dark:focus-within:border-[#1a73e8] p-1.5'
        }
      >
        {/* 附件行 */}
        {hasAttachmentRow && (
          <div className="flex flex-wrap gap-2 px-3 pt-2 pb-1">
            {attachments.map((a, i) => (
              <span
                key={a.url}
                className="inline-flex items-center gap-1.5 pl-3 pr-2 py-1 rounded-full bg-[#f0f4f9] dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#e3e3e3] border border-[#e3e3e3] dark:border-[#3c4043] text-xs max-w-[240px]"
                title={a.filename}
              >
                <span className="text-[#1a73e8] dark:text-[#8ab4f8]">
                  {ICON_BY_TYPE(a.content_type, a.filename)}
                </span>
                <span className="truncate">{a.filename}</span>
                {a.size != null && (
                  <span className="text-[#747775] dark:text-[#9aa0a6] shrink-0">{fmtSize(a.size)}</span>
                )}
                <button
                  type="button"
                  onClick={() => removeAttachment(i)}
                  className="p-0.5 hover:bg-[#e1e7f0] dark:hover:bg-[#333537] rounded-full text-[#747775] hover:text-[#1f1f1f] dark:text-[#9aa0a6] dark:hover:text-[#f1f3f4]"
                  title="移除"
                >
                  <X className="w-3 h-3" />
                </button>
              </span>
            ))}
            {pending.map((p) => (
              <span
                key={p.id}
                className={
                  'inline-flex items-center gap-1.5 pl-3 pr-2 py-1 rounded-full text-xs max-w-[240px] border ' +
                  (p.status === 'uploading'
                    ? 'bg-[#f0f4f9] dark:bg-[#28292a] text-[#444746] dark:text-[#c4c7c5] border-[#e3e3e3] dark:border-[#3c4043]'
                    : 'bg-rose-50 dark:bg-rose-950/40 text-rose-600 dark:text-rose-400 border-rose-200 dark:border-rose-900')
                }
              >
                {p.status === 'uploading' ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin text-[#1a73e8] dark:text-[#8ab4f8]" />
                ) : (
                  ICON_BY_TYPE(p.file.type, p.file.name)
                )}
                <span className="truncate">{p.file.name}</span>
                <button
                  type="button"
                  onClick={() => removePending(p.id)}
                  className="p-0.5 hover:bg-black/5 dark:hover:bg-white/10 rounded-full"
                >
                  <X className="w-3 h-3" />
                </button>
              </span>
            ))}
          </div>
        )}

        <div className="flex items-center gap-1 min-h-[46px] px-1">
          {/* 左侧 + 按钮 */}
          <input
            ref={fileRef}
            type="file"
            multiple
            className="hidden"
            onChange={onFileInput}
          />
          <button
            type="button"
            onClick={() => fileRef.current?.click()}
            disabled={disabled}
            className="p-2 text-[#444746] dark:text-[#c4c7c5] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded-full transition-colors disabled:opacity-40 shrink-0"
            title="添加图片或文件"
          >
            <Plus className="w-5 h-5" />
          </button>

          {/* 选中的 Skill 胶囊 */}
          {pickedSkill && (
            <span
              className="inline-flex items-center gap-1 px-2.5 py-1 rounded-full bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] text-xs font-medium shrink-0"
              title={pickedSkill.description ?? ''}
            >
              <Wrench className="w-3 h-3" />
              <span>{pickedSkill.name}</span>
              <button
                type="button"
                onClick={() => setPickedSkill(null)}
                className="p-0.5 hover:bg-black/10 rounded-full text-[#041e49] dark:text-[#c2e7ff]"
                title="取消技能"
              >
                <X className="w-3 h-3" />
              </button>
            </span>
          )}

          {/* 输入框正文 */}
          <textarea
            ref={ref}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={handleKey}
            rows={1}
            placeholder={placeholder}
            disabled={disabled}
            className="flex-1 min-w-[120px] resize-none px-2 py-1 outline-none text-[15px] leading-relaxed bg-transparent text-[#1f1f1f] dark:text-[#f1f3f4] placeholder:text-[#747775] dark:placeholder:text-[#9aa0a6]"
          />

          {/* 右侧模型选择药丸（如 Flash ▾） */}
          {models.length > 0 && onModelChange && (
            <div className="shrink-0">
              <ModelPicker
                models={models}
                value={currentModel}
                onChange={onModelChange}
                disabled={disabled}
                variant="pill"
              />
            </div>
          )}

          {/* 发送 / 停止 按钮 */}
          {streaming ? (
            <button
              type="button"
              onClick={onStop}
              className="p-2.5 rounded-full bg-[#1f1f1f] dark:bg-[#28292a] hover:bg-[#37393b] dark:hover:bg-[#3c4043] text-white flex items-center justify-center shrink-0 shadow-sm transition-all duration-150 active:scale-95 group"
              title="停止生成"
            >
              <Square className="w-3.5 h-3.5 fill-current rounded-[2px] transition-transform group-hover:scale-110" />
            </button>
          ) : (
            <button
              type="button"
              onClick={submit}
              disabled={!hasContent || disabled || uploading}
              className={
                'p-2.5 rounded-full transition-all duration-150 flex items-center justify-center shrink-0 ' +
                (hasContent && !disabled && !uploading
                  ? 'bg-[#1a73e8] text-white hover:bg-[#1557b0] shadow-sm'
                  : 'text-[#747775] dark:text-[#9aa0a6] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] disabled:opacity-40')
              }
              title={uploading ? '上传中…' : '发送'}
            >
              {hasContent ? <ArrowUp className="w-4 h-4 stroke-[2.5]" /> : <Send className="w-4 h-4" />}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
