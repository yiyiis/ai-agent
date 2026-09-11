// 登录拦截：优先复用 jwt_token cookie（中台跳转 / 已登录）；没有或失效时
// 显示账号密码登录表单，走本服务的 /api/auth/login 校验 user 表并签发 token。

import { useEffect, useState } from 'react'
import { api, setUnauthorizedHandler } from '../api/client'
import { clearJwtCookie, isJwtValid, readJwtCookie, writeJwtCookie } from '../lib/auth'
import { LoginForm } from './LoginForm'

export interface Identity {
  user_id: number
  company_id: number
  name: string
}

interface Props {
  children: (identity: Identity & { onLogout: () => void }) => React.ReactNode
}

type State =
  | { kind: 'checking' }
  | { kind: 'authed'; identity: Identity }
  | { kind: 'unauthed' }

export function LoginGate({ children }: Props) {
  const [state, setState] = useState<State>({ kind: 'checking' })

  const check = async () => {
    setState({ kind: 'checking' })
    const token = readJwtCookie()
    if (!token || !isJwtValid(token)) {
      setState({ kind: 'unauthed' })
      return
    }
    try {
      const me = await api.me()
      setState({
        kind: 'authed',
        identity: { user_id: me.user_id, company_id: me.company_id, name: me.name ?? '' },
      })
    } catch {
      setState({ kind: 'unauthed' })
    }
  }

  const logout = () => {
    clearJwtCookie()
    setState({ kind: 'unauthed' })
  }

  useEffect(() => {
    setUnauthorizedHandler(() => setState({ kind: 'unauthed' }))
    check()
    return () => setUnauthorizedHandler(null)
  }, [])

  if (state.kind === 'checking') {
    return (
      <div className="flex h-screen items-center justify-center text-sm text-neutral-500">
        正在校验登录态…
      </div>
    )
  }

  if (state.kind === 'unauthed') {
    return (
      <LoginForm
        onSuccess={(token) => {
          writeJwtCookie(token)
          check()
        }}
      />
    )
  }

  return <>{children({ ...state.identity, onLogout: logout })}</>
}
