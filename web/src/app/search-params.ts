// The chat URL contract: /?models=<id>[,<id>]&q=<text>&temporary-chat=true&tools=<id>[,<id>]

export interface ChatParams {
  models: string[]
  q: string
  temporary: boolean
  tools: string[]
}

const list = (v: string | null) =>
  (v ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

export function parseChatParams(search: string | URLSearchParams): ChatParams {
  const p = typeof search === 'string' ? new URLSearchParams(search) : search
  return {
    models: p.getAll('models').flatMap(list),
    q: p.get('q') ?? '',
    temporary: ['true', '1', 'yes'].includes((p.get('temporary-chat') ?? '').toLowerCase()),
    tools: p.getAll('tools').flatMap(list),
  }
}

/** Removes one-shot params (q, tools) after they are consumed, keeping the rest. */
export function withoutOneShot(search: string): string {
  const p = new URLSearchParams(search)
  p.delete('q')
  p.delete('tools')
  const s = p.toString()
  return s ? `?${s}` : ''
}

/**
 * A same-origin path to return to after sign-in, else "/". Rejects absolute
 * and protocol-relative URLs, backslashes (browsers read "/\x" as "//x"),
 * control characters and /auth itself.
 */
export function safeRedirect(v: string | null | undefined): string {
  if (!v || !v.startsWith('/') || v.startsWith('//') || /[\\\u0000-\u001f\u007f]/.test(v)) return '/'
  return v === '/auth' || v.startsWith('/auth?') || v.startsWith('/auth#') || v.startsWith('/auth/') ? '/' : v
}
