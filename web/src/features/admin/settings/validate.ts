// Form rules for the server editor dialogs. Pure functions: field name ->
// message (empty object = valid). Field names are the JSON keys the server
// names in its {detail, code, field} errors; the Go side (internal/api,
// internal/admin, internal/models) enforces the same rules.
import type { Connection, HookServer, OAuthProvider, ToolServer } from '../api'

export type Errors = Record<string, string>

const ID_RE = /^[a-z0-9][a-z0-9._:-]*$/
const HEADER_RE = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/

/** Lowercase letters, digits, . _ : - ; unique among existing ids. Only new rows are checked. */
export function idError(id: string, isNew: boolean, taken: string[], optional = false): string | undefined {
  if (!isNew) return
  if (!id) return optional ? undefined : 'ID is required'
  if (!ID_RE.test(id)) return 'Use lowercase letters, digits and . _ : - only'
  if (taken.includes(id)) return 'This ID is already used'
}

/** http(s) URL with a host and no surrounding spaces. */
export function urlError(s: string, required = true): string | undefined {
  if (!s) return required ? 'URL is required' : undefined
  if (s.trim() !== s) return 'Remove the spaces around the URL'
  let u: URL
  try {
    u = new URL(s)
  } catch {
    return 'Must be an http(s) URL with a host, e.g. http://host:8080'
  }
  if ((u.protocol !== 'http:' && u.protocol !== 'https:') || !u.hostname) return 'Must be an http(s) URL with a host, e.g. http://host:8080'
}

function put(e: Errors, field: string, msg: string | undefined | false) {
  if (msg && !e[field]) e[field] = msg
}

const isInt = (n: number) => Number.isInteger(n)

export interface Ctx {
  isNew: boolean
  /** ids already in the list (uniqueness on add) */
  taken: string[]
}

export function validateConnection(c: Pick<Connection, 'id' | 'kind' | 'base_url' | 'prefix' | 'sort'>, ctx: Ctx): Errors {
  const e: Errors = {}
  put(e, 'id', idError(c.id, ctx.isNew, ctx.taken, true)) // empty: the server generates one
  put(e, 'base_url', urlError(c.base_url))
  if (!e.base_url && c.kind === 'ollama' && /\/v1\/?$/.test(c.base_url)) put(e, 'base_url', 'An Ollama URL has no /v1 (e.g. http://host:11434); use the OpenAI-compatible kind for /v1')
  put(e, 'prefix', /[ .]/.test(c.prefix) && 'Prefix must not contain spaces or dots')
  put(e, 'sort', !isInt(c.sort) && 'Must be a whole number')
  return e
}

export function validateToolServer(t: Pick<ToolServer, 'id' | 'kind' | 'url' | 'spec_path' | 'auth' | 'max_result_chars'>, key: string | null, ctx: Ctx & { providers: string[] }): Errors {
  const e: Errors = {}
  put(e, 'id', idError(t.id, ctx.isNew, ctx.taken))
  put(e, 'url', urlError(t.url))
  if (t.kind === 'openapi' && (/:\/\//.test(t.spec_path) || /\s/.test(t.spec_path))) put(e, 'spec_path', 'Use a path relative to the URL, e.g. openapi.json')
  put(e, 'max_result_chars', (!isInt(t.max_result_chars) || t.max_result_chars < 0) && 'Must be 0 (no limit) or more')
  const a = t.auth
  const header = a.header.trim()
  if (a.type === 'header' && !header) put(e, 'auth.header', 'Header name is required for header auth')
  if ((a.type === 'header' || a.type === 'user_oauth') && header && !HEADER_RE.test(header)) put(e, 'auth.header', 'Not a valid header name (letters, digits and - only, e.g. X-API-Key)')
  if (a.type === 'user_oauth') {
    if (!a.provider) put(e, 'auth.provider', 'Choose the OAuth provider users sign in with')
    else if (!ctx.providers.includes(a.provider)) put(e, 'auth.provider', `Unknown OAuth provider ${a.provider}`)
  }
  // a key is needed unless one is stored and kept (null = keep, '' = clear)
  const needsKey = a.type === 'bearer' || a.type === 'header' || (a.type === 'user_oauth' && !!header)
  if (needsKey && !(key || (key === null && a.has_key))) put(e, 'auth.key', a.type === 'user_oauth' ? 'A key is required with a header name' : `A key is required for ${a.type} auth`)
  return e
}

export function validateHook(h: Pick<HookServer, 'id' | 'url' | 'events' | 'priority' | 'timeout_ms' | 'scope' | 'model_ids'>, config: string, ctx: Ctx): Errors {
  const e: Errors = {}
  put(e, 'id', idError(h.id, ctx.isNew, ctx.taken))
  put(e, 'url', urlError(h.url))
  if (!h.events.length) put(e, 'events', 'Pick inlet, outlet or both')
  else put(e, 'events', h.events.some((x) => x !== 'inlet' && x !== 'outlet') && 'Events are inlet and outlet')
  put(e, 'priority', !isInt(h.priority) && 'Must be a whole number')
  put(e, 'timeout_ms', (!isInt(h.timeout_ms) || h.timeout_ms < 1000 || h.timeout_ms > 120000) && 'Timeout must be 1000 to 120000 ms')
  put(e, 'model_ids', h.scope === 'models' && !h.model_ids.length && 'Pick at least one model, or apply to all models')
  put(e, 'config', configError(config))
  return e
}

function configError(text: string): string | undefined {
  if (!text.trim()) return
  try {
    const v = JSON.parse(text)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) return 'Config must be a JSON object'
  } catch (err) {
    return `Not valid JSON: ${(err as Error).message}`
  }
}

export function validateProvider(p: Pick<OAuthProvider, 'id' | 'login' | 'issuer' | 'authorization_endpoint' | 'token_endpoint' | 'userinfo_endpoint' | 'revocation_endpoint' | 'client_id' | 'scope'>, ctx: Ctx): Errors {
  const e: Errors = {}
  put(e, 'id', idError(p.id, ctx.isNew, ctx.taken))
  put(e, 'client_id', !p.client_id.trim() && 'Client ID is required')
  for (const f of ['issuer', 'authorization_endpoint', 'token_endpoint', 'userinfo_endpoint', 'revocation_endpoint'] as const) put(e, f, urlError(p[f], false))
  const googleDefaults = p.login && !p.authorization_endpoint && !p.token_endpoint
  const hint = p.login ? 'Set both the authorization and token endpoints, or neither for Google' : 'Required: set both the authorization and token endpoints'
  if (!googleDefaults) {
    put(e, 'authorization_endpoint', !p.authorization_endpoint && hint)
    put(e, 'token_endpoint', !p.token_endpoint && hint)
  }
  const scope = p.scope.trim()
  if (scope.includes(',')) put(e, 'scope', 'Separate scopes with spaces, not commas')
  else if (p.login && scope && !scope.split(/\s+/).some((s) => s === 'openid' || s.includes('email'))) put(e, 'scope', 'Sign-in needs the openid or an email scope')
  return e
}
