import { useEffect, useState, type ReactNode } from 'react'
import { useQueryClient, useQuery } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useMe } from '@/api/queries'
import { cx } from '@/lib/format'
import { IconButton } from '@/ui/Dialog'
import { toast } from '@/stores/toast'
import { useGroups, type Grant } from './api'
import { Select } from '@/ui/Select'

export const input = 'w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm outline-none focus:border-accent disabled:opacity-60'
export const btn = 'inline-flex items-center gap-1.5 rounded-lg border border-line px-3 py-1.5 text-sm hover:bg-surface-2 disabled:opacity-50'
export const btnPrimary = 'inline-flex items-center gap-1.5 rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50'

export function Field({ label, hint, children, className }: { label: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cx('block space-y-1', className)}>
      <span className="text-sm font-medium">{label}</span>
      {children}
      {hint && <span className="block text-xs text-muted">{hint}</span>}
    </label>
  )
}

export function Section({ title, children, aside }: { title: string; children: ReactNode; aside?: ReactNode }) {
  return (
    <section className="space-y-3 rounded-xl border border-line p-4">
      <div className="flex items-center gap-2">
        <h2 className="flex-1 text-sm font-semibold">{title}</h2>
        {aside}
      </div>
      {children}
    </section>
  )
}

const tones = {
  ok: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
  busy: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
  bad: 'bg-red-500/15 text-red-700 dark:text-red-300',
  muted: 'bg-surface-2 text-muted',
}

export function Badge({ tone = 'muted', children, title }: { tone?: keyof typeof tones; children: ReactNode; title?: string }) {
  return (
    <span title={title} className={cx('inline-flex items-center whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium', tones[tone])}>
      {children}
    </span>
  )
}

export function ManagedNote({ what }: { what: string }) {
  return (
    <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm" data-testid="managed-note">
      {what} is managed by <code>kera apply</code>: edits here are replaced the next time the configuration is applied.
    </p>
  )
}

export function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/**
 * Edits the grants of one resource through GET/PUT `url` (an array of
 * {principal, permission}). Principals are user:<id>, group:<id> or user:*.
 */
export function AccessEditor({ url, canEdit }: { url: string; canEdit: boolean }) {
  const qc = useQueryClient()
  const { data: me } = useMe()
  const { data: grants, isLoading } = useQuery({ queryKey: ['ws', 'grants', url], queryFn: () => api<Grant[]>(url) })
  const { data: groups = [] } = useGroups(me?.role === 'admin')
  const [rows, setRows] = useState<Grant[]>([])
  const [principal, setPrincipal] = useState('')
  const [saving, setSaving] = useState(false)
  useEffect(() => setRows(grants ?? []), [grants])
  const label = (p: string) => {
    if (p === 'user:*') return 'Everyone'
    if (p.startsWith('group:')) return `Group ${groups.find((g) => `group:${g.id}` === p)?.name ?? p.slice(6)}`
    return `User ${p.slice(5)}`
  }
  const add = (p: string) => {
    p = p.trim()
    if (!p) return
    if (p === '*') p = 'user:*'
    if (!/^(user|group):.+/.test(p)) {
      toast('Use user:<id>, group:<id> or *', 'error')
      return
    }
    if (!rows.some((r) => r.principal === p)) setRows([...rows, { principal: p, permission: 'read' }])
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
  return (
    <div className="space-y-3" data-testid="access-editor">
      <p className="text-xs text-muted">The owner and admins always have full access.</p>
      {rows.length === 0 && <p className="text-sm text-muted">Private: nobody else has access.</p>}
      <ul className="divide-y divide-line rounded-lg border border-line">
        {rows.map((g, i) => (
          <li key={g.principal} className="flex items-center gap-2 px-3 py-2 text-sm">
            <span className="min-w-0 flex-1 truncate">{label(g.principal)}</span>
            {g.managed && <Badge>kera apply</Badge>}
            <Select
              aria-label={`Permission for ${label(g.principal)}`}
              value={g.permission}
              disabled={!canEdit}
              onChange={(v) => setRows(rows.map((r, j) => (j === i ? { ...r, permission: v as Grant['permission'] } : r)))}
              size="sm"
              className="w-auto"
              options={[
                { value: 'read', label: 'Can use' },
                { value: 'write', label: 'Can edit' },
              ]}
            />
            {canEdit && (
              <IconButton label="Remove" onClick={() => setRows(rows.filter((_, j) => j !== i))}>
                <Trash2 size={15} />
              </IconButton>
            )}
          </li>
        ))}
      </ul>
      {canEdit && (
        <div className="flex flex-wrap items-center gap-2">
          <button type="button" className={btn} onClick={() => add('user:*')}>
            <Plus size={14} /> Everyone
          </button>
          {groups.length > 0 && (
            <Select
              aria-label="Add a group"
              className="w-auto"
              value=""
              placeholder="Add a group..."
              onChange={add}
              options={groups.map((g) => ({ value: `group:${g.id}`, label: g.name }))}
            />
          )}
          <input className={cx(input, 'w-56 flex-none py-1.5')} placeholder="user:<id> or group:<id>" value={principal} onChange={(e) => setPrincipal(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), add(principal))} />
          <button type="button" className={btn} onClick={() => add(principal)} disabled={!principal.trim()}>
            Add
          </button>
          <span className="flex-1" />
          <button type="button" className={btnPrimary} onClick={save} disabled={!dirty || saving} data-testid="save-access">
            Save access
          </button>
        </div>
      )}
    </div>
  )
}
