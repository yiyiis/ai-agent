// 登录态 token 的读写。
// 本应用把自己签发的 token 写在 `agents_token` 里，兼容读取 `jwt_token`。

const OWN_COOKIE = 'agents_token'
const UPSTREAM_COOKIE = 'jwt_token'

/** 取出所有同名 cookie 的值——同名可能出现多次（父域一份、本域一份）。 */
function readCookieValues(name: string): string[] {
  if (typeof document === 'undefined') return []
  return document.cookie
    .split(/;\s*/)
    .filter((s) => s.startsWith(`${name}=`))
    .map((s) => decodeURIComponent(s.slice(name.length + 1)))
    .filter(Boolean)
}

/**
 * 返回当前可用的 token：本应用自己种的优先，再回退中台跨子域种的。
 *
 * 挑的是第一个**有效**的（而不是第一个存在的），这样即便将来又出现同名冲突，
 * 过期的那份也不会把有效的挡住。
 */
export function readJwtCookie(): string | null {
  for (const name of [OWN_COOKIE, UPSTREAM_COOKIE]) {
    for (const token of readCookieValues(name)) {
      if (isJwtValid(token)) return token
    }
  }
  return null
}

// 只在当前站点种（不指定 domain），与中台父域的 cookie 互不覆盖。
export function writeJwtCookie(token: string, maxAgeSeconds = 7 * 24 * 3600) {
  if (typeof document === 'undefined') return
  document.cookie = `${OWN_COOKIE}=${encodeURIComponent(token)}; path=/; max-age=${maxAgeSeconds}; SameSite=Lax`
}

// 只清自己的。中台那份在父域，这里既清不掉、也不该清——清了会连带影响用户在中台的登录态。
export function clearJwtCookie() {
  if (typeof document === 'undefined') return
  document.cookie = `${OWN_COOKIE}=; path=/; max-age=0; SameSite=Lax`
}

export interface JwtPayload {
  Uid?: number
  CompanyId?: number
  exp?: number
}

// 仅用于前端展示 / 判断登录态，不做签名校验（后端会校验）
export function decodeJwtPayload(token: string): JwtPayload | null {
  try {
    const parts = token.split('.')
    if (parts.length !== 3) return null
    const payload = parts[1].replace(/-/g, '+').replace(/_/g, '/')
    const pad = payload.length % 4 === 0 ? '' : '='.repeat(4 - (payload.length % 4))
    const json = atob(payload + pad)
    return JSON.parse(decodeURIComponent(escape(json)))
  } catch {
    return null
  }
}

export function isJwtValid(token: string | null): boolean {
  if (!token) return false
  const p = decodeJwtPayload(token)
  if (!p) return false
  if (typeof p.exp === 'number' && p.exp * 1000 < Date.now()) return false
  return true
}
