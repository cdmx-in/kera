import { uiConfig } from './ui-config'

export type Theme = 'light' | 'dark' | 'system'

/** The stored choice (key "theme", shared with the server's inline script), else the brand default. */
export function getTheme(): Theme {
  try {
    const t = localStorage.getItem('theme')
    if (t === 'light' || t === 'dark' || t === 'system') return t
    if (t && t.includes('dark')) return 'dark'
  } catch {
    // blocked storage
  }
  return uiConfig().brand.defaultTheme ?? 'system'
}

export function setTheme(t: Theme) {
  try {
    localStorage.setItem('theme', t)
  } catch {
    // still applied for this page
  }
  applyTheme(t)
}

const systemDark = () => typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: dark)').matches

/** data-theme pins light/dark; the "dark" class always reflects what is shown. */
export function applyTheme(t: Theme) {
  const el = document.documentElement
  if (t === 'system') delete el.dataset.theme
  else el.dataset.theme = t
  el.classList.toggle('dark', t === 'dark' || (t === 'system' && systemDark()))
}

/** Applies the theme now and follows OS changes while it is "system". */
export function initTheme() {
  applyTheme(getTheme())
  if (typeof matchMedia !== 'function') return
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (getTheme() === 'system') applyTheme('system')
  })
}
