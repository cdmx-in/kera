import { useEffect, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient, useQuery } from '@tanstack/react-query'
import { CircleAlert, FileLock2, Globe, Plus, Search, Trash2, User, Users as GroupIcon, type LucideIcon } from 'lucide-react'
import { api } from '@/api/client'
import { useMe } from '@/api/queries'
import { cx } from '@/lib/format'
import { IconButton } from '@/ui/Dialog'
import { toast } from '@/stores/toast'
import { Principal } from '@/features/admin/Access'
import { UserPicker } from '@/features/admin/UserPicker'
import { Badge, ManagedBadge } from '@/features/admin/ui'
import { useGroups, type Grant } from './api'
import { Select } from '@/ui/Select'

export { Badge, ManagedBadge }

export const input = 'w-full min-w-0 rounded-lg border border-line bg-surface px-3 py-2 text-sm outline-none transition-colors hover:border-accent/45 focus:border-accent focus-visible:ring-3 focus-visible:ring-accent-soft disabled:opacity-60 aria-[invalid=true]:border-red-500 aria-[invalid=true]:focus-visible:ring-red-500/20'
export const btn = 'inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium hover:bg-surface-2 disabled:opacity-50'
export const btnPrimary = 'inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white shadow-sm hover:opacity-90 disabled:opacity-50'
/** A whole card that is a link or button: hover lift, accent ring, keyboard focus ring. */
export const cardLink =
  'group flex h-full w-full flex-col rounded-2xl border border-line bg-surface p-4 text-left shadow-[0_1px_2px_rgb(0_0_0/0.04)] outline-none transition duration-150 hover:-translate-y-0.5 hover:border-accent/50 hover:shadow-pop hover:ring-1 hover:ring-accent/30 focus-visible:border-accent focus-visible:ring-3 focus-visible:ring-accent-soft motion-reduce:transition-none motion-reduce:hover:translate-y-0'

export function Field({ label, hint, error, children, className }: { label: string; hint?: ReactNode; error?: string; children: ReactNode; className?: string }) {
  return (
    <label className={cx('block space-y-1.5', className)}>
      <span className="block text-xs font-medium text-muted">{label}</span>
      {children}
      <FieldError error={error} />
      {hint && <span className="block text-xs leading-snug text-muted">{hint}</span>}
    </label>
  )
}

/** Inline error under a field. */
export function FieldError({ error }: { error?: string }) {
  if (!error) return null
  return (
    <span role="alert" className="flex items-start gap-1 text-xs font-medium text-red-600 dark:text-red-400" data-testid="field-error">
      <CircleAlert size={13} className="mt-px shrink-0" aria-hidden />
      {error}
    </span>
  )
}

/** Toolbar row of a list page: search, filters, then the create action on the right. */
export function Toolbar({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col gap-3 rounded-2xl border border-line bg-surface p-3 shadow-[0_1px_2px_rgb(0_0_0/0.04)] lg:flex-row lg:items-center">
      <div className="flex min-w-0 flex-1 flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center">{children}</div>
      {action && <div className="flex shrink-0 justify-end">{action}</div>}
    </div>
  )
}

/** A group heading with a count, above a card grid. */
export function GroupHead({ title, count, hint }: { title: string; count: number; hint?: string }) {
  return (
    <div className="mb-3 flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <h2 className="text-sm font-semibold tracking-tight">{title}</h2>
      <span className="rounded-full bg-surface-2 px-2 py-px text-xs font-medium tabular-nums text-muted">{count}</span>
      {hint && <span className="w-full text-xs text-muted sm:w-auto">{hint}</span>}
    </div>
  )
}

export const grid = 'grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4'

/** Header pinned to the top of the scroll area for editors: back link, identity, actions, tabs. */
export function EditorHeader({ back, icon, title, subtitle, actions, children }: { back: ReactNode; icon: ReactNode; title: ReactNode; subtitle?: ReactNode; actions?: ReactNode; children?: ReactNode }) {
  return (
    <div className="sticky top-0 z-20 -mx-4 -mt-4 border-b border-line bg-bg/90 px-4 pt-3 backdrop-blur sm:-mx-6 sm:-mt-6 sm:px-6 lg:-mx-8 lg:px-8" data-testid="editor-header">
      <div className="flex items-center gap-3 pb-2">
        {back}
        {icon}
        <div className="min-w-0 flex-1">
          <h2 className="flex min-w-0 items-center gap-1.5 text-base font-semibold sm:text-lg">{title}</h2>
          {subtitle && <p className="truncate text-xs text-muted">{subtitle}</p>}
        </div>
        {actions}
      </div>
      {children}
    </div>
  )
}

