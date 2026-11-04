import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Activity, CheckCircle2, Cloud, Cpu, Loader2, Pencil, Plus, Trash2, XCircle } from 'lucide-react'
import { api } from '@/api/client'
import { toast } from '@/stores/toast'
import { IconButton } from '@/ui/Dialog'
import { Switch } from '@/ui/misc'
import { enc, useConnections, type Connection } from '../api'
import { parseList } from '../lib'
import { attempt, Badge, Btn, Card, Confirm, inputCls, Loading, ManagedBadge, SecretInput } from '../ui'
import { Select } from '@/ui/Select'
import { displayName } from '../labels'
import { IconTile } from './serviceIcons'
import { bad, EditorDialog, useDraftTest, useFormErrors, VField, type ProbeResult } from './dialogKit'
import { validateConnection } from './validate'

const kinds: [Connection['kind'], string][] = [
  ['ollama', 'Ollama'],
  ['openai', 'OpenAI-compatible (chat completions)'],
  ['responses', 'OpenAI Responses API'],
]

const kindLabel: Record<Connection['kind'], string> = { ollama: 'Ollama', openai: 'OpenAI-compatible', responses: 'Responses API' }

type TestResult = { state: 'running' } | { state: 'ok'; models: number; ms: number } | { state: 'error'; message: string }

const TEST = '/api/v1/configs/connections/test'

const blank: Connection = { id: '', kind: 'openai', base_url: '', prefix: '', model_ids: [], enabled: true, headers: {}, options: {}, sort: 0, managed: false, has_key: false }

export function ConnectionsTab() {
  const conns = useConnections()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Connection | null>(null)
  const [deleting, setDeleting] = useState<Connection | null>(null)
  const [tests, setTests] = useState<Record<string, TestResult>>({})
  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: ['admin', 'connections'] }), qc.invalidateQueries({ queryKey: ['models'] })])
  /** Throws on failure (the dialog shows field errors inline). */
  const save = async (c: Connection & { api_key?: string }) => {
    await api('/api/v1/configs/connections', { method: 'POST', json: { connections: [c] } })
    await refresh()
  }
  // Test = list the server's models with the stored key: proves the URL, the key and the network path in one call.
  const test = async (c: Connection) => {
    setTests((t) => ({ ...t, [c.id]: { state: 'running' } }))
    try {
      const r = await api<ProbeResult>(TEST, { method: 'POST', json: c })
      setTests((t) => ({ ...t, [c.id]: r.ok ? { state: 'ok', models: r.models?.length ?? 0, ms: r.ms } : { state: 'error', message: r.error ?? 'Failed' } }))
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
            <Switch checked={c.enabled} onChange={(on) => void attempt(() => save({ ...c, enabled: on }), 'Connection saved')} label={`Enable ${c.id}`} />
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
      {editing && <EditConnection conn={editing} taken={conns.data?.connections.map((x) => x.id) ?? []} onClose={() => setEditing(null)} onSave={save} />}
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

const connFields = ['id', 'base_url', 'prefix', 'sort', 'kind', 'api_key', 'options']

function EditConnection({ conn, taken, onClose, onSave }: { conn: Connection; taken: string[]; onClose: () => void; onSave: (c: Connection & { api_key?: string }) => Promise<void> }) {
  const [c, setC] = useState(conn)
  const [key, setKey] = useState<string | null>(null)
  const [models, setModels] = useState(conn.model_ids.join('\n'))
  const isNew = !conn.id
  const form = useFormErrors(validateConnection(c, { isNew, taken }), !isNew)
  const test = useDraftTest(TEST, form)
  const fwd = c.options.forward_user_headers ?? {}
  const setFwd = (k: string, v: string) => setC({ ...c, options: { ...c.options, forward_user_headers: { ...fwd, [k]: v } } })
  const body = () => {
    const b: Connection & { api_key?: string } = { ...c, model_ids: parseList(models) }
    if (key !== null) b.api_key = key
    return b
  }
  const submit = async () => {
    await onSave(body())
    toast('Connection saved')
    onClose()
  }
  return (
    <EditorDialog title={isNew ? 'Add connection' : `Edit ${displayName(conn.id)}`} managed={conn.managed} form={form} fields={connFields} onClose={onClose} onSubmit={submit} test={test} onTest={() => void test.run(body())}>
      <div className="grid gap-3 sm:grid-cols-2">
        <VField label="ID" hint={isNew ? 'Leave empty to generate one.' : undefined} error={form.err('id')}>
          <input name="id" value={c.id} disabled={!isNew} onChange={(e) => setC({ ...c, id: e.target.value })} className={inputCls + bad(form.err('id'))} />
        </VField>
        <VField label="Kind">
          <Select
            value={c.kind}
            onChange={(v) => {
              setC({ ...c, kind: v as Connection['kind'] })
              form.touch('base_url')
              test.clear()
            }}
            options={kinds.map(([value, label]) => ({ value, label }))}
            aria-label="Kind"
          />
        </VField>
      </div>
      <VField label="Base URL" hint={c.kind === 'ollama' ? 'Ollama: no /v1, e.g. http://127.0.0.1:11434' : 'May end in /v1, e.g. http://litellm:4000/v1'} error={form.err('base_url')}>
        <input name="base_url" value={c.base_url} onChange={(e) => setC({ ...c, base_url: e.target.value })} placeholder="http://127.0.0.1:11434" className={inputCls + bad(form.err('base_url'))} data-testid="connection-url" />
      </VField>
      <VField label="API key" hint="Write-only: the stored key is never shown." error={form.err('api_key')}>
        <SecretInput has={conn.has_key} value={key} onChange={setKey} label="API key" error={form.err('api_key')} />
      </VField>
      <div className="grid gap-3 sm:grid-cols-2">
        <VField label="Prefix" hint="Prepended to model ids, e.g. web." error={form.err('prefix')}>
          <input name="prefix" value={c.prefix} onChange={(e) => setC({ ...c, prefix: e.target.value })} className={inputCls + bad(form.err('prefix'))} />
        </VField>
        <VField label="Sort" error={form.err('sort')}>
          <input name="sort" type="number" step={1} value={Number.isNaN(c.sort) ? '' : c.sort} onChange={(e) => setC({ ...c, sort: e.target.valueAsNumber })} className={inputCls + bad(form.err('sort'))} />
        </VField>
      </div>
      <VField label="Model IDs" hint="One per line. Empty lists whatever the server reports.">
        <textarea value={models} rows={3} onChange={(e) => setModels(e.target.value)} className={inputCls} />
      </VField>
      <fieldset className="space-y-2">
        <legend className="text-xs font-medium text-muted">Forward the user as headers (e.g. for spend tracking)</legend>
        <div className="grid gap-2 sm:grid-cols-2">
          {['email', 'id', 'name', 'role'].map((k) => (
            <input key={k} value={fwd[k] ?? ''} onChange={(e) => setFwd(k, e.target.value)} placeholder={`Header for ${k}`} aria-label={`Header for user ${k}`} className={inputCls} />
          ))}
        </div>
        {form.err('options') && <p className="text-xs text-red-600 dark:text-red-400">{form.err('options')}</p>}
      </fieldset>
    </EditorDialog>
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
