import { useEffect, useMemo, useRef, useState } from 'react'
import CodeMirror, { type ReactCodeMirrorRef } from '@uiw/react-codemirror'
import { EditorView } from '@codemirror/view'
import { markdown } from '@codemirror/lang-markdown'
import { json } from '@codemirror/lang-json'
import { python } from '@codemirror/lang-python'
import {
  Download,
  File as FileIcon,
  FilePlus,
  Loader2,
  Pencil,
  Save,
  Trash2,
} from 'lucide-react'
import { api } from '../api/client'
import type { SkillFileNode } from '../types'
import { cn } from '../lib/utils'

interface Props {
  skillId: number
  /** 父组件用来在 modal 关闭前提醒"还有未保存修改" */
  onDirtyChange?: (dirty: boolean) => void
}

/** 文件后缀 → CodeMirror 语言扩展（没有的就当纯文本）
 *
 * 所有文件都额外塞 lineWrapping——SKILL.md / manifest.json 经常有超长行，
 * 不换行的话编辑器会把外层布局撑爆出 modal 宽度。
 */
function langForPath(path: string) {
  const base = [EditorView.lineWrapping]
  const lower = path.toLowerCase()
  if (lower.endsWith('.md') || lower.endsWith('.markdown'))
    return [markdown(), ...base]
  if (lower.endsWith('.json')) return [json(), ...base]
  if (lower.endsWith('.py')) return [python(), ...base]
  return base
}