/** A rounded square holding a lucide icon in the accent colour. */
export function IconTile({ icon: Icon, size = 'md', className }: { icon: LucideIcon; size?: 'sm' | 'md' | 'lg'; className?: string }) {
  const box = { sm: 'size-8 rounded-lg', md: 'size-10 rounded-xl', lg: 'size-12 rounded-2xl' }[size]
  return (
    <span className={cx('grid shrink-0 place-items-center border border-line bg-accent-soft/60 text-accent dark:text-accent-bright', box, className)} aria-hidden>
      <Icon size={size === 'sm' ? 16 : size === 'md' ? 19 : 22} />
    </span>
  )
}

/** A card with an icon tile, title and optional hint in its header. */
export function Section({ title, icon, hint, children, aside, className }: { title: string; icon?: LucideIcon; hint?: ReactNode; children: ReactNode; aside?: ReactNode; className?: string }) {
  return (
    <section className={cx('rounded-2xl border border-line bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)]', className)}>
      <header className="flex flex-wrap items-center gap-3 rounded-t-2xl border-b border-line bg-surface-2/40 px-4 py-3 sm:px-5">
        {icon && <IconTile icon={icon} size="sm" />}
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold tracking-tight">{title}</h3>
          {hint && <p className="mt-0.5 text-xs leading-relaxed text-muted">{hint}</p>}
        </div>
        {aside}
      </header>
      <div className="space-y-4 p-4 sm:p-5">{children}</div>
    </section>
  )
}

/** A small rounded chip for card footers. */
export function Chip({ icon: Icon, children, title, tone = 'grey' }: { icon?: LucideIcon; children?: ReactNode; title?: string; tone?: 'grey' | 'accent' | 'green' | 'amber' | 'red' }) {
  const tones = {
    grey: 'border-line bg-surface-2/60 text-muted',
    accent: 'border-accent/25 bg-accent-soft text-accent dark:text-accent-bright',
    green: 'border-green-600/20 bg-green-500/10 text-green-800 dark:text-green-300',
    amber: 'border-amber-600/25 bg-amber-500/10 text-amber-800 dark:text-amber-300',
    red: 'border-red-600/25 bg-red-500/10 text-red-800 dark:text-red-300',
  }
  return (
    <span title={title} className={cx('inline-flex max-w-full items-center gap-1 whitespace-nowrap rounded-full border px-2 py-0.5 text-[11px] font-medium', tones[tone])}>
      {Icon && <Icon size={12} className="shrink-0" aria-hidden />}
      {children !== undefined && <span className="truncate">{children}</span>}
    </span>
  )
}

