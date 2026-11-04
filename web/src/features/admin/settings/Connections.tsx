import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Activity, CheckCircle2, Cloud, Cpu, Loader2, Pencil, Plus, Trash2, XCircle } from 'lucide-react'
import { api } from '@/api/client'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Switch } from '@/ui/misc'
import { enc, useConnections, type Connection } from '../api'
import { parseList } from '../lib'
import { attempt, Badge, Btn, Card, Confirm, Field, inputCls, Loading, ManagedBadge, managedWarning, SecretInput } from '../ui'
import { Select } from '@/ui/Select'
import { displayName } from '../labels'
import { IconTile } from './serviceIcons'

const kinds: [Connection['kind'], string][] = [
  ['ollama', 'Ollama'],
  ['openai', 'OpenAI-compatible (chat completions)'],
  ['responses', 'OpenAI Responses API'],
]

const kindLabel: Record<Connection['kind'], string> = { ollama: 'Ollama', openai: 'OpenAI-compatible', responses: 'Responses API' }

type TestResult = { state: 'running' } | { state: 'ok'; models: number; ms: number } | { state: 'error'; message: string }

const blank: Connection = { id: '', kind: 'openai', base_url: '', prefix: '', model_ids: [], enabled: true, headers: {}, options: {}, sort: 0, managed: false, has_key: false }

export function ConnectionsTab() {
  const conns = useConnections()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Connection | null>(null)
  const [deleting, setDeleting] = useState<Connection | null>(null)
  const [tests, setTests] = useState<Record<string, TestResult>>({})
  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: ['admin', 'connections'] }), qc.invalidateQueries({ queryKey: ['models'] })])
  const save = (c: Connection & { api_key?: string }) =>
    attempt(async () => {
      await api('/api/v1/configs/connections', { method: 'POST', json: { connections: [c] } })
      await refresh()
    }, 'Connection saved')
  // Test = list the server's models: proves the URL, the key and the network path in one call.
  const test = async (c: Connection) => {
    setTests((t) => ({ ...t, [c.id]: { state: 'running' } }))
    const started = performance.now()
    try {
      const r = await api<{ models: string[] }>(`/api/v1/configs/connections/${enc(c.id)}/verify`, { method: 'POST' })
      setTests((t) => ({ ...t, [c.id]: { state: 'ok', models: r.models.length, ms: Math.round(performance.now() - started) } }))
    } catch (e) {
      setTests((t) => ({ ...t, [c.id]: { state: 'error', message: (e as Error).message } }))
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Btn variant="primary" onClick={() => setEditing(blank)}>
          <Plus size={15} /> Add connection
        </Btn>
      </div>
      <Loading q={conns} />
      {conns.data?.connections.map((c) => (
        <Card key={c.id} className="p-4">
          <div className="flex flex-wrap items-center gap-3" data-testid="connection-row">
            <IconTile title={kindLabel[c.kind]}>
              {c.kind === 'ollama' ? <Cpu size={18} className="text-accent dark:text-accent-bright" aria-hidden /> : <Cloud size={18} className="text-accent dark:text-accent-bright" aria-hidden />}
            </IconTile>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-semibold">{displayName(c.id)}</span>
                <Badge>{kindLabel[c.kind] ?? c.kind}</Badge>
                {c.managed && <ManagedBadge compact />}
                {c.implicit && <Badge title="From OLLAMA_BASE_URL">From environment</Badge>}
                {c.has_key && <Badge tone="green">Key stored</Badge>}
                <TestBadge r={tests[c.id]} />
              </div>
              <p className="truncate text-sm text-muted">
                <code className="text-xs">{c.id}</code> · {c.base_url}
                {c.prefix && ` · Prefix “${c.prefix}”`}
                {c.model_ids.length > 0 && ` · ${c.model_ids.length} ${c.model_ids.length === 1 ? 'model' : 'models'} listed`}
              </p>
            </div>
            <Switch checked={c.enabled} onChange={(on) => void save({ ...c, enabled: on })} label={`Enable ${c.id}`} />
            <Btn onClick={() => void test(c)} disabled={tests[c.id]?.state === 'running'} data-testid="test-connection">
              {tests[c.id]?.state === 'running' ? <Loader2 size={14} className="animate-spin" /> : <Activity size={14} />} Test
            </Btn>
            {!c.implicit && (
              <>
                <IconButton label={`Edit ${c.id}`} onClick={() => setEditing(c)}>
                  <Pencil size={15} />
                </IconButton>
                <IconButton label={`Delete ${c.id}`} onClick={() => setDeleting(c)} className="hover:text-red-600">
                  <Trash2 size={15} />
                </IconButton>
              </>
            )}
          </div>
        </Card>
      ))}
      {editing && <EditConnection conn={editing} onClose={() => setEditing(null)} onSave={save} />}
      <Confirm
        open={!!deleting}
        title="Delete connection"
        body={<p>Delete {deleting?.id}? Its models disappear from the picker; presets built on them stop working.</p>}
        onClose={() => setDeleting(null)}
        onConfirm={async () => {
          await api(`/api/v1/configs/connections/${enc(deleting!.id)}`, { method: 'DELETE' })
          await refresh()
        }}
      />
    </div>
  )
}