export function SkillFileEditor({ skillId, onDirtyChange }: Props) {
  const [tree, setTree] = useState<SkillFileNode[]>([])
  const [loadingTree, setLoadingTree] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const [content, setContent] = useState<string>('')
  const [savedContent, setSavedContent] = useState<string>('')
  const [isBinary, setIsBinary] = useState(false)
  const [loadingFile, setLoadingFile] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [isDark, setIsDark] = useState(() =>
    document.documentElement.classList.contains('dark'),
  )
  const editorRef = useRef<ReactCodeMirrorRef>(null)

  useEffect(() => {
    const observer = new MutationObserver(() => {
      setIsDark(document.documentElement.classList.contains('dark'))
    })
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class'],
    })
    return () => observer.disconnect()
  }, [])

  const dirty = !isBinary && content !== savedContent

  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])

  const refreshTree = async () => {
    setLoadingTree(true)
    try {
      const t = await api.getSkillTree(skillId)
      setTree(t)
      // 默认选中 SKILL.md，方便用户进来直接编辑主文件
      if (!selected && t.length > 0) {
        const main = t.find((n) => n.path === 'SKILL.md') ?? t[0]
        await loadFile(main.path)
      }
    } catch (e) {
      setError(String(e))
    } finally {
      setLoadingTree(false)
    }
  }

  useEffect(() => {
    refreshTree()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [skillId])

  const loadFile = async (path: string) => {
    if (dirty && !confirm('当前文件有未保存修改，确定切走吗？')) return
    setError(null)
    setLoadingFile(true)
    try {
      const f = await api.getSkillFile(skillId, path)
      setSelected(path)
      setIsBinary(f.is_binary)
      setContent(f.is_binary ? '' : f.content)
      setSavedContent(f.is_binary ? '' : f.content)
    } catch (e) {
      setError(String(e))
    } finally {
      setLoadingFile(false)
    }
  }

  const save = async () => {
    if (!selected || isBinary || !dirty) return
    setError(null)
    setSaving(true)
    try {
      await api.writeSkillFile(skillId, selected, content)
      setSavedContent(content)
      // 刷新文件树以反映可能的大小变化
      const t = await api.getSkillTree(skillId)
      setTree(t)
    } catch (e) {
      setError(String(e))
    } finally {
      setSaving(false)
    }
  }

  const createFile = async () => {
    const path = prompt('新文件路径（相对 skill 根，可带子目录，如 templates/foo.txt）')
    if (!path) return
    setError(null)
    try {
      await api.createSkillFile(skillId, path.trim(), '')
      const t = await api.getSkillTree(skillId)
      setTree(t)
      await loadFile(path.trim())
    } catch (e) {
      setError(String(e))
    }
  }

  const renameFile = async (path: string) => {
    const next = prompt('新路径（相对 skill 根）', path)
    if (!next || next === path) return
    setError(null)
    try {
      await api.renameSkillFile(skillId, path, next.trim())
      const t = await api.getSkillTree(skillId)
      setTree(t)
      if (selected === path) {
        setSelected(next.trim())
      }
    } catch (e) {
      setError(String(e))
    }
  }

  const removeFile = async (path: string) => {
    if (!confirm(`删除 ${path}？`)) return
    setError(null)
    try {
      await api.deleteSkillFile(skillId, path)
      const t = await api.getSkillTree(skillId)
      setTree(t)
      if (selected === path) {
        setSelected(null)
        setContent('')
        setSavedContent('')
        setIsBinary(false)
      }
    } catch (e) {
      setError(String(e))
    }
  }

  const downloadBinary = async () => {
    if (!selected) return
    // 走文件读接口不行（二进制不返回内容）；直接 fetch /file?path=...
    // 这里走 tree 里的 size + 单文件下载链接太复杂，简单点：用 download 整个 skill 让用户挑
    // 但更直接：fetch /file?path=...&raw=1 ——我们后端目前没做 raw 模式。
    // v1 先给个提示，后续再加 raw 下载接口。
    alert('二进制文件单文件下载暂未支持，请用 skill 整体下载（侧栏上方按钮）。')
  }

  const extensions = useMemo(
    () => (selected ? langForPath(selected) : []),
    [selected],
  )

  // ⌘S / Ctrl+S 保存
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 's') {
        e.preventDefault()
        if (dirty && !saving) save()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dirty, saving, content, selected])

  return (
    // 关键：min-w-0 让 flex 子节点可以收缩到父容器内，否则编辑器内容会把整层布局撑过 modal 宽度
    <div className="flex h-full min-h-0 min-w-0 w-full gap-3">
      {/* 文件树 */}
      <div className="w-56 shrink-0 flex flex-col border border-neutral-200 dark:border-[#3c4043] rounded-md overflow-hidden bg-white dark:bg-[#1e1f20]">
        <div className="flex items-center justify-between px-2 py-1.5 border-b border-neutral-200 dark:border-[#3c4043] bg-neutral-50 dark:bg-[#28292a]">
          <span className="text-xs font-medium text-neutral-600 dark:text-[#c4c7c5]">文件</span>
          <button
            onClick={createFile}
            title="新建文件"
            className="p-1 hover:bg-neutral-200 dark:hover:bg-[#333537] rounded text-neutral-600 dark:text-[#c4c7c5]"
          >
            <FilePlus className="w-3.5 h-3.5" />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto scrollbar-thin">
          {loadingTree && (
            <div className="text-xs text-neutral-400 dark:text-[#9aa0a6] px-2 py-3 flex items-center gap-1">
              <Loader2 className="w-3 h-3 animate-spin" />
              加载中…
            </div>
          )}
          {!loadingTree && tree.length === 0 && (
            <div className="text-xs text-neutral-400 dark:text-[#747775] px-2 py-3">（无文件）</div>
          )}
          {tree.map((node) => {
            const isSelected = node.path === selected
            const isDirty = isSelected && dirty
            return (
              <div
                key={node.path}
                className={cn(
                  'group flex items-center gap-1 px-2 py-1 text-xs cursor-pointer',
                  isSelected
                    ? 'bg-neutral-900 dark:bg-[#004a77] text-white dark:text-[#c2e7ff]'
                    : 'hover:bg-neutral-100 dark:hover:bg-[#28292a] text-neutral-700 dark:text-[#f1f3f4]',
                )}
                onClick={() => loadFile(node.path)}
                title={node.path}
              >
                <FileIcon className="w-3 h-3 shrink-0 opacity-70" />
                <span className="truncate flex-1">{node.path}</span>
                {isDirty && (
                  <span className="w-1.5 h-1.5 rounded-full bg-orange-400 shrink-0" />
                )}
                {node.is_binary && (
                  <span
                    className={cn(
                      'text-[10px] px-1 rounded shrink-0',
                      isSelected
                        ? 'bg-white/20 text-white'
                        : 'bg-neutral-200 dark:bg-[#3c4043] text-neutral-600 dark:text-[#c4c7c5]',
                    )}
                    title="二进制文件"
                  >
                    bin
                  </span>
                )}
                <button
                  onClick={(e) => {
                    e.stopPropagation()
                    renameFile(node.path)
                  }}
                  className={cn(
                    'p-0.5 rounded opacity-0 group-hover:opacity-100 shrink-0',
                    isSelected ? 'hover:bg-white/20' : 'hover:bg-neutral-200 dark:hover:bg-[#333537]',
                  )}
                  title="重命名"
                >
                  <Pencil className="w-3 h-3" />
                </button>
                <button
                  onClick={(e) => {
                    e.stopPropagation()
                    removeFile(node.path)
                  }}
                  className={cn(
                    'p-0.5 rounded opacity-0 group-hover:opacity-100 shrink-0',
                    isSelected
                      ? 'hover:bg-white/20 text-white'
                      : 'hover:bg-red-50 dark:hover:bg-red-950/40 text-red-500',
                  )}
                  title="删除"
                >
                  <Trash2 className="w-3 h-3" />
                </button>
              </div>
            )
          })}
        </div>
      </div>

      {/* 编辑器 */}
      <div className="flex-1 min-w-0 flex flex-col border border-neutral-200 dark:border-[#3c4043] rounded-md overflow-hidden bg-white dark:bg-[#1e1f20]">
        <div className="flex items-center justify-between px-3 py-1.5 border-b border-neutral-200 dark:border-[#3c4043] bg-neutral-50 dark:bg-[#28292a] text-xs">
          <div className="flex items-center gap-2 min-w-0">
            <span className="font-mono truncate text-neutral-700 dark:text-[#f1f3f4]">
              {selected ?? '（未选中文件）'}
            </span>
            {dirty && <span className="text-orange-500">●</span>}
          </div>
          <div className="flex items-center gap-1">
            {isBinary && selected && (
              <button
                onClick={downloadBinary}
                className="px-2 py-0.5 rounded hover:bg-neutral-200 dark:hover:bg-[#333537] text-neutral-600 dark:text-[#c4c7c5] flex items-center gap-1"
              >
                <Download className="w-3 h-3" /> 下载
              </button>
            )}
            <button
              onClick={save}
              disabled={!dirty || saving || isBinary}
              className="px-2 py-0.5 rounded bg-neutral-900 dark:bg-[#1a73e8] text-white flex items-center gap-1 disabled:opacity-40 hover:bg-neutral-800 dark:hover:bg-[#1557b0]"
              title="保存（⌘S）"
            >
              {saving ? (
                <Loader2 className="w-3 h-3 animate-spin" />
              ) : (
                <Save className="w-3 h-3" />
              )}
              保存
            </button>
          </div>
        </div>

        {error && (
          <div className="px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs border-b border-red-100 dark:border-red-900">
            {error}
          </div>
        )}

        <div className="flex-1 min-h-0 overflow-auto">
          {loadingFile && (
            <div className="text-xs text-neutral-400 dark:text-[#9aa0a6] px-3 py-3 flex items-center gap-1">
              <Loader2 className="w-3 h-3 animate-spin" />
              加载中…
            </div>
          )}
          {!loadingFile && !selected && (
            <div className="text-sm text-neutral-400 dark:text-[#747775] text-center py-12">
              从左侧选一个文件开始编辑
            </div>
          )}
          {!loadingFile && selected && isBinary && (
            <div className="text-sm text-neutral-500 dark:text-[#9aa0a6] text-center py-12">
              二进制文件，不可在此编辑
            </div>
          )}
          {!loadingFile && selected && !isBinary && (
            <CodeMirror
              ref={editorRef}
              value={content}
              onChange={(v) => setContent(v)}
              extensions={extensions}
              basicSetup={{
                lineNumbers: true,
                highlightActiveLine: true,
                foldGutter: true,
                bracketMatching: true,
                autocompletion: false,
              }}
              theme={isDark ? 'dark' : 'light'}
              height="100%"
              style={{ height: '100%', fontSize: 13 }}
            />
          )}
        </div>
      </div>
    </div>
  )
}
