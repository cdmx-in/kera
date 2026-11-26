// The audit log's words: one sentence per event, built here from the action catalogue (tested in audit.test.ts).
// Every action the server records needs an entry below; internal/audit TestActionsHaveSentences checks that.

export type Outcome = 'ok' | 'denied' | 'failed'
export type ActorKind = 'user' | 'api_key' | 'service' | 'system' | 'anonymous'
export type Meta = Record<string, unknown>

export interface AuditEvent {
  id: string
  at: string
  actor_id: string
  actor_label: string
  actor_kind: ActorKind
  action: string
  target_type: string
  target_id: string
  target_label: string
  outcome: Outcome
  ip: string
  user_agent: string
  meta: Meta
}

export type Category = 'signin' | 'users' | 'secrets' | 'pipelines' | 'settings' | 'data' | 'other'

/** Filter choices, with the action prefixes each one asks the server for. */
export const categories: { key: Exclude<Category, 'other'>; label: string; prefixes: string[] }[] = [
  { key: 'signin', label: 'Sign-in', prefixes: ['auth.', 'apikey.'] },
  { key: 'users', label: 'Users and access', prefixes: ['user.', 'group.', 'permissions.', 'access.'] },
  { key: 'secrets', label: 'Secrets', prefixes: ['secret.'] },
  { key: 'pipelines', label: 'Pipelines', prefixes: ['pipeline.', 'runner.'] },
  {
    key: 'settings',
    label: 'Settings',
    prefixes: ['settings.', 'guardrails.', 'tool_server.', 'hook_server.', 'oauth_provider.', 'connection.', 'integration.', 'skill.', 'brand.', 'model.', 'config.apply', 'admin.'],
  },
  { key: 'data', label: 'Data', prefixes: ['knowledge.', 'chat.', 'memory.', 'audit.', 'config.export'] },
]

export function categoryOf(action: string): Category {
  return categories.find((c) => c.prefixes.some((p) => action.startsWith(p)))?.key ?? 'other'
}

/**
 * A phrase in the base form ("delete the stored secret {t}"): {t} is where the target goes. The sentence puts the
 * verb in the past ("deleted"), or after "tried to" when the outcome was not ok. extra adds detail after it.
 */
type Phrase = string | ((m: Meta, e: AuditEvent) => string)
type Entry = { say: Phrase; extra?: (m: Meta, e: AuditEvent) => string }

const str = (v: unknown) => (typeof v === 'string' ? v : '')
const list = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

/** "a", "a and b", "a, b and 2 more". */
export function names(items: string[], max = 3): string {
  if (items.length <= 1) return items[0] ?? ''
  if (items.length <= max) return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
  return `${items.slice(0, max - 1).join(', ')} and ${items.length - max + 1} more`
}

const people = (m: Meta) => {
  const n = list(m.users).length
  return n ? plural(n, 'person', 'people') : 'people'
}

const kinds: Record<string, string> = { model: 'model', knowledge: 'knowledge collection', tool_server: 'tool server', prompt: 'prompt', chat: 'chat', folder: 'folder', skill: 'skill' }