function EditConnection({ conn, onClose, onSave }: { conn: Connection; onClose: () => void; onSave: (c: Connection & { api_key?: string }) => Promise<boolean> }) {
  const [c, setC] = useState(conn)
  const [key, setKey] = useState<string | null>(null)
  const [models, setModels] = useState(conn.model_ids.join('\n'))
  const fwd = c.options.forward_user_headers ?? {}
  const setFwd = (k: string, v: string) => setC({ ...c, options: { ...c.options, forward_user_headers: { ...fwd, [k]: v } } })
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const body: Connection & { api_key?: string } = { ...c, model_ids: parseList(models) }
    if (key !== null) body.api_key = key
    if (await onSave(body)) onClose()
  }
  const isNew = !conn.id
  return (
    <Dialog open onClose={onClose} label="Edit connection" className="w-[min(40rem,calc(100vw-2rem))]">
      <DialogHeader title={isNew ? 'Add connection' : `Edit ${displayName(conn.id)}`} onClose={onClose} />
      <form onSubmit={submit} className="space-y-3 p-4">
        {conn.managed && <p className="text-xs text-amber-700 dark:text-amber-400">{managedWarning}</p>}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="ID" hint={isNew ? 'Leave empty to generate one.' : undefined}>
            <input value={c.id} disabled={!isNew} onChange={(e) => setC({ ...c, id: e.target.value })} className={inputCls} />
          </Field>
          <Field label="Kind">
            <Select value={c.kind} onChange={(v) => setC({ ...c, kind: v as Connection['kind'] })} options={kinds.map(([value, label]) => ({ value, label }))} aria-label="Kind" />
          </Field>
        </div>
        <Field label="Base URL">
          <input value={c.base_url} required type="url" onChange={(e) => setC({ ...c, base_url: e.target.value })} placeholder="http://127.0.0.1:11434" className={inputCls} />
        </Field>
        <Field label="API key" hint="Write-only: the stored key is never shown.">
          <SecretInput has={conn.has_key} value={key} onChange={setKey} label="API key" />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Prefix" hint="Prepended to model ids, e.g. web.">
            <input value={c.prefix} onChange={(e) => setC({ ...c, prefix: e.target.value })} className={inputCls} />
          </Field>
          <Field label="Sort">
            <input type="number" value={c.sort} onChange={(e) => setC({ ...c, sort: Number(e.target.value) || 0 })} className={inputCls} />
          </Field>
        </div>
        <Field label="Model IDs" hint="One per line. Empty lists whatever the server reports.">
          <textarea value={models} rows={3} onChange={(e) => setModels(e.target.value)} className={inputCls} />
        </Field>
        <fieldset className="space-y-2">
          <legend className="text-xs font-medium text-muted">Forward the user as headers (e.g. for spend tracking)</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {['email', 'id', 'name', 'role'].map((k) => (
              <input key={k} value={fwd[k] ?? ''} onChange={(e) => setFwd(k, e.target.value)} placeholder={`Header for ${k}`} aria-label={`Header for user ${k}`} className={inputCls} />
            ))}
          </div>
        </fieldset>
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary">
            Save
          </Btn>
        </div>
      </form>
    </Dialog>
  )
}

function TestBadge({ r }: { r?: TestResult }) {
  if (!r || r.state === 'running') return null
  if (r.state === 'ok')
    return (
      <Badge tone="green" title="The server answered and listed its models">
        <CheckCircle2 size={11} /> Connected · {r.models} {r.models === 1 ? 'model' : 'models'} · {r.ms} ms
      </Badge>
    )
  return (
    <Badge tone="red" title={r.message}>
      <XCircle size={11} /> Failed: {r.message.length > 60 ? r.message.slice(0, 60) + '…' : r.message}
    </Badge>
  )
}
