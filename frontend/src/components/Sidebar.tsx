import { useState } from 'react'
import {
  Brain,
  LayoutGrid,
  MoreHorizontal,
  Pencil,
  Settings,
  SquarePen,
  Trash2,
} from 'lucide-react'
import type { Session } from '../types'
import { cn } from '../lib/utils'
import { BrandIcon, SidebarToggleIcon } from './BrandIcons'
import { SettingsModal } from './SettingsModal'

interface Props {
  sessions: Session[]
  activeId: string | null
  /** 主区域当前显示的 view —— 用来高亮侧栏底部的入口按钮 */
  view: 'chat' | 'plaza' | 'memories'
  onSelect: (id: string) => void
  onCreate: () => void
  onDelete: (id: string) => void
  onRename: (id: string, title: string) => void
  onOpenPlaza: () => void
  onOpenMemories: () => void
  userName?: string
  userId?: number
  onLogout: () => void
}

export function Sidebar({
  sessions,
  activeId,
  view,
  onSelect,
  onCreate,
  onDelete,
  onRename,
  onOpenPlaza,
  onOpenMemories,
  userName,
  userId,
  onLogout,
}: Props) {
  const [collapsed, setCollapsed] = useState(() => {
    return localStorage.getItem('ai_agent_sidebar_collapsed') === 'true'
  })
  const [settingsOpen, setSettingsOpen] = useState(false)

  const toggleCollapse = () => {
    setCollapsed((prev) => {
      const next = !prev
      localStorage.setItem('ai_agent_sidebar_collapsed', String(next))
      return next
    })
  }

  const display = userName || (userId ? `u${userId}` : '用户')

  return (
    <>
      <aside
        className={cn(
          'bg-[#f0f4f9] dark:bg-[#1e1f20] flex flex-col shrink-0 select-none border-r border-[#e3e3e3]/70 dark:border-[#28292a] font-sans transition-all duration-300 ease-in-out',
          collapsed ? 'w-[68px]' : 'w-64',
        )}
      >
        {/* 顶部标题与折叠按钮 */}
        <div className={cn('pt-4 pb-3 flex items-center', collapsed ? 'px-3 justify-center' : 'px-4 justify-between')}>
          {!collapsed && (
            <div className="flex items-center gap-2.5 min-w-0">
              <BrandIcon className="w-6 h-6 shrink-0" />
              <div className="flex flex-col min-w-0">
                <span className="text-[16px] font-semibold text-[#1f1f1f] dark:text-[#f1f3f4] tracking-tight leading-tight truncate">
                  AI Agent
                </span>
                <span className="text-[10px] text-[#747775] dark:text-[#9aa0a6] leading-tight truncate">
                  智能协同工作台
                </span>
              </div>
            </div>
          )}
          <button
            onClick={toggleCollapse}
            className="p-1.5 rounded-full hover:bg-[#e1e7f0] dark:hover:bg-[#28292a] text-[#444746] dark:text-[#c4c7c5] transition-colors"
            title={collapsed ? '展开侧边栏' : '折叠侧栏'}
          >
            <SidebarToggleIcon className="w-4 h-4" />
          </button>
        </div>

        {/* 发起新对话按钮 */}
        <div className={cn('pt-1 pb-2', collapsed ? 'px-3 flex justify-center' : 'px-3')}>
          {collapsed ? (
            <button
              onClick={onCreate}
              className="w-10 h-10 rounded-full bg-white dark:bg-[#28292a] hover:bg-[#e1e7f0]/60 dark:hover:bg-[#333537] active:bg-[#d3e3fd] text-[#1f1f1f] dark:text-[#e3e3e3] border border-[#e3e3e3] dark:border-[#3c4043] shadow-[0_1px_3px_rgba(0,0,0,0.06)] hover:shadow flex items-center justify-center transition-all"
              title="发起新对话"
            >
              <SquarePen className="w-4 h-4 text-[#444746] dark:text-[#c4c7c5]" />
            </button>
          ) : (
            <button
              onClick={onCreate}
              className="w-full flex items-center gap-3 px-4 py-2.5 rounded-full bg-white dark:bg-[#28292a] hover:bg-[#e1e7f0]/60 dark:hover:bg-[#333537] active:bg-[#d3e3fd] text-[#1f1f1f] dark:text-[#e3e3e3] text-sm font-medium border border-[#e3e3e3] dark:border-[#3c4043] shadow-[0_1px_3px_rgba(0,0,0,0.06)] hover:shadow transition-all"
            >
              <SquarePen className="w-4 h-4 text-[#444746] dark:text-[#c4c7c5]" />
              <span>发起新对话</span>
            </button>
          )}
        </div>

        {/* 核心功能导航 */}
        <div className={cn('py-1 space-y-1', collapsed ? 'px-3 flex flex-col items-center' : 'px-3 space-y-0.5')}>
          {collapsed ? (
            <>
              <button
                onClick={onOpenPlaza}
                className={cn(
                  'w-10 h-10 rounded-full flex items-center justify-center text-xs font-medium transition-colors',
                  view === 'plaza'
                    ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff]'
                    : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0] dark:hover:bg-[#28292a]',
                )}
                title="Skills 广场"
              >
                <LayoutGrid className="w-4 h-4" />
              </button>
              <button
                onClick={onOpenMemories}
                className={cn(
                  'w-10 h-10 rounded-full flex items-center justify-center text-xs font-medium transition-colors',
                  view === 'memories'
                    ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff]'
                    : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0] dark:hover:bg-[#28292a]',
                )}
                title="记忆库"
              >
                <Brain className="w-4 h-4" />
              </button>
            </>
          ) : (
            <>
              <button
                onClick={onOpenPlaza}
                className={cn(
                  'w-full flex items-center gap-3 px-3.5 py-2 rounded-full text-xs font-medium transition-colors',
                  view === 'plaza'
                    ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-semibold'
                    : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0] dark:hover:bg-[#28292a]',
                )}
              >
                <LayoutGrid className="w-4 h-4 shrink-0" />
                <span>Skills 广场</span>
              </button>
              <button
                onClick={onOpenMemories}
                className={cn(
                  'w-full flex items-center gap-3 px-3.5 py-2 rounded-full text-xs font-medium transition-colors',
                  view === 'memories'
                    ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-semibold'
                    : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0] dark:hover:bg-[#28292a]',
                )}
              >
                <Brain className="w-4 h-4 shrink-0" />
                <span>记忆库</span>
              </button>
            </>
          )}
        </div>

        {/* 最近对话区域（仅在展开时显示） */}
        {!collapsed && (
          <>
            <div className="px-4 pt-4 pb-1 text-xs font-medium text-[#747775] dark:text-[#9aa0a6]">
              最近
            </div>
            <div className="flex-1 overflow-y-auto scrollbar-thin px-3 py-1 space-y-0.5">
              {sessions.length === 0 ? (
                <div className="text-center text-[#747775] dark:text-[#9aa0a6] text-xs py-8">
                  暂无历史对话
                </div>
              ) : (
                sessions.map((s) => (
                  <SessionItem
                    key={s.id}
                    session={s}
                    active={view === 'chat' && s.id === activeId}
                    onSelect={() => onSelect(s.id)}
                    onDelete={() => onDelete(s.id)}
                    onRename={(title) => onRename(s.id, title)}
                  />
                ))
              )}
            </div>
          </>
        )}

        {/* 折叠时填充剩余空间 */}
        {collapsed && <div className="flex-1" />}

        {/* 底部用户信息与设置 */}
        <div className="p-2 border-t border-[#e3e3e3]/80 dark:border-[#28292a]">
          {collapsed ? (
            <div className="flex flex-col items-center gap-2 py-1">
              <button
                onClick={() => setSettingsOpen(true)}
                className="w-10 h-10 rounded-full hover:bg-[#e1e7f0] dark:hover:bg-[#28292a] text-[#444746] dark:text-[#c4c7c5] flex items-center justify-center transition-colors"
                title="设置"
              >
                <Settings className="w-4 h-4" />
              </button>
              <div
                onClick={() => setSettingsOpen(true)}
                className="w-8 h-8 rounded-full bg-[#1a73e8] text-white text-xs font-medium flex items-center justify-center shrink-0 cursor-pointer shadow-sm hover:ring-2 hover:ring-[#1a73e8]/30 transition-all"
                title={`${display} (点击打开设置)`}
              >
                {display.slice(0, 1).toUpperCase()}
              </div>
            </div>
          ) : (
            <div className="flex items-center justify-between p-2 rounded-full hover:bg-[#e1e7f0] dark:hover:bg-[#28292a] transition-colors">
              <div
                onClick={() => setSettingsOpen(true)}
                className="flex items-center gap-2.5 min-w-0 cursor-pointer flex-1"
                title="点击打开设置"
              >
                <div className="w-7 h-7 rounded-full bg-[#1a73e8] text-white text-xs font-medium flex items-center justify-center shrink-0">
                  {display.slice(0, 1).toUpperCase()}
                </div>
                <div className="min-w-0 flex flex-col">
                  <span className="text-xs font-medium text-[#1f1f1f] dark:text-[#f1f3f4] truncate leading-tight">
                    {display}
                  </span>
                  <span className="text-[10px] text-[#747775] dark:text-[#9aa0a6] leading-tight font-medium">
                    Pro 尊享版
                  </span>
                </div>
              </div>
              <button
                onClick={() => setSettingsOpen(true)}
                className="p-1.5 text-[#747775] hover:text-[#1f1f1f] dark:text-[#9aa0a6] dark:hover:text-[#e3e3e3] rounded-full hover:bg-black/5 dark:hover:bg-white/10 transition-colors"
                title="设置"
              >
                <Settings className="w-4 h-4" />
              </button>
            </div>
          )}
        </div>
      </aside>

      {/* 设置对话框 */}
      <SettingsModal
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        userName={userName}
        userId={userId}
        onLogout={onLogout}
      />
    </>
  )
}

