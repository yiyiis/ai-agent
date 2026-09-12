import { useState } from 'react'
import { Loader2, Lock, User } from 'lucide-react'
import { BrandIcon } from './BrandIcons'
import { api } from '../api/client'

/** 账号密码登录表单。成功后把 token 交给上层（写 cookie 并重新校验）。 */
export function LoginForm({ onSuccess }: { onSuccess: (token: string) => void }) {
  const [account, setAccount] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!account.trim() || !password) return
    setError(null)
    setLoading(true)
    try {
      const { token } = await api.login(account.trim(), password)
      onSuccess(token)
    } catch (err) {
      // 信封错误已带业务文案（如"账号或密码错误"），直接展示
      setError(String(err))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center p-4 bg-white dark:bg-[#131314] bg-[radial-gradient(circle_at_50%_45%,_rgba(219,234,254,0.5)_0%,_rgba(235,243,254,0.2)_45%,_#ffffff_75%)] dark:bg-[radial-gradient(circle_at_50%_45%,_rgba(26,115,232,0.15)_0%,_rgba(19,19,20,0.5)_45%,_#131314_75%)] text-[#1f1f1f] dark:text-[#f1f3f4] relative overflow-hidden select-none font-sans">
      <form
        onSubmit={submit}
        className="relative w-full max-w-sm bg-white dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] rounded-[28px] shadow-gemini-pill p-8 z-10 animate-slide-up"
      >
        <div className="text-center mb-6">
          <div className="w-14 h-14 rounded-full bg-[#f0f4f9] dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] flex items-center justify-center shadow-sm mx-auto mb-3">
            <BrandIcon className="w-8 h-8" />
          </div>
          <h1 className="text-2xl font-normal text-[#1f1f1f] dark:text-[#f1f3f4] tracking-tight">AI Agent 工作台</h1>
          <p className="text-xs text-[#747775] dark:text-[#9aa0a6] mt-1">登录你的智能协同助手</p>
        </div>

        <div className="space-y-4">
          <div>
            <label className="block text-xs font-medium text-[#444746] dark:text-[#c4c7c5] mb-1.5 flex items-center gap-1">
              <User className="w-3.5 h-3.5 text-[#747775] dark:text-[#9aa0a6]" />
              <span>账号</span>
            </label>
            <input
              autoFocus
              value={account}
              onChange={(e) => setAccount(e.target.value)}
              className="w-full px-4 py-2.5 bg-[#f0f4f9] dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] rounded-2xl text-sm outline-none focus:bg-white dark:focus:bg-[#1e1f20] focus:border-[#1a73e8] dark:focus:border-[#8ab4f8] focus:ring-2 focus:ring-[#d3e3fd] dark:focus:ring-[#8ab4f8]/20 transition-all placeholder:text-[#747775] dark:placeholder:text-[#9aa0a6] text-[#1f1f1f] dark:text-[#f1f3f4]"
              placeholder="用户名 / 邮箱 / 手机号"
            />
          </div>

          <div>
            <label className="block text-xs font-medium text-[#444746] dark:text-[#c4c7c5] mb-1.5 flex items-center gap-1">
              <Lock className="w-3.5 h-3.5 text-[#747775] dark:text-[#9aa0a6]" />
              <span>密码</span>
            </label>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full px-4 py-2.5 bg-[#f0f4f9] dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] rounded-2xl text-sm outline-none focus:bg-white dark:focus:bg-[#1e1f20] focus:border-[#1a73e8] dark:focus:border-[#8ab4f8] focus:ring-2 focus:ring-[#d3e3fd] dark:focus:ring-[#8ab4f8]/20 transition-all placeholder:text-[#747775] dark:placeholder:text-[#9aa0a6] text-[#1f1f1f] dark:text-[#f1f3f4]"
              placeholder="请输入密码"
            />
          </div>
        </div>

        {error && (
          <div className="mt-4 px-3.5 py-2 bg-rose-50 dark:bg-rose-950/40 border border-rose-200 dark:border-rose-900/50 text-rose-700 dark:text-rose-400 text-xs rounded-2xl flex items-center gap-1.5">
            <span>{error}</span>
          </div>
        )}

        <button
          type="submit"
          disabled={loading}
          className="mt-6 w-full flex items-center justify-center gap-2 px-4 py-2.5 bg-[#1a73e8] hover:bg-[#1557b0] dark:bg-[#8ab4f8] dark:hover:bg-[#a8c7fa] text-white dark:text-[#202124] text-sm font-medium rounded-full shadow-sm hover:shadow active:scale-[0.99] transition-all disabled:opacity-50"
        >
          {loading && <Loader2 className="w-4 h-4 animate-spin" />}
          {loading ? '正在安全登录…' : '进入工作台'}
        </button>
      </form>
    </div>
  )
}

