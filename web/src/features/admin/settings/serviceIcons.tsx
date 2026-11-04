// Icons for OAuth providers and tool servers, derived from what they are
// (issuer, endpoints, scopes, kind), so any deployment gets sensible marks.
import type { ReactNode } from 'react'
import { Braces, KeyRound, Plug } from 'lucide-react'

type Mark = 'google' | 'sheets' | 'drive' | 'microsoft' | 'github' | 'key'

/** Picks the mark for a provider: Google APIs by scope first (Sheets, Drive), then the issuer's brand. */
export function providerMark(p: { issuer?: string; authorization_endpoint?: string; scope?: string; id?: string }): Mark {
  const where = `${p.issuer ?? ''} ${p.authorization_endpoint ?? ''}`.toLowerCase()
  const scope = (p.scope ?? '').toLowerCase()
  const google = where.includes('google') || (!where.trim() && /google/.test(p.id ?? '')) || scope.includes('googleapis.com')
  if (google && scope.includes('/auth/spreadsheets')) return 'sheets'
  if (google && /\/auth\/drive/.test(scope)) return 'drive'
  if (google) return 'google'
  if (where.includes('microsoft') || where.includes('login.live.com')) return 'microsoft'
  if (where.includes('github')) return 'github'
  return 'key'
}

const marks: Record<Mark, ReactNode> = {
  google: (
    <svg viewBox="0 0 48 48" aria-hidden className="size-5">
      <path fill="#EA4335" d="M24 9.5c3.5 0 6.6 1.2 9 3.5l6.7-6.7C35.6 2.4 30.2 0 24 0 14.6 0 6.6 5.4 2.6 13.2l7.8 6.1C12.3 13.6 17.7 9.5 24 9.5z" />
      <path fill="#4285F4" d="M46.5 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.7c-.6 3-2.3 5.5-4.8 7.2l7.5 5.8c4.4-4.1 7.1-10.1 7.1-17.5z" />
      <path fill="#FBBC05" d="M10.4 28.7c-.5-1.4-.8-3-.8-4.7s.3-3.2.8-4.7l-7.8-6.1C1 16.5 0 20.1 0 24s1 7.5 2.6 10.8l7.8-6.1z" />
      <path fill="#34A853" d="M24 48c6.5 0 11.9-2.1 15.9-5.8l-7.5-5.8c-2.1 1.4-4.8 2.3-8.4 2.3-6.3 0-11.7-4.1-13.6-9.8l-7.8 6.1C6.6 42.6 14.6 48 24 48z" />
    </svg>
  ),
  sheets: (
    <svg viewBox="0 0 24 24" aria-hidden className="size-5">
      <path fill="#0F9D58" d="M14.5 2H6.2C5 2 4 3 4 4.2v15.6C4 21 5 22 6.2 22h11.6c1.2 0 2.2-1 2.2-2.2V7.5L14.5 2z" />
      <path fill="#87CEAC" d="M14.5 2v4.3c0 .7.5 1.2 1.2 1.2H20L14.5 2z" />
      <path fill="#F1F1F1" d="M7.5 10.5v7h9v-7h-9zm1.2 1.2h2.7v1.7H8.7v-1.7zm0 2.9h2.7v1.7H8.7v-1.7zm6.6 1.7h-2.7v-1.7h2.7v1.7zm0-2.9h-2.7v-1.7h2.7v1.7z" />
    </svg>
  ),
  drive: (
    <svg viewBox="0 0 24 24" aria-hidden className="size-5">
      <path fill="#0066DA" d="M3.2 18.6l.9 1.5c.2.3.5.6.8.8L8 15.3H2c0 .4.1.8.3 1.1l.9 2.2z" />
      <path fill="#00AC47" d="M12 8.4L8.9 3.1c-.3.2-.6.5-.8.8L2.3 14.2c-.2.3-.3.7-.3 1.1h6L12 8.4z" />
      <path fill="#EA4335" d="M19.1 20.9c.3-.2.6-.5.8-.8l.4-.6 1.7-3c.2-.3.3-.7.3-1.1h-6l1.3 2.5 1.5 3z" />
      <path fill="#00832D" d="M12 8.4l3.1-5.3c-.3-.2-.7-.3-1.1-.3H10c-.4 0-.8.1-1.1.3L12 8.4z" />
      <path fill="#2684FC" d="M16 15.3H8l-3.1 5.6c.3.2.7.3 1.1.3h12c.4 0 .8-.1 1.1-.3L16 15.3z" />
      <path fill="#FFBA00" d="M19.1 8.8l-3-5c-.2-.3-.5-.6-.8-.8L12 8.4l4 6.9h6c0-.4-.1-.8-.3-1.1l-2.6-5.4z" />
    </svg>
  ),
  microsoft: (
    <svg viewBox="0 0 24 24" aria-hidden className="size-5">
      <path fill="#F25022" d="M3 3h8.5v8.5H3z" />
      <path fill="#7FBA00" d="M12.5 3H21v8.5h-8.5z" />
      <path fill="#00A4EF" d="M3 12.5h8.5V21H3z" />
      <path fill="#FFB900" d="M12.5 12.5H21V21h-8.5z" />
    </svg>
  ),
  github: (
    <svg viewBox="0 0 24 24" aria-hidden className="size-5 fill-current">
      <path d="M12 2a10 10 0 0 0-3.2 19.5c.5.1.7-.2.7-.5v-1.7c-2.8.6-3.4-1.3-3.4-1.3-.5-1.2-1.1-1.5-1.1-1.5-.9-.6.1-.6.1-.6 1 .1 1.5 1 1.5 1 .9 1.6 2.4 1.1 3 .9.1-.7.4-1.1.6-1.4-2.2-.3-4.6-1.1-4.6-5a3.9 3.9 0 0 1 1-2.7c-.1-.3-.5-1.3.1-2.7 0 0 .9-.3 2.8 1a9.6 9.6 0 0 1 5 0c1.9-1.3 2.8-1 2.8-1 .6 1.4.2 2.4.1 2.7a3.9 3.9 0 0 1 1 2.7c0 3.9-2.4 4.7-4.6 5 .4.3.7.9.7 1.8V21c0 .3.2.6.7.5A10 10 0 0 0 12 2z" />
    </svg>
  ),
  key: <KeyRound size={18} className="text-accent dark:text-accent-bright" aria-hidden />,
}

/** A square tile holding a mark, sized for list rows. */
export function IconTile({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span title={title} className="grid size-10 shrink-0 place-items-center rounded-xl border border-line bg-surface-2/60">
      {children}
    </span>
  )
}

export function ProviderIcon({ p }: { p: Parameters<typeof providerMark>[0] }) {
  const m = providerMark(p)
  return <IconTile title={m === 'key' ? 'OAuth provider' : m[0].toUpperCase() + m.slice(1)}>{marks[m]}</IconTile>
}

/** Tool servers: the sign-in provider's mark for per-user OAuth servers, else MCP / OpenAPI symbols. */
export function ToolServerIcon({ kind, provider }: { kind: string; provider?: Parameters<typeof providerMark>[0] }) {
  if (provider) {
    const m = providerMark(provider)
    if (m !== 'key') return <IconTile title={m}>{marks[m]}</IconTile>
  }
  return (
    <IconTile title={kind === 'mcp' ? 'MCP server' : 'OpenAPI server'}>
      {kind === 'mcp' ? <Plug size={18} className="text-accent dark:text-accent-bright" aria-hidden /> : <Braces size={18} className="text-accent dark:text-accent-bright" aria-hidden />}
    </IconTile>
  )
}