export function SearchBox({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  return (
    <div className="relative w-full sm:w-72">
      <Search size={15} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted" aria-hidden />
      <input type="search" className={cx(input, 'py-1.5 pl-9')} placeholder={label} value={value} onChange={(e) => onChange(e.target.value)} aria-label={label} />
    </div>
  )
}

/** Toggle chips that pick one filter. */
export function FilterChips<T extends string>({ options, value, onChange, counts }: { options: readonly (readonly [T, string])[]; value: T; onChange: (v: T) => void; counts?: Partial<Record<T, number>> }) {
  return (
    <div role="group" aria-label="Filter" className="flex flex-wrap gap-1.5">
      {options.map(([k, l]) => (
        <button
          key={k}
          type="button"
          aria-pressed={value === k}
          onClick={() => onChange(k)}
          data-testid={`filter-${k}`}
          className={cx(
            'inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors',
            value === k ? 'border-accent bg-accent text-white' : 'border-line bg-surface text-muted hover:border-accent/45 hover:text-fg',
          )}
        >
          {l}
          {counts?.[k] !== undefined && <span className={cx('tabular-nums', value === k ? 'text-white/80' : 'text-muted/80')}>{counts[k]}</span>}
        </button>
      ))}
    </div>
  )
}

/** The current tab from ?tab= (so a reload keeps it); the default tab leaves the URL clean. */
export function useTab<T extends string>(keys: readonly T[], def: T): [T, (t: T) => void] {
  const [params, setParams] = useSearchParams()
  const raw = params.get('tab') as T | null
  const tab = raw && keys.includes(raw) ? raw : def
  const set = (t: T) =>
    setParams(
      (p) => {
        const q = new URLSearchParams(p)
        if (t === def) q.delete('tab')
        else q.set('tab', t)
        return q
      },
      { replace: true },
    )
  return [tab, set]
}

export function TabBar<T extends string>({ tabs, value, onChange, label }: { tabs: readonly { key: T; label: string; icon: LucideIcon; badge?: ReactNode; error?: boolean }[]; value: T; onChange: (t: T) => void; label: string }) {
  return (
    <div role="tablist" aria-label={label} className="-mb-px flex gap-1 overflow-x-auto [scrollbar-width:none]">
      {tabs.map(({ key, label: l, icon: Icon, badge, error }) => (
        <button
          key={key}
          type="button"
          role="tab"
          aria-selected={value === key}
          onClick={() => onChange(key)}
          data-testid={`tab-${key}`}
          className={cx(
            'inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2.5 text-sm transition-colors',
            value === key ? 'border-accent font-medium text-fg' : 'border-transparent text-muted hover:text-fg',
          )}
        >
          <Icon size={15} aria-hidden className={value === key ? 'text-accent dark:text-accent-bright' : ''} />
          {l}
          {badge}
          {error && <span className="size-2 rounded-full bg-red-500" title="Has errors" aria-label="Has errors" data-testid="tab-error" />}
        </button>
      ))}
    </div>
  )
}

/** A styled empty or error state for a list. */
export function Empty({ icon, title, children, action }: { icon: LucideIcon; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center rounded-2xl border border-dashed border-line bg-surface-2/20 px-6 py-12 text-center">
      <IconTile icon={icon} size="lg" />
      <p className="mt-4 font-medium">{title}</p>
      {children && <p className="mt-1 max-w-md text-sm text-muted">{children}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

/** Card-shaped placeholders while a grid loads. */
export function GridSkeleton({ n = 8, className }: { n?: number; className?: string }) {
  return (
    <div className={className} aria-busy="true" aria-label="Loading">
      {Array.from({ length: n }, (_, i) => (
        <div key={i} className="h-40 animate-pulse rounded-2xl border border-line bg-surface-2/50" />
      ))}
    </div>
  )
}

export function ManagedNote({ what }: { what: string }) {
  return (
    <p className="flex items-start gap-2 rounded-xl border border-accent/30 bg-accent-soft px-3 py-2 text-sm" data-testid="managed-note">
      <FileLock2 size={16} className="mt-0.5 shrink-0 text-accent dark:text-accent-bright" aria-hidden />
      <span>
        {what} is managed by the config file: <code>kera apply</code> replaces edits made here the next time the configuration is applied.
      </span>
    </p>
  )
}

export function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** Grant principal for non-admins, who cannot look users or groups up. */
function PlainPrincipal({ p }: { p: string }) {
  const [Icon, text] = p === 'user:*' ? [Globe, 'Everyone'] : p.startsWith('group:') ? [GroupIcon, `Group ${p.slice(6)}`] : [User, p]
  return (
    <span className="flex min-w-0 flex-1 items-center gap-2">
      <Icon size={16} className="shrink-0 text-muted" />
      <span className="truncate">{text}</span>
    </span>
  )
}

/**
 * Edits the grants of one resource through GET/PUT `url` (an array of
 * {principal, permission}). Principals are user:<id>, group:<id> or user:*.
 * Admins resolve names and pick users and groups; others type a principal.
 */
export function AccessEditor({ url, canEdit }: { url: string; canEdit: boolean }) {
  const qc = useQueryClient()
  const { data: me } = useMe()
  const isAdmin = me?.role === 'admin'
  const { data: grants, isLoading } = useQuery({ queryKey: ['ws', 'grants', url], queryFn: () => api<Grant[]>(url) })
  const { data: groups = [] } = useGroups(isAdmin)
  const [rows, setRows] = useState<Grant[]>([])
  const [principal, setPrincipal] = useState('')
  const [saving, setSaving] = useState(false)
  useEffect(() => setRows(grants ?? []), [grants])
  const has = (p: string) => rows.some((r) => r.principal === p)
  const add = (p: string) => {
    p = p.trim()
    if (!p) return
    if (p === '*') p = 'user:*'
    if (!/^(user|group):.+/.test(p)) {
      toast('Use user:<id>, group:<id> or *', 'error')
      return
    }
    if (!has(p)) setRows([...rows, { principal: p, permission: 'read' }])
    setPrincipal('')
  }
  const save = async () => {
    setSaving(true)
    try {
      await api(url, { method: 'PUT', json: rows.map(({ principal, permission }) => ({ principal, permission })) })
      await qc.invalidateQueries({ queryKey: ['ws', 'grants', url] })
      toast('Access saved')
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setSaving(false)
    }
  }
  if (isLoading) return <p className="text-sm text-muted">Loading access...</p>
  const dirty = JSON.stringify(rows.map((r) => [r.principal, r.permission])) !== JSON.stringify((grants ?? []).map((r) => [r.principal, r.permission]))
  const freeGroups = groups.filter((g) => !has(`group:${g.id}`))
  return (
    <div className="space-y-3" data-testid="access-editor">
      <p className="text-xs text-muted">The owner and admins always have full access.</p>
      <ul className="divide-y divide-line rounded-xl border border-line">
        {rows.length === 0 && <li className="px-3 py-3 text-sm text-muted">Private: nobody else has access.</li>}
        {rows.map((g, i) => (
          <li key={g.principal} className="flex items-center gap-2 px-3 py-2 text-sm" data-testid="grant-row">
            {isAdmin ? <Principal p={g.principal} groupName={(gid) => groups.find((x) => x.id === gid)?.name} /> : <PlainPrincipal p={g.principal} />}
            {g.managed && <ManagedBadge compact />}
            <Select
              aria-label={`Permission for ${g.principal}`}
              value={g.permission}
              disabled={!canEdit}
              onChange={(v) => setRows(rows.map((r, j) => (j === i ? { ...r, permission: v as Grant['permission'] } : r)))}
              size="sm"
              className="w-auto shrink-0"
              options={[
                { value: 'read', label: 'Can use' },
                { value: 'write', label: 'Can edit' },
              ]}
            />
            {canEdit && (
              <IconButton label={`Remove ${g.principal}`} onClick={() => setRows(rows.filter((_, j) => j !== i))}>
                <Trash2 size={15} />
              </IconButton>
            )}
          </li>
        ))}
      </ul>
      {canEdit && (
        <div className="grid gap-2 sm:grid-cols-[auto_1fr_1fr]">
          <button type="button" className={btn} onClick={() => add('user:*')} disabled={has('user:*')}>
            <Globe size={14} /> Everyone
          </button>
          {isAdmin ? (
            <>
              <Select aria-label="Add a group" value="" placeholder="Add a group..." onChange={(v) => v && add(`group:${v}`)} options={freeGroups.map((g) => ({ value: g.id, label: g.name }))} disabled={!freeGroups.length} />
              <UserPicker placeholder="Add a user..." exclude={rows.filter((r) => r.principal.startsWith('user:')).map((r) => r.principal.slice(5))} onPick={(u) => add(`user:${u.id}`)} />
            </>
          ) : (
            <div className="flex gap-2 sm:col-span-2">
              <input className={cx(input, 'py-1.5')} placeholder="user:<id> or group:<id>" aria-label="Add a principal" value={principal} onChange={(e) => setPrincipal(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), add(principal))} />
              <button type="button" className={btn} onClick={() => add(principal)} disabled={!principal.trim()}>
                <Plus size={14} /> Add
              </button>
            </div>
          )}
        </div>
      )}
      {canEdit && (
        <div className="flex justify-end gap-2">
          {dirty && (
            <button type="button" className={btn} onClick={() => setRows(grants ?? [])}>
              Discard
            </button>
          )}
          <button type="button" className={btnPrimary} onClick={save} disabled={!dirty || saving} data-testid="save-access">
            Save access
          </button>
        </div>
      )}
    </div>
  )
}
