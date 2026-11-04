import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router'
import { Pencil, Search, Shield } from 'lucide-react'
import { api } from '@/api/client'
import type { Model } from '@/api/types'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Switch } from '@/ui/misc'
import { cx } from '@/lib/format'
import { AccessEditor } from '../Access'
import { enc, useAdmin, type ModelRow } from '../api'
import { attempt, Badge, Card, inputCls, Loading, ManagedBadge, tdCls, thCls } from '../ui'

interface Item {
  id: string
  name: string
  kind: string
  base: string
  local: boolean
  hidden: boolean
  active: boolean
  managed: boolean
  preset: boolean
}

export function ModelsTab() {
  // active models of every kind (embedding and rerank too), hidden included (admins see all)
  const listed = useAdmin<{ data: Model[] }>(['models-all'], '/api/models?include_hidden=true&kinds=all')
  const rows = useAdmin<ModelRow[]>(['model-rows'], '/api/v1/models/')
  const qc = useQueryClient()
  const [q, setQ] = useState('')
  const [access, setAccess] = useState<Item | null>(null)
  const items = useMemo(() => {
    const byId = new Map<string, Item>()
    for (const m of listed.data?.data ?? [])
      byId.set(m.id, { id: m.id, name: m.name || m.id, kind: m.kind ?? 'chat', base: '', local: !!m.local, hidden: !!m.hidden, active: true, managed: false, preset: false })
    for (const r of rows.data ?? []) {
      const cur = byId.get(r.id)
      byId.set(r.id, {
        id: r.id,
        name: r.name || cur?.name || r.id,
        kind: cur?.kind ?? 'chat',
        base: r.base_model_id ?? r.connection_id ?? '',
        local: cur?.local ?? false,
        hidden: !!r.meta?.hidden,
        active: r.is_active,
        managed: r.managed,
        preset: !!r.base_model_id,
      })
    }
    const needle = q.trim().toLowerCase()
    return [...byId.values()].filter((m) => !needle || `${m.id} ${m.name}`.toLowerCase().includes(needle)).sort((a, b) => Number(b.preset) - Number(a.preset) || a.name.localeCompare(b.name))
  }, [listed.data, rows.data, q])
  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: ['models'] }), qc.invalidateQueries({ queryKey: ['admin', 'model-rows'] }), qc.invalidateQueries({ queryKey: ['admin', 'models-all'] })])
  const setHidden = (m: Item, hidden: boolean) =>
    attempt(async () => {
      await api(`/api/v1/models/model/update?id=${enc(m.id)}`, { method: 'POST', json: { meta: { hidden } } })
      await refresh()
    })
  const setActive = (m: Item, active: boolean) =>
    attempt(async () => {
      await api(`/api/v1/models/model/update?id=${enc(m.id)}`, { method: 'POST', json: { is_active: active } })
      await refresh()
    })

  return (
    <Card
      title="Models"
      hint="Presets and discovered models. Hidden models stay usable by API clients and links; inactive ones are off for everyone."
      actions={
        <label className="relative w-full sm:w-64">
          <Search size={15} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
          <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter models" aria-label="Filter models" className={cx(inputCls, 'pl-8')} />
        </label>
      }
    >
      <Loading q={listed.isLoading ? listed : rows} />
      <div className="overflow-x-auto">
        <table className="w-full text-sm" data-testid="models-table">
          <thead className="border-b border-line">
            <tr>
              <th className={thCls}>Model</th>
              <th className={thCls}>Built on</th>
              <th className={thCls}>Active</th>
              <th className={thCls}>Hidden</th>
              <th className={thCls}>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {items.map((m) => (
              <tr key={m.id} className={cx(!m.active && 'opacity-60')}>
                <td className={tdCls}>
                  <div className="flex min-w-48 flex-wrap items-center gap-1.5">
                    <span className="font-medium">{m.name}</span>
                    {m.preset && <Badge tone="accent">preset</Badge>}
                    {m.local && <Badge>local</Badge>}
                    {m.kind !== 'chat' && <Badge title="Not offered for chat">{m.kind}</Badge>}
                    {m.managed && <ManagedBadge />}
                  </div>
                  <div className="font-mono text-[11px] text-muted">{m.id}</div>
                </td>
                <td className={cx(tdCls, 'font-mono text-xs text-muted')}>{m.base || '–'}</td>
                <td className={tdCls}>
                  <Switch checked={m.active} onChange={(on) => void setActive(m, on)} label={`${m.id} active`} />
                </td>
                <td className={tdCls}>
                  <Switch checked={m.hidden} onChange={(on) => void setHidden(m, on)} label={`${m.id} hidden`} />
                </td>
                <td className={cx(tdCls, 'whitespace-nowrap text-right')}>
                  <IconButton label={`Who can use ${m.id}`} onClick={() => setAccess(m)}>
                    <Shield size={15} />
                  </IconButton>
                  <Link to={`/workspace/models/${enc(m.id)}`} aria-label={`Edit ${m.id} in the workspace`} title="Edit in the workspace" className="inline-flex size-8 items-center justify-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg">
                    <Pencil size={15} />
                  </Link>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {access && (
        <Dialog open onClose={() => setAccess(null)} label="Model access" className="w-[min(36rem,calc(100vw-2rem))]">
          <DialogHeader title={`Who can use ${access.name}`} onClose={() => setAccess(null)} />
          <div className="p-4">
            <AccessEditor type="model" id={access.id} hint={access.managed ? 'Saving replaces the grants from the config file too; the next kera apply adds them back.' : 'Admins can always use every model.'} />
          </div>
        </Dialog>
      )}
    </Card>
  )
}