/** The catalogue. Keys are the server's action names. */
export const catalogue: Record<string, Entry> = {
  // sign-in and keys
  'auth.sign_in': { say: 'sign in', extra: (m) => (str(m.method) === 'password' ? ' with a password' : m.method ? ` through ${str(m.method)}` : '') },
  'auth.sign_in_failed': {
    say: (_m, e) => (e.target_id || e.target_label.includes('@') ? 'sign in as {t}' : e.target_label ? 'sign in with an unknown email' : 'sign in'),
    extra: (m) => (m.reason ? ` (${str(m.reason).replace(/_/g, ' ')})` : ''),
  },
  'auth.sign_out': { say: 'sign out' },
  'auth.password_change': { say: 'change their own password' },
  'auth.login_link': { say: 'make a sign-in link for {t}' },
  'apikey.create': { say: 'create the API key {t}' },
  'apikey.revoke': { say: (m) => (m.count != null ? 'revoke all their API keys' : 'revoke the API key {t}') },
  // users, groups, access
  'user.create': { say: 'add the user {t}', extra: (m) => (m.role ? ` as ${str(m.role)}` : '') },
  'user.update': { say: 'update the user {t}' },
  'user.rename': { say: 'rename the user {t}' },
  'user.role_change': { say: 'change the role of {t}', extra: (m) => (m.from && m.to ? ` from ${str(m.from)} to ${str(m.to)}` : m.to ? ` to ${str(m.to)}` : '') },
  'user.password_set': { say: 'set a new password for {t}', extra: (m) => (m.api_keys_revoked ? ' and revoked their API keys' : '') },
  'user.disable': { say: 'disable the user {t}', extra: (m) => (m.reason ? ` (${str(m.reason)})` : '') },
  'user.enable': { say: 'enable the user {t}' },
  'user.delete': { say: 'delete the user {t}' },
  'permissions.update': { say: 'change the default permissions' },
  'group.create': { say: 'create the group {t}' },
  'group.update': { say: 'update the group {t}' },
  'group.member_add': { say: (m) => `add ${people(m)} to the group {t}` },
  'group.member_remove': { say: (m) => `remove ${people(m)} from the group {t}` },
  'group.delete': { say: 'delete the group {t}' },
  'access.update': { say: (_m, e) => `change who can use the ${kinds[e.target_type] ?? e.target_type} {t}` },
  // settings and connected services
  'settings.update': { say: (m) => (list(m.settings).length === 1 ? 'change the setting {s}' : list(m.settings).length ? 'change the settings {s}' : 'change settings') },
  'guardrails.update': { say: 'change the guardrails', extra: (m) => (list(m.settings).length ? ` (${names(list(m.settings))})` : '') },
  'tool_server.update': { say: 'save the tool server {t}' },
  'tool_server.test': { say: 'test a tool server connection' },
  'tool_server.delete': { say: 'delete the tool server {t}' },
  'hook_server.update': { say: 'save the hook server {t}' },
  'hook_server.test': { say: 'test a hook server connection' },
  'hook_server.delete': { say: 'delete the hook server {t}' },
  'oauth_provider.update': { say: 'save the sign-in provider {t}' },
  'oauth_provider.test': { say: 'test a sign-in provider' },
  'oauth_provider.delete': { say: 'delete the sign-in provider {t}' },
  'connection.update': { say: (m) => (list(m.connections).length === 1 ? `save the model connection ${list(m.connections)[0]}` : `save ${plural(list(m.connections).length, 'model connection')}`) },
  'connection.verify': { say: 'refresh the models of the connection {t}' },
  'connection.test': { say: 'test a model connection' },
  'connection.delete': { say: 'delete the model connection {t}' },
  'integration.run': { say: 'start the integration {t}' },
  'skill.import': { say: 'import a skill' },
  'skill.update': { say: 'update the skill {t}' },
  'skill.delete': { say: 'delete the skill {t}' },
  'brand.asset_upload': { say: 'upload a brand image' },
  'brand.asset_delete': { say: 'delete the brand image {t}' },
  'model.create': { say: 'create the model {t}' },
  'model.update': { say: 'update the model {t}' },
  'model.toggle': { say: (m) => (m.active ? 'turn on the model {t}' : 'turn off the model {t}') },
  'model.delete': { say: 'delete the model {t}' },
  'config.apply': {
    say: 'apply the config file',
    extra: (m) => (typeof m.created === 'number' ? ` (${m.created} created, ${Number(m.updated)} updated, ${Number(m.deleted)} deleted)` : ''),
  },
  // stored secrets
  'secret.create': { say: 'create the stored secret {t}', extra: (m) => (m.copied_from ? ` from the variable ${str(m.copied_from)}` : '') },
  'secret.update': {
    say: (m) => {
      const c = list(m.changed)
      if (c.includes('value')) return c.includes('description') ? 'replace the value and description of the stored secret {t}' : 'replace the value of the stored secret {t}'
      return c.includes('description') ? 'edit the description of the stored secret {t}' : 'update the stored secret {t}'
    },
  },
  'secret.delete': { say: 'delete the stored secret {t}' },
  // pipelines and runners
  'runner.create': { say: 'add the runner {t}' },
  'runner.token_rotate': { say: 'rotate the token of the runner {t}' },
  'runner.delete': { say: 'remove the runner {t}' },
  'pipeline.create': { say: 'create the pipeline {t}' },
  'pipeline.update': {
    say: 'update the pipeline {t}',
    extra: (m) => (m.env_shared === true ? ' (its token may now read its variables)' : m.env_shared === false ? ' (its token may no longer read its variables)' : ''),
  },
  'pipeline.delete': { say: 'delete the pipeline {t}' },
  'pipeline.token_rotate': { say: 'rotate the token of the pipeline {t}' },
  'pipeline.override': { say: 'change the one-time overrides of the pipeline {t}', extra: (m) => (m.mass_delete ? ' (mass delete allowed)' : '') },
  'pipeline.schedule_change': { say: 'change the schedule of the pipeline {t}' },
  'pipeline.stages_update': { say: 'change the stages of the pipeline {t}' },
  'pipeline.script_save': { say: 'save the script of the pipeline {t}' },
  'pipeline.env_set': {
    say: (m) => (m.variable ? `set the variable ${str(m.variable)} of the pipeline {t}` : 'set a variable of the pipeline {t}'),
    extra: (m) => (m.secret_ref ? ` to the stored secret ${str(m.secret_ref)}` : ''),
  },
  'pipeline.env_read': { say: 'read the variables of the pipeline {t}' },
  'pipeline.env_delete': { say: (m) => (m.var ? `delete the variable ${str(m.var)} of the pipeline {t}` : 'delete a variable of the pipeline {t}') },
  'pipeline.check': { say: 'check the pipeline {t}' },
  'pipeline.run': { say: 'run the pipeline {t}' },
  'pipeline.run_cancel': { say: 'cancel a run of the pipeline {t}' },
  'pipeline.run_close': { say: 'close a run of the pipeline {t}' },
  'pipeline.notebook_start': { say: 'open a notebook on the pipeline {t}' },
  'pipeline.notebook_close': { say: 'close a notebook on the pipeline {t}' },
  'pipeline.notebook_run': { say: 'run a notebook cell of the pipeline {t}' },
  'pipeline.notebook_stop': { say: 'stop a notebook cell of the pipeline {t}' },
  // data
  'knowledge.delete': { say: 'delete the knowledge collection {t}' },
  'knowledge.file_remove': { say: 'remove a file from the knowledge collection {t}' },
  'chat.share': { say: 'share a chat' },
  'chat.unshare': { say: 'stop sharing a chat' },
  'memory.clear': { say: 'clear their memories' },
  'config.export': { say: 'export the config' },
  'audit.export': { say: 'export the audit log', extra: (m) => (typeof m.rows === 'number' ? ` (${plural(m.rows, 'event')})` : '') },
  // an admin route the catalogue does not name
  'admin.request': { say: 'make an admin change', extra: (m) => (m.route ? ` (${str(m.method)} ${str(m.route)})` : '') },
}

