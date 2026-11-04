export type Theme = 'light' | 'dark' | 'system'

export function getTheme(): Theme {
  try {
    const t = localStorage.getItem('theme')
    if (t === 'light' || t === 'dark') return t
    if (t && t.includes('dark')) return 'dark'
  } catch {
    // blocked storage
  }
  return 'system'
}

export function setTheme(t: Theme) {
  try {
    localStorage.setItem('theme', t)
  } catch {
    // still applied for this page
  }
  applyTheme(t)
}

export function applyTheme(t: Theme) {
  const el = document.documentElement
  if (t === 'system') delete el.dataset.theme
  else el.dataset.theme = t
}
