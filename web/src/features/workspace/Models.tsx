import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Plus, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useMe, useModels, useTools } from '@/api/queries'
import type { Model } from '@/api/types'
import { cx } from '@/lib/format'
import { IconButton } from '@/ui/Dialog'
import { Spinner, Switch } from '@/ui/misc'
import { toast } from '@/stores/toast'
import { useCollections, useModelRow, useModelRows } from './api'
import { BUILTINS, CAPABILITIES, formToBody, rowToForm, validModelId, type ModelForm, type ModelRow, type Toggle } from './model-form'
import { AccessEditor, Badge, Field, ManagedNote, Section, btn, btnPrimary, errorText, input } from './ui'

const editLink = (id: string) => `/workspace/models/edit?id=${encodeURIComponent(id)}`

export function ModelsList({ canCreate }: { canCreate: boolean }) {
  const { data: me } = useMe()
  const { data: rows, isLoading } = useModelRows()
  const { data: models = [] } = useModels()
  const [q, setQ] = useState('')
  const isAdmin = me?.role === 'admin'
  const byId = useMemo(() => new Map(models.map((m) => [m.id, m])), [models])
  const rowIds = new Set((rows ?? []).map((r) => r.id))
  const match = (id: string, name: string) => !q || `${id} ${name}`.toLowerCase().includes(q.toLowerCase())
  const presets = (rows ?? []).filter((r) => r.base_model_id && match(r.id, r.name))
  const overrides = (rows ?? []).filter((r) => !r.base_model_id && match(r.id, r.name))
  // Discovered models without a row: admins may override them.
  const discovered = isAdmin ? models.filter((m) => !rowIds.has(m.id) && match(m.id, m.name)) : []
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-2">
        <input className={cx(input, 'max-w-xs')} placeholder="Search models" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search models" />
        <span className="flex-1" />
        {canCreate && (
          <Link to="/workspace/models/new" className={btnPrimary} data-testid="new-model">
            <Plus size={15} /> New model
          </Link>
        )}
      </div>
      {isLoading && <Spinner />}
      <ModelTable title="Assistants (presets)" rows={presets.map((r) => ({ id: r.id, name: r.name, row: r, model: byId.get(r.id) }))} empty="No presets you can edit." />
      <ModelTable title="Base model settings" rows={overrides.map((r) => ({ id: r.id, name: r.name, row: r, model: byId.get(r.id) }))} empty="No base model has custom settings." />
      {isAdmin && <ModelTable title="Discovered models" rows={discovered.map((m) => ({ id: m.id, name: m.name, model: m }))} empty="Every discovered model has settings." />}
    </div>
  )
}