function SessionItem({
  session,
  active,
  onSelect,
  onDelete,
  onRename,
}: {
  session: Session
  active: boolean
  onSelect: () => void
  onDelete: () => void
  onRename: (title: string) => void
}) {
  const [menuOpen, setMenuOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [titleInput, setTitleInput] = useState(session.title)

  if (editing) {
    return (
      <div className="px-3 py-1.5">
        <input
          autoFocus
          value={titleInput}
          onChange={(e) => setTitleInput(e.target.value)}
          onBlur={() => {
            const next = titleInput.trim()
            if (next && next !== session.title) onRename(next)
            setEditing(false)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              const next = titleInput.trim()
              if (next && next !== session.title) onRename(next)
              setEditing(false)
            } else if (e.key === 'Escape') {
              setTitleInput(session.title)
              setEditing(false)
            }
          }}
          className="w-full text-xs px-2.5 py-1 rounded-lg border border-[#1a73e8] bg-white dark:bg-[#28292a] text-[#1f1f1f] dark:text-[#e3e3e3] outline-none"
        />
      </div>
    )
  }

  return (
    <div
      className={cn(
        'group relative flex items-center gap-2 px-3.5 py-2 rounded-full cursor-pointer text-xs transition-colors',
        active
          ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-semibold'
          : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0] dark:hover:bg-[#28292a]',
      )}
      onClick={onSelect}
    >
      <span className="flex-1 truncate">{session.title}</span>

      <div className="relative shrink-0">
        <button
          className={cn(
            'p-1 rounded-full text-[#444746] dark:text-[#c4c7c5] hover:bg-black/10 dark:hover:bg-white/10 transition-opacity',
            menuOpen ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
          )}
          onClick={(e) => {
            e.stopPropagation()
            setMenuOpen((o) => !o)
          }}
          title="更多操作"
        >
          <MoreHorizontal className="w-3.5 h-3.5" />
        </button>

        {menuOpen && (
          <>
            <div
              className="fixed inset-0 z-20"
              onClick={(e) => {
                e.stopPropagation()
                setMenuOpen(false)
              }}
            />
            <div className="absolute right-0 top-full mt-1 w-32 bg-white dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] rounded-2xl shadow-lg z-30 p-1 animate-fade-in">
              <button
                className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-[#444746] dark:text-[#c4c7c5] rounded-xl hover:bg-[#f0f4f9] dark:hover:bg-[#333537] transition-colors"
                onClick={(e) => {
                  e.stopPropagation()
                  setMenuOpen(false)
                  setEditing(true)
                }}
              >
                <Pencil className="w-3.5 h-3.5 text-[#747775] dark:text-[#9aa0a6]" /> 重命名
              </button>
              <button
                className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-rose-600 dark:text-rose-400 rounded-xl hover:bg-rose-50 dark:hover:bg-rose-950/40 transition-colors"
                onClick={(e) => {
                  e.stopPropagation()
                  setMenuOpen(false)
                  if (confirm(`删除会话 "${session.title}"？`)) onDelete()
                }}
              >
                <Trash2 className="w-3.5 h-3.5 text-rose-500" /> 删除
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
