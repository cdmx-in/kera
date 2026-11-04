export function formatBytes(n?: number): string {
  if (n == null || !Number.isFinite(n)) return ''
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

// "domain--portals-and-modules.md" -> "Domain – Portals and modules"
export function prettyFileName(name: string): string {
  const base = name.replace(/\.[a-z0-9]{1,5}$/i, '')
  const t = base.split('--').map((p) => p.replace(/[-_]+/g, ' ').trim()).filter(Boolean).map((p) => p[0].toUpperCase() + p.slice(1)).join(' – ')
  return t || name
}

export function fileExt(name: string): string {
  const i = name.lastIndexOf('.')
  return i > 0 ? name.slice(i + 1).toLowerCase() : ''
}

export function domainOf(url?: string): string {
  if (!url) return ''
  try {
    return new URL(url).hostname.replace(/^www\./, '')
  } catch {
    return ''
  }
}

export function cx(...c: (string | false | null | undefined)[]): string {
  return c.filter(Boolean).join(' ')
}

/** Short random id for client-side message ids. */
export function newId(): string {
  return crypto.randomUUID()
}

export function storageGet(store: 'local' | 'session', key: string): string | null {
  try {
    return (store === 'local' ? localStorage : sessionStorage).getItem(key)
  } catch {
    return null
  }
}

export function storageSet(store: 'local' | 'session', key: string, value: string | null) {
  try {
    const s = store === 'local' ? localStorage : sessionStorage
    if (value == null || value === '') s.removeItem(key)
    else s.setItem(key, value)
  } catch {
    // storage blocked or full: drafts and preferences are best effort
  }
}
