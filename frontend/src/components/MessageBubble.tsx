import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import {
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  FileText,
  Image as ImageIcon,
  Pencil,
  RotateCcw,
} from 'lucide-react'
import { useState } from 'react'
import type { Attachment } from '../types'
import { cn } from '../lib/utils'
import { isPreviewable } from './FilePreviewModal'
import { BrandIcon } from './BrandIcons'

interface Props {
  role: 'user' | 'assistant'
  content: string
  reasoning?: string | null
  attachments?: Attachment[] | null
  streaming?: boolean
  onRegenerate?: () => void
  onEdit?: () => void
  onPreview?: (a: Attachment) => void
}

function fmtSize(n?: number | null) {
  if (!n) return ''
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / 1024 / 1024).toFixed(1)}MB`
}

function isImage(a: Attachment) {
  const ct = (a.content_type || '').toLowerCase()
  if (ct.startsWith('image/')) return true
  const ext = a.filename.toLowerCase().split('.').pop() || ''
  return ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg'].includes(ext)
}

function AttachmentList({
  items,
  align,
  onPreview,
}: {
  items: Attachment[]
  align: 'start' | 'end'
  onPreview?: (a: Attachment) => void
}) {
  const renderItem = (a: Attachment) => {
    const previewable = !!onPreview && isPreviewable(a)
    if (isImage(a)) {
      const img = (
        <img
          src={a.url}
          alt={a.filename}
          className="max-h-40 max-w-[220px] object-cover rounded-xl"
        />
      )
      const cls =
        'block rounded-2xl overflow-hidden border border-[#e3e3e3] hover:border-[#1a73e8] transition-all'
      return previewable ? (
        <button
          key={a.url}
          type="button"
          onClick={() => onPreview!(a)}
          className={cls}
          title={a.filename}
        >
          {img}
        </button>
      ) : (
        <a
          key={a.url}
          href={a.url}
          target="_blank"
          rel="noreferrer"
          className={cls}
          title={a.filename}
        >
          {img}
        </a>
      )
    }
    const inner = (
      <>
        {a.content_type?.startsWith('image/') ? (
          <ImageIcon className="w-3.5 h-3.5 text-[#1a73e8] shrink-0" />
        ) : (
          <FileText className="w-3.5 h-3.5 text-[#1a73e8] shrink-0" />
        )}
        <span className="truncate">{a.filename}</span>
        {a.size != null && (
          <span className="text-[#747775] shrink-0">{fmtSize(a.size)}</span>
        )}
      </>
    )
    const cls =
      'inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-[#f0f4f9] dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] text-[#1f1f1f] dark:text-[#f1f3f4] text-xs max-w-[260px] hover:bg-[#e1e7f0] dark:hover:bg-[#333537] transition-colors'
    return previewable ? (
      <button
        key={a.url}
        type="button"
        onClick={() => onPreview!(a)}
        className={cls}
        title={`预览 ${a.filename}`}
      >
        {inner}
      </button>
    ) : (
      <a
        key={a.url}
        href={a.url}
        target="_blank"
        rel="noreferrer"
        className={cls}
        title={a.filename}
      >
        {inner}
      </a>
    )
  }
  return (
    <div
      className={cn(
        'flex flex-wrap gap-2 mb-2',
        align === 'end' ? 'justify-end' : 'justify-start',
      )}
    >
      {items.map(renderItem)}
    </div>
  )
}

export function MessageBubble({
  role,
  content,
  reasoning,
  attachments,
  streaming,
  onRegenerate,
  onEdit,
  onPreview,
}: Props) {
  const [copied, setCopied] = useState(false)
  const [reasoningOpen, setReasoningOpen] = useState(false)
  const isUser = role === 'user'

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(content)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // ignore
    }
  }

  // 用户消息：Gemini 圆角气泡（支持深色模式）
  if (isUser) {
    return (
      <div className="group flex flex-col items-end gap-1.5 max-w-[82%] ml-auto font-sans">
        {attachments && attachments.length > 0 && (
          <AttachmentList items={attachments} align="end" onPreview={onPreview} />
        )}
        {content && (
          <div className="px-5 py-3 rounded-[22px] rounded-br-[6px] bg-[#f0f4f9] dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#f1f3f4] text-[15px] leading-relaxed whitespace-pre-wrap select-text">
            {content}
            {streaming && (
              <span className="inline-block w-1.5 h-4 ml-1 bg-[#1a73e8] dark:bg-[#8ab4f8] animate-pulse align-middle rounded-sm" />
            )}
          </div>
        )}
        {!streaming && content && (
          <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
            <button
              onClick={copy}
              className="p-1.5 text-[#747775] dark:text-[#9aa0a6] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded-full transition-colors"
              title="复制"
            >
              {copied ? <Check className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
            </button>
            {onEdit && (
              <button
                onClick={onEdit}
                className="p-1.5 text-[#747775] dark:text-[#9aa0a6] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded-full transition-colors"
                title="编辑"
              >
                <Pencil className="w-3.5 h-3.5" />
              </button>
            )}
          </div>
        )}
      </div>
    )
  }

  // 助手消息：标志性透气布局 + 专属品牌徽标（支持深色模式）
  return (
    <div className="group flex gap-4 w-full font-sans animate-fade-in">
      <div className="shrink-0 pt-0.5">
        <BrandIcon className="w-6 h-6" />
      </div>
      <div className="flex-1 min-w-0 flex flex-col">
        {reasoning && (
          <div className="mb-3 rounded-xl border border-[#e3e3e3] dark:border-[#3c4043] bg-[#f8f9fa] dark:bg-[#1e1f20] overflow-hidden">
            <button
              onClick={() => setReasoningOpen((o) => !o)}
              className="flex w-full items-center gap-2 px-3 py-2 text-xs text-[#747775] dark:text-[#9aa0a6] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] transition-colors"
            >
              <Brain className="w-3.5 h-3.5" />
              <span className="font-medium">思考过程</span>
              {reasoningOpen ? (
                <ChevronDown className="w-3.5 h-3.5 ml-auto" />
              ) : (
                <ChevronRight className="w-3.5 h-3.5 ml-auto" />
              )}
            </button>
            {reasoningOpen && (
              <div className="px-3 pb-2.5 text-xs leading-relaxed text-[#747775] dark:text-[#9aa0a6] whitespace-pre-wrap break-words">
                {reasoning}
              </div>
            )}
          </div>
        )}
        <div className="text-[15px] leading-relaxed text-[#1f1f1f] dark:text-[#e3e3e3] markdown-body select-text">
          <ReactMarkdown
            remarkPlugins={[remarkGfm]}
            rehypePlugins={[rehypeHighlight]}
          >
            {content || (streaming ? '' : '')}
          </ReactMarkdown>
          {streaming && (
            <span className="inline-block w-2 h-4 ml-1 bg-[#1a73e8] dark:bg-[#8ab4f8] animate-pulse align-middle rounded-sm" />
          )}
        </div>

        {!streaming && content && (
          <div className="flex items-center gap-1 mt-3 opacity-0 group-hover:opacity-100 transition-opacity">
            <button
              onClick={copy}
              className="p-1.5 text-[#747775] dark:text-[#9aa0a6] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded-full transition-colors"
              title="复制回答"
            >
              {copied ? <Check className="w-4 h-4 text-emerald-600 dark:text-emerald-400" /> : <Copy className="w-4 h-4" />}
            </button>
            {onRegenerate && (
              <button
                onClick={onRegenerate}
                className="p-1.5 text-[#747775] dark:text-[#9aa0a6] hover:text-[#1f1f1f] dark:hover:text-[#f1f3f4] hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] rounded-full transition-colors"
                title="重新生成"
              >
                <RotateCcw className="w-4 h-4" />
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  )
}


