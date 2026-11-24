// Pure helpers for the admin pages (tested in lib.test.ts).

export type Perms = Record<string, Record<string, boolean>>

/** "max_tool_iterations" -> "Max tool iterations"; "api_keys" -> "API keys". */
export function humanize(key: string): string {
  const s = key
    .replace(/[._]/g, ' ')
    .replace(/\bapi\b/gi, 'API')
    .replace(/\burl\b/gi, 'URL')
    .replace(/\bid(s?)\b/gi, 'ID$1')
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** Categories and leaves of the known permission tree, sorted, values from `value` (missing = false). */
export function permRows(known: Perms, value: Perms): { cat: string; leaves: { key: string; on: boolean }[] }[] {
  return Object.keys(known)
    .sort()
    .map((cat) => ({
      cat,
      leaves: Object.keys(known[cat] ?? {})
        .sort()
        .map((key) => ({ key, on: !!value?.[cat]?.[key] })),
    }))
}

/** Returns a copy of p with cat.key set. */
export function setPerm(p: Perms, cat: string, key: string, on: boolean): Perms {
  return { ...p, [cat]: { ...(p?.[cat] ?? {}), [key]: on } }
}

const isoDay = (d: Date) => d.toISOString().slice(0, 10)

/** First and last day of now's UTC month (YYYY-MM-DD). */
export function monthRange(now = new Date()): [string, string] {
  const y = now.getUTCFullYear()
  const m = now.getUTCMonth()
  return [isoDay(new Date(Date.UTC(y, m, 1))), isoDay(new Date(Date.UTC(y, m + 1, 0)))]
}

/** The n UTC days up to and including today. */
export function lastDays(n: number, now = new Date()): [string, string] {
  const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()))
  return [isoDay(new Date(end.getTime() - (n - 1) * 86400_000)), isoDay(end)]
}