const irregular: Record<string, string> = { do: 'did', read: 'read', run: 'ran', set: 'set', stop: 'stopped', make: 'made', cancel: 'cancelled', open: 'opened' }

/** The past tense of a base-form phrase: its first word changes. */
export function past(phrase: string): string {
  const [verb, ...rest] = phrase.split(' ')
  const done = irregular[verb] ?? (verb.endsWith('e') ? verb + 'd' : /[^aeiou]y$/.test(verb) ? verb.slice(0, -1) + 'ied' : verb + 'ed')
  return [done, ...rest].join(' ')
}

/** Who did it, as the sentence names them. */
export function actorName(e: AuditEvent): string {
  if (e.actor_kind === 'anonymous') return 'Someone'
  return e.actor_label || e.actor_id || (e.actor_kind === 'system' ? 'The command line' : 'Kera')
}

/** What it was done to, as the sentence names it. */
export const targetName = (e: AuditEvent) => e.target_label || e.target_id

export interface Part {
  text: string
  /** actor or target: shown stronger */
  strong?: boolean
}

/** The event as sentence parts: actor, verb, target, detail. */
export function describe(e: AuditEvent): Part[] {
  const m = e.meta ?? {}
  const entry = catalogue[e.action]
  let phrase: string
  let extra = ''
  if (entry) {
    phrase = typeof entry.say === 'string' ? entry.say : entry.say(m, e)
    extra = entry.extra?.(m, e) ?? ''
  } else {
    // unknown action (a newer server): name it as it is
    phrase = `do ${e.action}` + (targetName(e) ? ' on {t}' : '')
  }
  phrase = phrase.replace('{s}', names(list(m.settings)))
  const verb = e.outcome === 'ok' ? past(phrase) : `tried to ${phrase}`
  const [before, after = ''] = verb.split('{t}')
  const parts: Part[] = [{ text: actorName(e), strong: e.actor_kind !== 'anonymous' }, { text: ' ' + before }]
  if (verb.includes('{t}')) parts.push({ text: targetName(e) || 'something', strong: true }, { text: after })
  if (extra) parts.push({ text: extra })
  return parts.filter((p) => p.text)
}