function ModelTable({ title, rows, empty }: { title: string; rows: { id: string; name: string; row?: ModelRow; model?: Model }[]; empty: string }) {
  return (
    <section>
      <h2 className="mb-2 text-sm font-semibold text-muted">{title}</h2>
      {rows.length === 0 ? (
        <p className="text-sm text-muted">{empty}</p>
      ) : (
        <ul className="divide-y divide-line rounded-xl border border-line">
          {rows.map(({ id, name, row, model }) => (
            <li key={id}>
              <Link to={editLink(id)} className="flex items-center gap-3 px-4 py-3 hover:bg-surface-2" data-testid="model-row">
                {row?.meta?.profile_image_url ? <img src={row.meta.profile_image_url} alt="" className="size-8 rounded-full object-cover" /> : <span className="grid size-8 place-items-center rounded-full bg-accent-soft text-xs font-semibold">{(name || id).slice(0, 1).toUpperCase()}</span>}
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{name || id}</span>
                  <span className="block truncate text-xs text-muted">
                    {id}
                    {row?.base_model_id ? ` · on ${row.base_model_id}` : ''}
                    {row?.meta?.description ? ` · ${row.meta.description}` : ''}
                  </span>
                </span>
                {model?.local && <Badge>local</Badge>}
                {row?.meta?.hidden && <Badge>hidden</Badge>}
                {row && row.is_active === false && <Badge tone="bad">inactive</Badge>}
                {row && !model && <Badge tone="busy" title="The base model is not available right now">unavailable</Badge>}
                {row?.managed && <Badge>kera apply</Badge>}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

const blank: ModelForm = rowToForm({ id: '', name: '' })

export function ModelEditor({ create = false }: { create?: boolean }) {
  const [params] = useSearchParams()
  const id = create ? undefined : (params.get('id') ?? undefined)
  const { data: row, error, isLoading } = useModelRow(id)
  const { data: me } = useMe()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [f, setF] = useState<ModelForm>(blank)
  const [saving, setSaving] = useState(false)
  useEffect(() => {
    if (row) setF(rowToForm(row))
  }, [row])
  if (!create && isLoading) return <Spinner />
  if (!create && error) return <p className="text-red-600">{errorText(error)}</p>
  const set = <K extends keyof ModelForm>(k: K, v: ModelForm[K]) => setF((x) => ({ ...x, [k]: v }))
  const isPreset = create || !!row?.base_model_id
  const save = async () => {
    let body
    try {
      body = formToBody(f)
    } catch (e) {
      toast(errorText(e), 'error')
      return
    }
    if (create && !validModelId(f.id)) return toast('Model id: letters, digits and . _ : - only', 'error')
    if (isPreset && !f.baseModelId) return toast('Choose a base model', 'error')
    setSaving(true)
    try {
      const saved = create ? await api<ModelRow>('/api/v1/models/create', { method: 'POST', json: body }) : await api<ModelRow>(`/api/v1/models/model/update?id=${encodeURIComponent(f.id)}`, { method: 'POST', json: body })
      await Promise.all([qc.invalidateQueries({ queryKey: ['ws'] }), qc.invalidateQueries({ queryKey: ['models'] })])
      toast('Model saved')
      if (create) navigate(editLink(saved.id), { replace: true })
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setSaving(false)
    }
  }
  const remove = async () => {
    if (!confirm(`Delete ${f.name || f.id}? Chats keep their messages.`)) return
    try {
      await api(`/api/v1/models/model/delete?id=${encodeURIComponent(f.id)}`, { method: 'DELETE' })
      await Promise.all([qc.invalidateQueries({ queryKey: ['ws'] }), qc.invalidateQueries({ queryKey: ['models'] })])
      navigate('/workspace/models')
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }
  const hasRow = (row?.created_at ?? 0) > 0 // a discovered model without settings comes back unsaved
  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault()
        void save()
      }}
      data-testid="model-editor"
    >
      <div className="flex items-center gap-2">
        <Link to="/workspace/models" className={btn}>
          <ArrowLeft size={15} /> Models
        </Link>
        <h2 className="min-w-0 flex-1 truncate text-lg font-semibold">{create ? 'New model' : f.name || f.id}</h2>
        {hasRow && (
          <IconButton label="Delete model" onClick={remove}>
            <Trash2 size={16} />
          </IconButton>
        )}
        <button type="submit" className={btnPrimary} disabled={saving} data-testid="save-model">
          {saving ? <Spinner /> : null} Save
        </button>
      </div>
      {row?.managed && <ManagedNote what="This model" />}
      {!create && !isPreset && <p className="text-sm text-muted">Settings for the base model {row?.connection_id ? `from connection ${row.connection_id}` : ''}. They apply to every chat with it and to presets built on it.</p>}
      <General f={f} set={set} create={create} isPreset={isPreset} />
      <Section title="System prompt">
        <textarea className={cx(input, 'min-h-48 font-mono text-[13px]')} value={f.system} onChange={(e) => set('system', e.target.value)} aria-label="System prompt" placeholder={isPreset ? 'Empty: inherit the base model prompt' : ''} />
        <p className="text-xs text-muted">Variables: {'{{CURRENT_DATE}} {{CURRENT_TIME}} {{CURRENT_WEEKDAY}} {{USER_NAME}} {{USER_EMAIL}} {{USER_LANGUAGE}}'}</p>
      </Section>
      <Section title="Parameters">
        <textarea className={cx(input, 'min-h-28 font-mono text-[13px]')} value={f.paramsText} onChange={(e) => set('paramsText', e.target.value)} aria-label="Parameters (JSON)" placeholder={'{"temperature": 0.3, "num_ctx": 65536, "think": false, "reasoning_effort": "low"}'} />
        <p className="text-xs text-muted">A JSON object. Request values win; these fill the gaps (num_ctx, think, temperature, reasoning_effort, compact_token_threshold...).</p>
      </Section>
      <Capabilities f={f} set={set} />
      <Tools f={f} set={set} />
      <KnowledgeRefs f={f} set={set} />
      <Suggestions f={f} set={set} />
      {me?.role === 'admin' && f.id && !create && (
        <Section title="Access">
          <AccessEditor url={`/api/v1/grants/model/${encodeURIComponent(f.id)}`} canEdit />
        </Section>
      )}
    </form>
  )
}

type SetFn = <K extends keyof ModelForm>(k: K, v: ModelForm[K]) => void

function General({ f, set, create, isPreset }: { f: ModelForm; set: SetFn; create: boolean; isPreset: boolean }) {
  const { data: models = [] } = useModels()
  const onImage = (file?: File) => {
    if (!file) return
    if (file.size > 256 * 1024) return toast('Pick an image under 256 KB', 'error')
    const r = new FileReader()
    r.onload = () => set('image', String(r.result))
    r.readAsDataURL(file)
  }
  return (
    <Section title="General">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Name">
          <input className={input} value={f.name} onChange={(e) => set('name', e.target.value)} data-testid="model-name" />
        </Field>
        <Field label="Model id" hint={create ? 'Used in URLs and the API; cannot change later.' : undefined}>
          <input className={input} value={f.id} disabled={!create} onChange={(e) => set('id', e.target.value)} data-testid="model-id" />
        </Field>
        {isPreset && (
          <Field label="Base model">
            <select className={input} value={f.baseModelId} onChange={(e) => set('baseModelId', e.target.value)} data-testid="base-model">
              <option value="">Choose...</option>
              {models
                .filter((m) => m.id !== f.id)
                .map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name || m.id}
                    {m.hidden ? ' (hidden)' : ''}
                  </option>
                ))}
              {f.baseModelId && !models.some((m) => m.id === f.baseModelId) && <option value={f.baseModelId}>{f.baseModelId} (unavailable)</option>}
            </select>
          </Field>
        )}
        <Field label="Description">
          <input className={input} value={f.description} onChange={(e) => set('description', e.target.value)} />
        </Field>
        <Field label="Image" hint="An image URL, or upload one (256 KB max).">
          <div className="flex items-center gap-2">
            {f.image && <img src={f.image} alt="" className="size-9 rounded-full object-cover" />}
            <input className={input} value={f.image.startsWith('data:') ? '(uploaded image)' : f.image} onChange={(e) => set('image', e.target.value)} />
            <label className={cx(btn, 'cursor-pointer')}>
              Upload
              <input type="file" accept="image/*" className="sr-only" onChange={(e) => onImage(e.target.files?.[0])} />
            </label>
          </div>
        </Field>
      </div>
      <div className="flex flex-wrap gap-6 pt-1">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={!f.hidden} onChange={(v) => set('hidden', !v)} label="Show in the model picker" /> Show in the model picker
        </label>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={f.active} onChange={(v) => set('active', v)} label="Active" /> Active
        </label>
      </div>
    </Section>
  )
}

function Capabilities({ f, set }: { f: ModelForm; set: SetFn }) {
  const toggleDefault = (id: string, on: boolean) => set('defaultFeatures', on ? [...new Set([...f.defaultFeatures, id])] : f.defaultFeatures.filter((d) => d !== id))
  return (
    <Section title="Capabilities and features">
      <div className="flex flex-wrap gap-x-5 gap-y-2">
        {CAPABILITIES.map((c) => (
          <label key={c} className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={f.capabilities[c] === true} onChange={(e) => set('capabilities', { ...f.capabilities, [c]: e.target.checked })} />
            {c.replace('_', ' ')}
          </label>
        ))}
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Web search" hint="Native: the provider's own search (Responses connections).">
          <select className={input} value={f.webSearch} onChange={(e) => set('webSearch', e.target.value as ModelForm['webSearch'])}>
            <option value="off">Off</option>
            <option value="builtin">Kera web tools</option>
            <option value="native">Native</option>
          </select>
        </Field>
        <Field label="Chat attachments" hint="Tools: the model reads files with tools. Full: text goes into the prompt.">
          <select className={input} value={f.fileContext} onChange={(e) => set('fileContext', e.target.value as ModelForm['fileContext'])}>
            <option value="tools">Read with tools</option>
            <option value="full">Whole text in the prompt</option>
          </select>
        </Field>
        <div className="space-y-2 pt-6">
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={f.codeInterpreter} onChange={(v) => set('codeInterpreter', v)} label="Code interpreter" /> Code interpreter
          </label>
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={f.memory} onChange={(v) => set('memory', v)} label="Memory" /> Memory
          </label>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm">
        <span className="text-muted">On by default in new chats:</span>
        {(['web_search', 'code_interpreter', 'memory'] as const).map((id) => (
          <label key={id} className="flex items-center gap-2">
            <input type="checkbox" checked={f.defaultFeatures.includes(id)} onChange={(e) => toggleDefault(id, e.target.checked)} />
            {id.replace('_', ' ')}
          </label>
        ))}
      </div>
    </Section>
  )
}

function Tools({ f, set }: { f: ModelForm; set: SetFn }) {
  const { data: servers = [], isError } = useTools()
  const known = servers.filter((s) => !s.id.startsWith('builtin:'))
  const ids = [...new Set([...known.map((s) => s.id.replace(/^server:/, '')), ...f.servers])]
  const [extra, setExtra] = useState('')
  return (
    <Section title="Tools">
      <div className="space-y-2">
        <p className="text-sm font-medium">Tool servers</p>
        {ids.length === 0 && <p className="text-sm text-muted">{isError ? 'Tool servers could not be listed.' : 'No tool servers configured.'}</p>}
        <div className="flex flex-wrap gap-x-5 gap-y-2">
          {ids.map((id) => (
            <label key={id} className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={f.servers.includes(id)} onChange={(e) => set('servers', e.target.checked ? [...f.servers, id] : f.servers.filter((s) => s !== id))} />
              {known.find((s) => s.id.replace(/^server:/, '') === id)?.name ?? id}
            </label>
          ))}
        </div>
        <div className="flex gap-2">
          <input className={cx(input, 'max-w-xs py-1.5')} placeholder="Tool server id" value={extra} onChange={(e) => setExtra(e.target.value)} aria-label="Add a tool server by id" />
          <button
            type="button"
            className={btn}
            disabled={!extra.trim()}
            onClick={() => {
              set('servers', [...new Set([...f.servers, extra.trim()])])
              setExtra('')
            }}
          >
            Add
          </button>
        </div>
      </div>
      <div className="space-y-2">
        <p className="text-sm font-medium">Built-in tools</p>
        <div className="grid gap-2 sm:grid-cols-4">
          {[...new Set([...BUILTINS, ...Object.keys(f.builtins)])].map((b) => (
            <label key={b} className="flex items-center justify-between gap-2 rounded-lg border border-line px-2 py-1 text-sm">
              {b.replace('_', ' ')}
              <select aria-label={`Built-in ${b}`} className="bg-transparent text-sm" value={f.builtins[b] ?? 'default'} onChange={(e) => set('builtins', { ...f.builtins, [b]: e.target.value as Toggle })}>
                <option value="default">default</option>
                <option value="on">on</option>
                <option value="off">off</option>
              </select>
            </label>
          ))}
        </div>
      </div>
    </Section>
  )
}

function KnowledgeRefs({ f, set }: { f: ModelForm; set: SetFn }) {
  const { data: colls = [] } = useCollections()
  const label = (k: { id?: string; name?: string }) => colls.find((c) => c.id === k.id || (!k.id && c.name === k.name))?.name ?? k.name ?? k.id ?? ''
  const attached = (id: string, name: string) => f.knowledge.some((k) => k.id === id || (!k.id && k.name === name))
  return (
    <Section title="Knowledge">
      {f.knowledge.length === 0 && <p className="text-sm text-muted">No knowledge attached.</p>}
      <ul className="space-y-2">
        {f.knowledge.map((k, i) => (
          <li key={`${k.id ?? ''}|${k.name ?? ''}`} className="flex items-center gap-2 text-sm">
            <span className="min-w-0 flex-1 truncate">
              {label(k)} {!k.id && <Badge title="Referenced by name">by name</Badge>}
              {!colls.some((c) => c.id === k.id || c.name === k.name) && <Badge tone="busy">not found</Badge>}
            </span>
            <select aria-label="Knowledge mode" className="rounded-md border border-line bg-surface px-2 py-1 text-sm" value={k.mode || 'retrieve'} onChange={(e) => set('knowledge', f.knowledge.map((x, j) => (j === i ? { ...x, mode: e.target.value } : x)))}>
              <option value="retrieve">Search relevant parts</option>
              <option value="full">Whole documents in the prompt</option>
            </select>
            <IconButton label="Detach" onClick={() => set('knowledge', f.knowledge.filter((_, j) => j !== i))}>
              <Trash2 size={15} />
            </IconButton>
          </li>
        ))}
      </ul>
      <select aria-label="Attach knowledge" className={cx(input, 'max-w-sm')} value="" onChange={(e) => e.target.value && set('knowledge', [...f.knowledge, { id: e.target.value, mode: 'retrieve' }])}>
        <option value="">Attach a collection...</option>
        {colls
          .filter((c) => !attached(c.id, c.name))
          .map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
      </select>
    </Section>
  )
}

function Suggestions({ f, set }: { f: ModelForm; set: SetFn }) {
  const upd = (i: number, v: Partial<ModelForm['suggestions'][number]>) => set('suggestions', f.suggestions.map((s, j) => (j === i ? { ...s, ...v } : s)))
  return (
    <Section
      title="Suggestions"
      aside={
        <button type="button" className={btn} onClick={() => set('suggestions', [...f.suggestions, { title: [], content: '' }])}>
          <Plus size={14} /> Add
        </button>
      }
    >
      {f.suggestions.length === 0 && <p className="text-sm text-muted">No starter suggestions.</p>}
      {f.suggestions.map((s, i) => (
        <div key={i} className="grid gap-2 rounded-lg border border-line p-3 sm:grid-cols-[1fr_1fr_auto]">
          <input className={input} placeholder="Title" value={s.title?.[0] ?? ''} onChange={(e) => upd(i, { title: [e.target.value, s.title?.[1] ?? ''] })} aria-label="Suggestion title" />
          <input className={input} placeholder="Subtitle" value={s.title?.[1] ?? ''} onChange={(e) => upd(i, { title: [s.title?.[0] ?? '', e.target.value] })} aria-label="Suggestion subtitle" />
          <IconButton label="Remove suggestion" onClick={() => set('suggestions', f.suggestions.filter((_, j) => j !== i))}>
            <Trash2 size={15} />
          </IconButton>
          <textarea className={cx(input, 'sm:col-span-3')} placeholder="Prompt sent when clicked" value={s.content} onChange={(e) => upd(i, { content: e.target.value })} aria-label="Suggestion prompt" />
        </div>
      ))}
    </Section>
  )
}