/** "today", "3 d ago" for an epoch-seconds timestamp ("–" when unset). */
export function ago(ts: number | null | undefined, nowMs = Date.now()): string {
  if (!ts) return '–'
  const s = Math.max(0, nowMs / 1000 - ts)
  if (s < 3600) return `${Math.max(1, Math.floor(s / 60))} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  return `${Math.floor(s / 86400)} d ago`
}

export const num = (v: number) => v.toLocaleString('en-US')
export const usd = (v: number) => '$' + v.toFixed(v < 1 ? 4 : 2)

/** Short token counts: 1234 -> "1.2k", 3_400_000 -> "3.4M". */
export function compact(v: number): string {
  if (v < 1000) return String(v)
  if (v < 1_000_000) return `${(v / 1000).toFixed(v < 10_000 ? 1 : 0)}k`
  return `${(v / 1_000_000).toFixed(1)}M`
}

/** Moves the item at from to index to (a copy). */
export function move<T>(list: T[], from: number, to: number): T[] {
  if (from === to || from < 0 || to < 0 || from >= list.length || to >= list.length) return list
  const out = [...list]
  const [x] = out.splice(from, 1)
  out.splice(to, 0, x)
  return out
}

/** One entry per non-empty line or comma-separated item. */
export function parseList(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

// ---- spend ---------------------------------------------------------------------

export interface SpendData {
  /** false when no spend source is set (rows and users are empty then) */
  configured?: boolean
  start_date: string
  end_date: string
  rows: { end_user: string; model: string; day: string; spend: number; prompt_tokens: number; completion_tokens: number; requests: number }[]
  users: { id: string; name: string; email: string; last_active_at: number }[]
}

export interface SpendPerson {
  id: string | null
  name: string
  email: string
  last: number | null
  spend: number
  requests: number
  tokens: number
}

export interface SpendView {
  total: number
  users: number
  active: number
  idle: number
  people: SpendPerson[]
  days: { day: string; spend: number; requests: number }[]
  models: { model: string; spend: number; requests: number; input: number; output: number }[]
}

/** Folds spend rows into per-person, per-day and per-model totals. LiteLLM's end_user is the email. */
export function spendView(d: SpendData): SpendView {
  const byUser = new Map<string, { spend: number; requests: number; tokens: number }>()
  const byModel = new Map<string, { spend: number; requests: number; input: number; output: number }>()
  const byDay = new Map<string, { spend: number; requests: number }>()
  for (const r of d.rows) {
    const u = byUser.get(r.end_user) ?? { spend: 0, requests: 0, tokens: 0 }
    u.spend += r.spend
    u.requests += r.requests
    u.tokens += r.prompt_tokens + r.completion_tokens
    byUser.set(r.end_user, u)
    const m = byModel.get(r.model) ?? { spend: 0, requests: 0, input: 0, output: 0 }
    m.spend += r.spend
    m.requests += r.requests
    m.input += r.prompt_tokens
    m.output += r.completion_tokens
    byModel.set(r.model, m)
    const d0 = byDay.get(r.day) ?? { spend: 0, requests: 0 }
    byDay.set(r.day, { spend: d0.spend + r.spend, requests: d0.requests + r.requests })
  }
  const zero = { spend: 0, requests: 0, tokens: 0 }
  const seen = new Set<string>()
  const people: SpendPerson[] = d.users.map((u) => {
    seen.add(u.email)
    return { id: u.id, name: u.name, email: u.email, last: u.last_active_at || null, ...(byUser.get(u.email) ?? zero) }
  })
  // Spend by someone who is not (or no longer) a user, or '' = unattributed (embeddings).
  for (const [email, s] of byUser) {
    if (!seen.has(email)) people.push({ id: null, name: email || 'Unattributed (embeddings)', email, last: null, ...s })
  }
  const total = people.reduce((a, p) => a + p.spend, 0)
  // free models (Ollama) cost nothing, so someone who used them is active all the same
  const active = people.filter((p) => p.spend > 0 || p.requests > 0).length
  return {
    total,
    users: d.users.length,
    active,
    idle: people.length - active,
    people,
    days: eachDay(d.start_date, d.end_date).map((day) => ({ day, ...(byDay.get(day) ?? { spend: 0, requests: 0 }) })),
    models: [...byModel].map(([model, m]) => ({ model, ...m })).sort((a, b) => b.spend - a.spend),
  }
}

/** Every YYYY-MM-DD from start to end inclusive (UTC). */
export function eachDay(start: string, end: string): string[] {
  const out: string[] = []
  for (let t = Date.parse(start + 'T00:00:00Z'), e = Date.parse(end + 'T00:00:00Z'); t <= e && out.length < 400; t += 86400_000) out.push(isoDay(new Date(t)))
  return out
}

export type SpendLevel = 'high' | 'medium' | 'light' | 'none'

/** Usage level by spend in USD: High > 10, Medium > 5, Light > 0 or any request (free models cost nothing). */
export function spendLevel(spend: number, requests = 0): SpendLevel {
  if (spend > 10) return 'high'
  if (spend > 5) return 'medium'
  if (spend > 0 || requests > 0) return 'light'
  return 'none'
}

export type SortKey = 'name' | 'last' | 'requests' | 'tokens' | 'spend'

export function sortPeople(people: SpendPerson[], key: SortKey, desc: boolean): SpendPerson[] {
  const val = (p: SpendPerson) => (key === 'name' ? p.name.toLowerCase() : (p[key] ?? -1))
  return [...people].sort((a, b) => {
    const x = val(a)
    const y = val(b)
    return (x < y ? -1 : x > y ? 1 : 0) * (desc ? -1 : 1)
  })
}

// ---- access -------------------------------------------------------------------

export interface GrantLike {
  principal: string
  permission: 'read' | 'write'
}

/** One row per principal; write implies read, so the higher permission wins. */
export function collapse(grants: GrantLike[]): GrantLike[] {
  const by = new Map<string, GrantLike>()
  for (const g of grants) {
    const cur = by.get(g.principal)
    if (!cur || g.permission === 'write') by.set(g.principal, { principal: g.principal, permission: g.permission })
  }
  return [...by.values()].sort((a, b) => order(a.principal) - order(b.principal) || a.principal.localeCompare(b.principal))
}

const order = (p: string) => (p === 'user:*' ? 0 : p.startsWith('group:') ? 1 : 2)

/** Number of pages for total rows (at least 1, so an empty table is "page 1 of 1"). */
export const pageCount = (total: number, size: number) => Math.max(1, Math.ceil(total / size))

/** page clamped to [1, pageCount]: a shrinking total (a delete, a narrower search) never strands you past the end. */
export const clampPage = (page: number, total: number, size: number) => Math.min(Math.max(1, Math.floor(page) || 1), pageCount(total, size))

/** The rows of one page (client-side paging), after clamping the page. */
export function pageSlice<T>(rows: T[], page: number, size: number): T[] {
  const p = clampPage(page, rows.length, size)
  return rows.slice((p - 1) * size, p * size)
}

/** The page that keeps the first visible row in view after the page size changes. */
export const pageForSize = (page: number, size: number, next: number) => Math.floor(((page - 1) * size) / next) + 1

/**
 * Page buttons: first, last and the pages around the current one, null for an
 * ellipsis. Always 7 slots once there are more than 7 pages, so the bar never jumps.
 */
export function pageRange(page: number, pages: number): (number | null)[] {
  if (pages <= 7) return Array.from({ length: pages }, (_, i) => i + 1)
  const lo = Math.max(2, Math.min(page - 1, pages - 4))
  const hi = Math.min(pages - 1, Math.max(page + 1, 5))
  const out: (number | null)[] = [1]
  if (lo > 2) out.push(null)
  for (let i = lo; i <= hi; i++) out.push(i)
  if (hi < pages - 1) out.push(null)
  out.push(pages)
  return out
}

export const NAME_MAX = 100
export const DESCRIPTION_MAX = 500

/** Field name -> message; empty means valid. */
export type FieldErrors = Partial<Record<'name' | 'description' | 'role' | 'permissions', string>>

function nameError(name: string, what: string): string | undefined {
  const n = name.trim()
  if (!n) return `${what} is required`
  if (n.length > NAME_MAX) return `${what} is too long (${n.length}/${NAME_MAX} characters)`
}

/** The edit-user dialog: a name, and a role that is not your own. */
export function validateUser(f: { name: string; role: string }, was: { role: string; self: boolean }): FieldErrors {
  const e: FieldErrors = {}
  const name = nameError(f.name, 'Name')
  if (name) e.name = name
  if (was.self && f.role !== was.role) e.role = 'You cannot change your own role'
  return e
}

/** The group dialog: a name unique among the other groups (ignoring case), a short description. */
export function validateGroup(f: { name: string; description: string }, groups: { id: string; name: string }[], id?: string): FieldErrors {
  const e: FieldErrors = {}
  const name = nameError(f.name, 'Name') ?? (groups.some((g) => g.id !== id && g.name.trim().toLowerCase() === f.name.trim().toLowerCase()) ? 'A group with that name already exists' : undefined)
  if (name) e.name = name
  if (f.description.length > DESCRIPTION_MAX) e.description = `Description is too long (${f.description.length}/${DESCRIPTION_MAX} characters)`
  return e
}

/** The dialog field an API error code belongs to (server-side validation shown inline). */
export function errorField(code?: string): keyof FieldErrors | undefined {
  if (code === 'bad_name' || code === 'duplicate') return 'name'
  if (code === 'bad_role' || code === 'own_role' || code === 'last_admin') return 'role'
  if (code === 'bad_permissions') return 'permissions'
}

const groupIcons: [RegExp, string][] = [
  [/admin|owner|root|super/i, 'shield'],
  [/dev|eng|code|tech|platform/i, 'code'],
  [/qa|test|quality/i, 'flask'],
  [/design|ux|\bui\b|creative/i, 'palette'],
  [/sales|business|account/i, 'briefcase'],
  [/support|help|service|care/i, 'lifebuoy'],
  [/financ|billing|pay|money/i, 'wallet'],
  [/\bhr\b|people|talent|recruit/i, 'heart'],
  [/market|growth|brand/i, 'megaphone'],
  [/ops|infra|sre|\bit\b/i, 'server'],
  [/data|analyt|research|science/i, 'chart'],
  [/manage|lead|exec|director/i, 'crown'],
]

/** An icon name for a group, guessed from its name; "users" when nothing matches. */
export const groupIcon = (name: string) => groupIcons.find(([re]) => re.test(name))?.[1] ?? 'users'

/** "Workspace models"-style labels for the permissions a group turns on. */
export const permChips = (p: Perms | null | undefined) =>
  Object.entries(p ?? {}).flatMap(([cat, leaves]) => Object.entries(leaves ?? {}).filter(([, on]) => on).map(([key]) => humanize(`${cat} ${key}`)))