/** The event as one plain sentence. */
export const sentence = (e: AuditEvent) => describe(e).map((p) => p.text).join('').replace(/\s+/g, ' ').trim()

const secretWords = ['password', 'passwd', 'pwd', 'secret', 'token', 'key', 'credential', 'cookie', 'authorization', 'dsn', 'value', 'content', 'body', 'message', 'prompt', 'text']

/** Same rule as the server (internal/audit secretKey): a key that would hold a value is never shown. */
export function secretKey(k: string): boolean {
  const s = k.toLowerCase().replace(/s$/, '')
  return secretWords.some((w) => s.endsWith(w))
}

/** Meta as label/value rows for the detail panel: names only, secret-looking keys left out. */
export function metaRows(m: Meta): [string, string][] {
  return Object.entries(m ?? {})
    .filter(([k]) => !secretKey(k))
    .map(([k, v]) => [k.replace(/_/g, ' '), Array.isArray(v) ? v.join(', ') || '(none)' : typeof v === 'boolean' ? (v ? 'yes' : 'no') : typeof v === 'object' && v ? JSON.stringify(v) : String(v)])
}

const pad = (n: number) => String(n).padStart(2, '0')
const clock = (d: Date) => `${pad(d.getHours())}:${pad(d.getMinutes())}`
const dayStart = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()

/** "just now", "12 min ago", "10:32 today", "10:32 yesterday", "Tue 10:32", "3 Nov, 10:32", "3 Nov 2025, 10:32" (local time). */
export function when(iso: string, now = new Date()): string {
  const d = new Date(iso)
  const s = (now.getTime() - d.getTime()) / 1000
  if (s >= 0 && s < 60) return 'just now'
  if (s >= 0 && s < 3600) return `${Math.floor(s / 60)} min ago`
  const days = Math.round((dayStart(now) - dayStart(d)) / 86400_000)
  if (days === 0) return `${clock(d)} today`
  if (days === 1) return `${clock(d)} yesterday`
  if (days > 1 && days < 7) return `${d.toLocaleDateString('en-GB', { weekday: 'short' })} ${clock(d)}`
  const opts: Intl.DateTimeFormatOptions = { day: 'numeric', month: 'short', ...(d.getFullYear() !== now.getFullYear() && { year: 'numeric' }) }
  return `${d.toLocaleDateString('en-GB', opts)}, ${clock(d)}`
}

/** The exact time, for the tooltip and the detail panel. */
export const exactTime = (iso: string) => new Date(iso).toLocaleString('en-GB', { dateStyle: 'full', timeStyle: 'long' })

export interface Filters {
  actor: string
  category: string
  outcome: string
  from: string
  to: string
  q: string
}

export const outcomeOptions = [
  { value: '', label: 'Any outcome' },
  { value: 'denied,failed', label: 'Refused or failed' },
  { value: 'denied', label: 'Refused' },
  { value: 'failed', label: 'Failed' },
  { value: 'ok', label: 'Succeeded' },
]

/** The query string the list and the export share. Days are local: they become the instants of local midnight. */
export function auditQuery(f: Filters): URLSearchParams {
  const p = new URLSearchParams()
  if (f.actor.trim()) p.set('actor', f.actor.trim())
  const cat = categories.find((c) => c.key === f.category)
  if (cat) p.set('action', cat.prefixes.join(','))
  if (f.outcome) p.set('outcome', f.outcome)
  if (f.from) p.set('from', new Date(`${f.from}T00:00:00`).toISOString())
  if (f.to) {
    const end = new Date(`${f.to}T00:00:00`)
    end.setDate(end.getDate() + 1)
    p.set('to', end.toISOString())
  }
  if (f.q.trim()) p.set('q', f.q.trim())
  return p
}
