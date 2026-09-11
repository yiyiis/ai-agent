import { useEffect, useState } from 'react'
import {
  Cpu,
  Info,
  LogOut,
  Monitor,
  Moon,
  Settings,
  Sparkles,
  Sun,
  User,
  X,
} from 'lucide-react'
import { cn } from '../lib/utils'
import { getStoredTheme, setTheme, type Theme } from '../lib/theme'

interface Props {
  open: boolean
  onClose: () => void
  userName?: string
  userId?: number
  onLogout: () => void
}

type TabType = 'general' | 'account' | 'about'

export function SettingsModal({
  open,
  onClose,
  userName,
  userId,
  onLogout,
}: Props) {
  const [activeTab, setActiveTab] = useState<TabType>('general')
  const [currentTheme, setCurrentTheme] = useState<Theme>(getStoredTheme())
  const [sendShortcut, setSendShortcut] = useState<'enter' | 'ctrl_enter'>(() => {
    return (localStorage.getItem('ai_agent_send_shortcut') as 'enter' | 'ctrl_enter') || 'enter'
  })
  const [confirmLogout, setConfirmLogout] = useState(false)

  useEffect(() => {
    if (!open) {
      setConfirmLogout(false)
      return
    }
    setCurrentTheme(getStoredTheme())
  }, [open])

  // ESC 键关闭
  useEffect(() => {
    if (!open) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, onClose])

  if (!open) return null

  const handleThemeChange = (newTheme: Theme) => {
    setCurrentTheme(newTheme)
    setTheme(newTheme)
  }

  const handleShortcutChange = (sc: 'enter' | 'ctrl_enter') => {
    setSendShortcut(sc)
    localStorage.setItem('ai_agent_send_shortcut', sc)
  }

  const display = userName || (userId ? `u${userId}` : '用户')

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40 backdrop-blur-sm animate-fade-in">
      {/* 遮罩背景点击关闭 */}
      <div className="absolute inset-0" onClick={onClose} />

      {/* 弹窗主体 */}
      <div className="relative w-full max-w-2xl bg-white dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] rounded-3xl shadow-2xl overflow-hidden flex flex-col max-h-[85vh] animate-slide-up z-10 font-sans">
        {/* 顶部标题栏 */}
        <div className="px-6 py-4 border-b border-[#e3e3e3]/70 dark:border-[#3c4043]/70 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-xl bg-[#f0f4f9] dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] flex items-center justify-center text-[#1a73e8] dark:text-[#8ab4f8]">
              <Settings className="w-4 h-4" />
            </div>
            <div>
              <h2 className="text-base font-semibold text-[#1f1f1f] dark:text-[#f1f3f4] leading-tight">
                设置
              </h2>
              <p className="text-[11px] text-[#747775] dark:text-[#9aa0a6] leading-tight">
                自定义您的 AI Agent 工作台偏好
              </p>
            </div>
          </div>
          <button
            onClick={onClose}
            className="p-1.5 text-[#747775] hover:text-[#1f1f1f] dark:text-[#9aa0a6] dark:hover:text-[#e3e3e3] rounded-full hover:bg-[#f0f4f9] dark:hover:bg-[#28292a] transition-colors"
            title="关闭 (Esc)"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* 主体左右分栏 */}
        <div className="flex flex-1 min-h-[380px] overflow-hidden">
          {/* 左侧 Tab 栏 */}
          <div className="w-44 p-3 border-r border-[#e3e3e3]/70 dark:border-[#3c4043]/70 bg-[#fafafa] dark:bg-[#191a1b] flex flex-col gap-1 shrink-0">
            <button
              onClick={() => setActiveTab('general')}
              className={cn(
                'flex items-center gap-2.5 px-3 py-2 text-xs font-medium rounded-xl transition-colors text-left',
                activeTab === 'general'
                  ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-semibold'
                  : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0]/60 dark:hover:bg-[#28292a]',
              )}
            >
              <Sparkles className="w-3.5 h-3.5" />
              <span>常规与外观</span>
            </button>
            <button
              onClick={() => setActiveTab('account')}
              className={cn(
                'flex items-center gap-2.5 px-3 py-2 text-xs font-medium rounded-xl transition-colors text-left',
                activeTab === 'account'
                  ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-semibold'
                  : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0]/60 dark:hover:bg-[#28292a]',
              )}
            >
              <User className="w-3.5 h-3.5" />
              <span>账号与安全</span>
            </button>
            <button
              onClick={() => setActiveTab('about')}
              className={cn(
                'flex items-center gap-2.5 px-3 py-2 text-xs font-medium rounded-xl transition-colors text-left',
                activeTab === 'about'
                  ? 'bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff] font-semibold'
                  : 'text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0]/60 dark:hover:bg-[#28292a]',
              )}
            >
              <Info className="w-3.5 h-3.5" />
              <span>关于与模型</span>
            </button>
          </div>

          {/* 右侧内容区 */}
          <div className="flex-1 p-6 overflow-y-auto scrollbar-thin space-y-6">
            {activeTab === 'general' && (
              <div className="space-y-6 animate-fade-in">
                {/* 主题切换 */}
                <div>
                  <h3 className="text-xs font-semibold text-[#1f1f1f] dark:text-[#e3e3e3] uppercase tracking-wider mb-3">
                    界面主题外观
                  </h3>
                  <div className="grid grid-cols-3 gap-3">
                    {/* 浅色 */}
                    <button
                      type="button"
                      onClick={() => handleThemeChange('light')}
                      className={cn(
                        'flex flex-col items-center p-3.5 rounded-2xl border text-center transition-all',
                        currentTheme === 'light'
                          ? 'border-[#1a73e8] bg-[#f0f4f9] dark:bg-[#1a3860]/40 ring-1 ring-[#1a73e8]'
                          : 'border-[#e3e3e3] dark:border-[#3c4043] hover:border-[#1a73e8]/50 bg-white dark:bg-[#28292a]/50',
                      )}
                    >
                      <div className="w-8 h-8 rounded-full bg-amber-50 dark:bg-amber-950/40 text-amber-600 flex items-center justify-center mb-2">
                        <Sun className="w-4 h-4" />
                      </div>
                      <span className="text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">
                        浅色模式
                      </span>
                      <span className="text-[10px] text-[#747775] dark:text-[#9aa0a6] mt-0.5">
                        经典明亮
                      </span>
                    </button>

                    {/* 深色 */}
                    <button
                      type="button"
                      onClick={() => handleThemeChange('dark')}
                      className={cn(
                        'flex flex-col items-center p-3.5 rounded-2xl border text-center transition-all',
                        currentTheme === 'dark'
                          ? 'border-[#1a73e8] bg-[#f0f4f9] dark:bg-[#1a3860]/40 ring-1 ring-[#1a73e8]'
                          : 'border-[#e3e3e3] dark:border-[#3c4043] hover:border-[#1a73e8]/50 bg-white dark:bg-[#28292a]/50',
                      )}
                    >
                      <div className="w-8 h-8 rounded-full bg-indigo-50 dark:bg-indigo-950/40 text-indigo-600 flex items-center justify-center mb-2">
                        <Moon className="w-4 h-4" />
                      </div>
                      <span className="text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">
                        深色模式
                      </span>
                      <span className="text-[10px] text-[#747775] dark:text-[#9aa0a6] mt-0.5">
                        护眼暗黑
                      </span>
                    </button>

                    {/* 跟随系统 */}
                    <button
                      type="button"
                      onClick={() => handleThemeChange('system')}
                      className={cn(
                        'flex flex-col items-center p-3.5 rounded-2xl border text-center transition-all',
                        currentTheme === 'system'
                          ? 'border-[#1a73e8] bg-[#f0f4f9] dark:bg-[#1a3860]/40 ring-1 ring-[#1a73e8]'
                          : 'border-[#e3e3e3] dark:border-[#3c4043] hover:border-[#1a73e8]/50 bg-white dark:bg-[#28292a]/50',
                      )}
                    >
                      <div className="w-8 h-8 rounded-full bg-slate-100 dark:bg-slate-800 text-[#444746] dark:text-[#c4c7c5] flex items-center justify-center mb-2">
                        <Monitor className="w-4 h-4" />
                      </div>
                      <span className="text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">
                        跟随系统
                      </span>
                      <span className="text-[10px] text-[#747775] dark:text-[#9aa0a6] mt-0.5">
                        自适应切换
                      </span>
                    </button>
                  </div>
                </div>

                {/* 快捷键设置 */}
                <div className="pt-4 border-t border-[#f0f0f0] dark:border-[#28292a]">
                  <h3 className="text-xs font-semibold text-[#1f1f1f] dark:text-[#e3e3e3] uppercase tracking-wider mb-3">
                    发送消息快捷键
                  </h3>
                  <div className="space-y-2">
                    <label
                      className={cn(
                        'flex items-center justify-between p-3 rounded-xl border cursor-pointer transition-colors',
                        sendShortcut === 'enter'
                          ? 'border-[#1a73e8] bg-[#f0f4f9]/50 dark:bg-[#1a3860]/30'
                          : 'border-[#e3e3e3] dark:border-[#3c4043] hover:bg-[#fafafa] dark:hover:bg-[#28292a]/40',
                      )}
                    >
                      <div className="flex items-center gap-3">
                        <input
                          type="radio"
                          name="shortcut"
                          checked={sendShortcut === 'enter'}
                          onChange={() => handleShortcutChange('enter')}
                          className="text-[#1a73e8]"
                        />
                        <div>
                          <div className="text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">
                            Enter 发送，Shift + Enter 换行
                          </div>
                          <div className="text-[10px] text-[#747775] dark:text-[#9aa0a6]">
                            适合快速对话与即时交互
                          </div>
                        </div>
                      </div>
                      <span className="text-[10px] font-mono px-2 py-0.5 bg-white dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] rounded text-[#444746] dark:text-[#c4c7c5]">
                        Enter
                      </span>
                    </label>

                    <label
                      className={cn(
                        'flex items-center justify-between p-3 rounded-xl border cursor-pointer transition-colors',
                        sendShortcut === 'ctrl_enter'
                          ? 'border-[#1a73e8] bg-[#f0f4f9]/50 dark:bg-[#1a3860]/30'
                          : 'border-[#e3e3e3] dark:border-[#3c4043] hover:bg-[#fafafa] dark:hover:bg-[#28292a]/40',
                      )}
                    >
                      <div className="flex items-center gap-3">
                        <input
                          type="radio"
                          name="shortcut"
                          checked={sendShortcut === 'ctrl_enter'}
                          onChange={() => handleShortcutChange('ctrl_enter')}
                          className="text-[#1a73e8]"
                        />
                        <div>
                          <div className="text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">
                            Ctrl + Enter 发送，Enter 换行
                          </div>
                          <div className="text-[10px] text-[#747775] dark:text-[#9aa0a6]">
                            适合长文本排版与代码多行编辑
                          </div>
                        </div>
                      </div>
                      <span className="text-[10px] font-mono px-2 py-0.5 bg-white dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] rounded text-[#444746] dark:text-[#c4c7c5]">
                        Ctrl+Enter
                      </span>
                    </label>
                  </div>
                </div>
              </div>
            )}

            {activeTab === 'account' && (
              <div className="space-y-6 animate-fade-in">
                <div>
                  <h3 className="text-xs font-semibold text-[#1f1f1f] dark:text-[#e3e3e3] uppercase tracking-wider mb-3">
                    当前用户信息
                  </h3>
                  <div className="p-4 rounded-2xl bg-[#f8f9fa] dark:bg-[#28292a]/60 border border-[#e3e3e3] dark:border-[#3c4043] flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <div className="w-12 h-12 rounded-full bg-[#1a73e8] text-white text-base font-semibold flex items-center justify-center shadow-sm">
                        {display.slice(0, 1).toUpperCase()}
                      </div>
                      <div>
                        <div className="text-sm font-semibold text-[#1f1f1f] dark:text-[#f1f3f4]">
                          {display}
                        </div>
                        <div className="text-xs text-[#747775] dark:text-[#9aa0a6]">
                          用户 ID: #{userId ?? '—'}
                        </div>
                      </div>
                    </div>
                    <span className="px-2.5 py-1 rounded-full text-xs font-semibold bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff]">
                      Pro 尊享版
                    </span>
                  </div>
                </div>

                {/* 退出登录区域 */}
                <div className="pt-4 border-t border-[#f0f0f0] dark:border-[#28292a]">
                  <h3 className="text-xs font-semibold text-rose-600 dark:text-rose-400 uppercase tracking-wider mb-2">
                    安全与退出
                  </h3>
                  <p className="text-xs text-[#747775] dark:text-[#9aa0a6] mb-4">
                    退出当前登录态后，若要继续使用需重新输入用户名与密码。
                  </p>

                  {!confirmLogout ? (
                    <button
                      type="button"
                      onClick={() => setConfirmLogout(true)}
                      className="flex items-center gap-2 px-4 py-2.5 text-xs font-medium text-rose-600 dark:text-rose-400 bg-rose-50 dark:bg-rose-950/40 hover:bg-rose-100 dark:hover:bg-rose-900/50 border border-rose-200 dark:border-rose-900/60 rounded-xl transition-colors"
                    >
                      <LogOut className="w-3.5 h-3.5" />
                      <span>退出当前登录</span>
                    </button>
                  ) : (
                    <div className="flex items-center gap-3 p-3 bg-rose-50/80 dark:bg-rose-950/50 border border-rose-200 dark:border-rose-900 rounded-2xl animate-fade-in">
                      <span className="text-xs text-rose-700 dark:text-rose-300 font-medium">
                        确定要退出当前账号吗？
                      </span>
                      <button
                        type="button"
                        onClick={onLogout}
                        className="px-3 py-1.5 bg-rose-600 text-white rounded-lg text-xs font-medium hover:bg-rose-700 transition-colors shadow-sm"
                      >
                        确认退出
                      </button>
                      <button
                        type="button"
                        onClick={() => setConfirmLogout(false)}
                        className="px-3 py-1.5 bg-white dark:bg-[#28292a] text-[#444746] dark:text-[#c4c7c5] border border-[#e3e3e3] dark:border-[#3c4043] rounded-lg text-xs font-medium hover:bg-[#f0f4f9] transition-colors"
                      >
                        取消
                      </button>
                    </div>
                  )}
                </div>
              </div>
            )}

            {activeTab === 'about' && (
              <div className="space-y-6 animate-fade-in">
                <div>
                  <h3 className="text-xs font-semibold text-[#1f1f1f] dark:text-[#e3e3e3] uppercase tracking-wider mb-3">
                    关于平台
                  </h3>
                  <div className="p-4 rounded-2xl bg-[#f8f9fa] dark:bg-[#28292a]/60 border border-[#e3e3e3] dark:border-[#3c4043] space-y-2.5">
                    <div className="flex items-center justify-between text-xs">
                      <span className="text-[#747775] dark:text-[#9aa0a6]">平台名称</span>
                      <span className="font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">AI Agent 智能协同工作台</span>
                    </div>
                    <div className="flex items-center justify-between text-xs">
                      <span className="text-[#747775] dark:text-[#9aa0a6]">当前版本</span>
                      <span className="font-mono font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">v1.2.0</span>
                    </div>
                    <div className="flex items-center justify-between text-xs">
                      <span className="text-[#747775] dark:text-[#9aa0a6]">技术架构</span>
                      <span className="text-[#1f1f1f] dark:text-[#e3e3e3]">Go 1.24 + Gin + React 19 + Vite</span>
                    </div>
                  </div>
                </div>

                <div className="pt-4 border-t border-[#f0f0f0] dark:border-[#28292a]">
                  <h3 className="text-xs font-semibold text-[#1f1f1f] dark:text-[#e3e3e3] uppercase tracking-wider mb-3">
                    接入模型服务
                  </h3>
                  <div className="space-y-2">
                    <div className="p-3 rounded-xl border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#28292a]/40 flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <Cpu className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8]" />
                        <span className="text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">MiniMax</span>
                      </div>
                      <span className="text-[10px] font-mono text-[#747775] dark:text-[#9aa0a6]">MiniMax-Text-01, MiniMax-M3</span>
                    </div>
                    <div className="p-3 rounded-xl border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#28292a]/40 flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <Cpu className="w-3.5 h-3.5 text-[#1a73e8] dark:text-[#8ab4f8]" />
                        <span className="text-xs font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">智谱 GLM Coding Plan</span>
                      </div>
                      <span className="text-[10px] font-mono text-[#747775] dark:text-[#9aa0a6]">GLM-5.3, Flash, 5.2, 5.1, Turbo, 4.7</span>
                    </div>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
