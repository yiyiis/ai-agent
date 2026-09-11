export type Theme = 'light' | 'dark' | 'system'

const THEME_KEY = 'ai_agent_theme'

export function getStoredTheme(): Theme {
  const t = localStorage.getItem(THEME_KEY)
  if (t === 'light' || t === 'dark' || t === 'system') return t
  return 'system'
}

export function applyTheme(theme: Theme) {
  const root = document.documentElement
  const isDark =
    theme === 'dark' ||
    (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)

  if (isDark) {
    root.classList.add('dark')
  } else {
    root.classList.remove('dark')
  }
}

export function setTheme(theme: Theme) {
  localStorage.setItem(THEME_KEY, theme)
  applyTheme(theme)
}

export function initTheme() {
  applyTheme(getStoredTheme())

  // 监听系统主题变化（仅在用户选择 system 时响应）
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
    if (getStoredTheme() === 'system') {
      if (e.matches) {
        document.documentElement.classList.add('dark')
      } else {
        document.documentElement.classList.remove('dark')
      }
    }
  })
}
