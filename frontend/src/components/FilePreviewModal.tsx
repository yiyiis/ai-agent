import { useEffect, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import { Check, Copy, Download, ExternalLink, X } from 'lucide-react'
import type { Attachment } from '../types'

interface Props {
  attachment: Attachment | null
  onClose: () => void
}

type Kind = 'markdown' | 'html' | 'pdf' | 'image' | 'text' | 'unknown'

function detectKind(a: Attachment): Kind {
  const ct = (a.content_type || '').toLowerCase()
  const ext = (a.filename.toLowerCase().split('.').pop() || '').trim()
  if (ct.includes('markdown') || ext === 'md' || ext === 'markdown') return 'markdown'
  if (ct === 'text/html' || ext === 'html' || ext === 'htm') return 'html'
  if (ct === 'application/pdf' || ext === 'pdf') return 'pdf'
  if (ct.startsWith('image/') ||
      ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg'].includes(ext)) return 'image'
  if (ct.startsWith('text/') || ['txt', 'log', 'csv', 'json', 'xml', 'yaml', 'yml'].includes(ext)) return 'text'
  return 'unknown'
}

export function isPreviewable(a: Attachment): boolean {
  return detectKind(a) !== 'unknown'
}

/** 抓取文本类资源——按 UTF-8 强制解码。
 *
 * 不能用 ``response.text()``：浏览器会优先信任服务端 ``Content-Type`` 里的 charset，
 * 而对象存储 OSS 对某些扩展名要么不带 charset、要么带错（GBK），导致中文乱码。
 * 我们已知所有上传都是 UTF-8 字节流，这里强制按 UTF-8 解。
 */
function useFetchText(url: string | null) {
  const [state, setState] = useState<{ text: string | null; error: string | null; loading: boolean }>(
    { text: null, error: null, loading: false },
  )
  useEffect(() => {
    if (!url) return
    let cancelled = false
    setState({ text: null, error: null, loading: true })
    fetch(url)
      .then(async (r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.arrayBuffer()
      })
      .then((buf) => {
        const t = new TextDecoder('utf-8').decode(buf)
        if (!cancelled) setState({ text: t, error: null, loading: false })
      })
      .catch((e) => {
        if (!cancelled) setState({ text: null, error: String(e), loading: false })
      })
    return () => {
      cancelled = true
    }
  }, [url])
  return state
}

function MarkdownView({ url }: { url: string }) {
  const { text, error, loading } = useFetchText(url)
  if (loading) return <div className="p-6 text-sm text-neutral-400">加载中…</div>
  if (error) return <div className="p-6 text-sm text-red-500">加载失败：{error}</div>
  return (
    <div className="markdown-body px-8 py-6 max-w-none">
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]}>
        {text || ''}
      </ReactMarkdown>
    </div>
  )
}

function TextView({ url }: { url: string }) {
  const { text, error, loading } = useFetchText(url)
  if (loading) return <div className="p-6 text-sm text-neutral-400 dark:text-[#9aa0a6]">加载中…</div>
  if (error) return <div className="p-6 text-sm text-red-500 dark:text-red-400">加载失败：{error}</div>
  return (
    <pre className="px-6 py-4 text-xs font-mono whitespace-pre-wrap break-words text-neutral-800 dark:text-[#f1f3f4]">
      {text}
    </pre>
  )
}

/** HTML 预览：先 fetch + UTF-8 解码，再通过 srcDoc 注入。
 *
 * 不直接用 ``iframe src=url``，原因有二：
 * 1) 对象存储 OSS 对 .html 经常以 ``text/plain`` 或 ``application/octet-stream`` 返回，
 *    浏览器会把整页当源码展示而不渲染；
 * 2) 即便被识别成 HTML，服务端的 Content-Type charset 也可能跟实际字节不一致，
 *    导致中文乱码——而 HTTP 头的 charset 优先级高于 ``<meta charset>``。
 * srcDoc 走 JS 字符串，跟服务端 Content-Type 完全解耦，渲染与编码都受我们掌控。
 */
function HtmlView({ url, title }: { url: string; title: string }) {
  const { text, error, loading } = useFetchText(url)
  if (loading) return <div className="p-6 text-sm text-neutral-400 dark:text-[#9aa0a6]">加载中…</div>
  if (error) return <div className="p-6 text-sm text-red-500 dark:text-red-400">加载失败：{error}</div>
  return (
    <iframe
      srcDoc={text || ''}
      title={title}
      // 不带 allow-same-origin —— srcDoc 内容来自不可信的 OSS，阻断它读 cookie / 与父页面通信
      sandbox="allow-scripts allow-popups"
      className="w-full h-full bg-white border-0"
    />
  )
}

export function FilePreviewModal({ attachment, onClose }: Props) {
  const [copied, setCopied] = useState(false)

  // Esc 关闭
  useEffect(() => {
    if (!attachment) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [attachment, onClose])

  if (!attachment) return null
  const kind = detectKind(attachment)
  const url = attachment.url

  const copyLink = async () => {
    try {
      await navigator.clipboard.writeText(url)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* ignore */
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 bg-black/40 flex items-stretch justify-end"
      onClick={onClose}
    >
      <div
        className="bg-white dark:bg-[#1e1f20] text-[#1f1f1f] dark:text-[#f1f3f4] border-l border-neutral-200 dark:border-[#3c4043] w-full max-w-[60vw] min-w-[480px] h-full flex flex-col shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        {/* 顶栏 */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-neutral-200 dark:border-[#3c4043] shrink-0">
          <div className="flex-1 min-w-0">
            <div className="text-sm font-medium truncate text-neutral-900 dark:text-[#f1f3f4]" title={attachment.filename}>
              {attachment.filename}
            </div>
            <div className="text-[11px] text-neutral-400 dark:text-[#9aa0a6] truncate">
              {attachment.content_type || kind}
            </div>
          </div>
          <button
            onClick={copyLink}
            className="p-1.5 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded"
            title="复制链接"
          >
            {copied ? <Check className="w-4 h-4 text-emerald-500" /> : <Copy className="w-4 h-4" />}
          </button>
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="p-1.5 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded"
            title="新标签打开"
          >
            <ExternalLink className="w-4 h-4" />
          </a>
          <a
            href={url}
            download={attachment.filename}
            className="p-1.5 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded"
            title="下载"
          >
            <Download className="w-4 h-4" />
          </a>
          <button
            onClick={onClose}
            className="p-1.5 text-neutral-500 dark:text-[#9aa0a6] hover:text-neutral-900 dark:hover:text-[#f1f3f4] hover:bg-neutral-100 dark:hover:bg-[#28292a] rounded ml-1"
            title="关闭 (Esc)"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* 内容区 */}
        <div className="flex-1 overflow-auto bg-neutral-50 dark:bg-[#131314]">
          {kind === 'markdown' && <MarkdownView url={url} />}
          {kind === 'html' && <HtmlView url={url} title={attachment.filename} />}
          {kind === 'pdf' && (
            <iframe
              src={url}
              title={attachment.filename}
              className="w-full h-full bg-white border-0"
            />
          )}
          {kind === 'image' && (
            <div className="w-full h-full flex items-center justify-center p-6 bg-neutral-100 dark:bg-[#1e1f20]">
              <img
                src={url}
                alt={attachment.filename}
                className="max-w-full max-h-full object-contain"
              />
            </div>
          )}
          {kind === 'text' && <TextView url={url} />}
          {kind === 'unknown' && (
            <div className="p-8 text-sm text-neutral-500 dark:text-[#9aa0a6]">
              此类型不支持在线预览。请点击右上角下载或在新标签页打开。
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
