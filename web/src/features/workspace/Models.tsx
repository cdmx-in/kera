import { useEffect, useMemo, useState } from 'react'
import { Confirm } from '@/features/admin/ui'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import {
  ArrowLeft,
  Blocks,
  Bot,
  Brain,
  Cloud,
  Cpu,
  Eye,
  Activity,
  EyeOff,
  FileText,
  Globe,
  IdCard,
  Library,
  Lightbulb,
  Plus,
  SearchX,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  SquareTerminal,
  Trash2,
  UserCog,
  Wrench,
} from 'lucide-react'
import { api } from '@/api/client'
import { useMe, useModels, useTools } from '@/api/queries'
import type { Model } from '@/api/types'
import { cx } from '@/lib/format'
import { IconButton } from '@/ui/Dialog'
import { Spinner, Switch } from '@/ui/misc'
import { toast } from '@/stores/toast'
import { useCollections, useModelRow, useModelRows } from './api'
import { BUILTINS, CAPABILITIES, formToBody, rowToForm, type ModelForm, type ModelRow, type Toggle } from './model-form'
import { serverFieldError, validateModel, type Errors } from './validate'
import { MODEL_FILTERS, filterModels, type ModelCardData, type ModelFilter } from './lib'
import {
  AccessEditor,
  Badge,
  Chip,
  EditorHeader,
  Empty,
  Field,
  FieldError,
  FilterChips,
  GridSkeleton,
  GroupHead,
  ManagedBadge,
  ManagedNote,
  SearchBox,
  Section,
  TabBar,
  Toolbar,
  btn,
  btnPrimary,
  cardLink,
  errorText,
  grid,
  input,
  useTab,
} from './ui'
import { Select } from '@/ui/Select'
import { ModelIcon } from '@/features/header/ModelPicker'

const editLink = (id: string) => `/workspace/models/edit?id=${encodeURIComponent(id)}`

interface CardItem extends ModelCardData {
  row?: ModelRow
  model?: Model
  base?: Model
  baseId?: string
}

export function ModelsList({ canCreate }: { canCreate: boolean }) {
  const { data: me } = useMe()
  const { data: rows, isLoading, error } = useModelRows()
  const { data: models = [] } = useModels()
  const [params, setParams] = useSearchParams()
  const [q, setQ] = useState('')
  const filter = (MODEL_FILTERS.find(([k]) => k === params.get('filter'))?.[0] ?? 'all') as ModelFilter
  const setFilter = (f: ModelFilter) => setParams(f === 'all' ? {} : { filter: f }, { replace: true })
  const isAdmin = me?.role === 'admin'
  const items = useMemo(() => {
    const byId = new Map(models.map((m) => [m.id, m]))
    const rowIds = new Set((rows ?? []).map((r) => r.id))
    const fromRow = (r: ModelRow): CardItem => {
      const model = byId.get(r.id)
      const base = r.base_model_id ? byId.get(r.base_model_id) : undefined
      return {
        id: r.id,
        name: r.name || r.id,
        description: r.meta?.description ?? model?.info?.meta?.description ?? '',
        preset: !!r.base_model_id,
        local: !!(model?.local ?? base?.local),
        hidden: !!(r.meta?.hidden ?? model?.hidden),
        row: r,
        model,
        base,
        baseId: r.base_model_id ?? undefined,
      }
    }
    // Discovered models without a row: admins may give them settings.
    const discovered: CardItem[] = isAdmin
      ? models.filter((m) => !rowIds.has(m.id)).map((m) => ({ id: m.id, name: m.name || m.id, description: m.info?.meta?.description ?? '', preset: false, local: !!m.local, hidden: !!m.hidden, model: m }))
      : []
    return [...(rows ?? []).map(fromRow), ...discovered]
  }, [rows, models, isAdmin])
  const shown = filterModels(items, q, filter)
  const counts = Object.fromEntries(MODEL_FILTERS.map(([k]) => [k, filterModels(items, q, k).length])) as Record<ModelFilter, number>
  const assistants = shown.filter((m) => m.preset)
  const bases = shown.filter((m) => !m.preset)
  const newButton = canCreate && (
    <Link to="/workspace/models/new" className={btnPrimary} data-testid="new-model">
      <Plus size={15} /> New model
    </Link>
  )
  return (
    <div className="space-y-6">
      <Toolbar action={newButton}>
        <SearchBox value={q} onChange={setQ} label="Search models" />
        <FilterChips options={MODEL_FILTERS} value={filter} onChange={setFilter} counts={counts} />
      </Toolbar>
      {isLoading && <GridSkeleton className={grid} />}
      {error && <p className="rounded-xl border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300">{errorText(error)}</p>}
      {!isLoading && !error && items.length === 0 && (
        <Empty icon={Bot} title="No models yet" action={newButton}>
          Assistants are presets built on a base model, with their own prompt, tools and knowledge.
        </Empty>
      )}
      {items.length > 0 && shown.length === 0 && (
        <Empty
          icon={SearchX}
          title="No models match"
          action={
            <button type="button" className={btn} onClick={() => (setQ(''), setFilter('all'))}>
              Clear search and filters
            </button>
          }
        >
          Try another search or filter.
        </Empty>
      )}
      {assistants.length > 0 && (
        <section>
          <GroupHead title="Assistants" count={assistants.length} hint="Presets with their own prompt, tools and knowledge" />
          <div className={grid}>
            {assistants.map((m) => (
              <ModelCard key={m.id} m={m} />
            ))}
          </div>
        </section>
      )}
      {bases.length > 0 && (
        <section>
          <GroupHead title="Base models" count={bases.length} hint="Settings that apply to every chat with the model and to presets built on it" />
          <div className={grid}>
            {bases.map((m) => (
              <ModelCard key={m.id} m={m} />
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

function ModelCard({ m }: { m: CardItem }) {
  const { row, model, base } = m
  const f = useMemo(() => (row ? rowToForm(row) : undefined), [row])
  const icon: Model = { ...(model ?? { id: m.id, name: m.name }), profileImageUrl: row?.meta?.profile_image_url || model?.profileImageUrl }
  const tools = f ? f.servers.length + Object.values(f.builtins).filter((v) => v === 'on').length : 0
  return (
    <Link to={editLink(m.id)} className={cardLink} data-testid="model-row" data-model-id={m.id}>
      <div className="flex items-start gap-3">
        <ModelIcon m={icon} size="lg" />
        <div className="min-w-0 flex-1 pt-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <h3 className="truncate font-semibold tracking-tight group-hover:text-accent dark:group-hover:text-accent-bright">{m.name}</h3>
            {row?.managed && <ManagedBadge compact />}
          </div>
          <p className="truncate font-mono text-xs text-muted" title={m.id}>
            {m.id}
          </p>
          {(row?.is_active === false || (row && !model) || (model?.kind && model.kind !== 'chat')) && (
            <div className="mt-1 flex flex-wrap gap-1">
              {row?.is_active === false && <Badge tone="red">Inactive</Badge>}
              {row && !model && (
                <Badge tone="amber" title="The base model is not available right now">
                  Unavailable
                </Badge>
              )}
              {model?.kind && model.kind !== 'chat' && <Badge>{model.kind}</Badge>}
            </div>
          )}
        </div>
      </div>
      <p className={cx('mt-3 line-clamp-3 text-sm leading-relaxed', m.description ? 'text-fg/80' : 'italic text-muted/80')}>{m.description || (row ? 'No description' : 'Default settings')}</p>
      <div className="mt-auto flex flex-wrap items-center gap-1.5 pt-4">
        {m.baseId && (
          <span title={`Base model: ${m.baseId}`} className="inline-flex min-w-0 max-w-full items-center gap-1 rounded-full border border-line bg-surface-2/60 py-0.5 pl-0.5 pr-2 text-[11px] font-medium text-muted">
            {base ? <ModelIcon m={base} size="xs" /> : <Cpu size={12} className="ml-1" aria-hidden />}
            <span className="truncate">{base?.name || m.baseId}</span>
          </span>
        )}
        {m.local ? (
          <Chip icon={ShieldCheck} tone="accent" title="Runs on this server: data stays here">
            On server
          </Chip>
        ) : (
          <Chip icon={Cloud} title="Runs with a cloud provider">
            Cloud
          </Chip>
        )}
        {f && f.webSearch !== 'off' && <Chip icon={Globe} title={f.webSearch === 'native' ? "Web search: the provider's own" : 'Web search: Kera web tools'} />}
        {f?.codeInterpreter && <Chip icon={SquareTerminal} title="Code interpreter" />}
        {f && f.knowledge.length > 0 && (
          <Chip icon={Library} title={`${f.knowledge.length} knowledge collection${f.knowledge.length === 1 ? '' : 's'}`}>
            {f.knowledge.length}
          </Chip>
        )}
        {tools > 0 && (
          <Chip icon={Wrench} title={`${tools} tool${tools === 1 ? '' : 's'}`}>
            {tools}
          </Chip>
        )}
        {m.hidden && (
          <Chip icon={EyeOff} title="Hidden from the model picker">
            Hidden
          </Chip>
        )}
      </div>
    </Link>
  )
}

const blank: ModelForm = rowToForm({ id: '', name: '' })

type TabKey = 'general' | 'prompt' | 'capabilities' | 'tools' | 'knowledge' | 'suggestions' | 'access'
const TAB_KEYS: TabKey[] = ['general', 'prompt', 'capabilities', 'tools', 'knowledge', 'suggestions', 'access']

function Count({ n }: { n: number }) {
  return n > 0 ? <span className="rounded-full bg-surface-2 px-1.5 text-[11px] tabular-nums text-muted">{n}</span> : null
}

export function ModelEditor({ create = false }: { create?: boolean }) {
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [params] = useSearchParams()
  const id = create ? undefined : (params.get('id') ?? undefined)
  const { data: row, error, isLoading } = useModelRow(id)
  const { data: me } = useMe()
  const { data: models = [] } = useModels()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [f, setF] = useState<ModelForm>(blank)
  const [saving, setSaving] = useState(false)
  const [tab, setTab] = useTab(TAB_KEYS, 'general')
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set())
  const [serverErr, setServerErr] = useState<Errors>({})
  const { data: rows } = useModelRows()
  const existingIds = useMemo(() => [...models.map((m) => m.id), ...(rows ?? []).map((r) => r.id)], [models, rows])
  const initial = useMemo(() => (row ? rowToForm(row) : blank), [row])
  useEffect(() => setF(initial), [initial])
  if (!create && isLoading)
    return (
      <div className="grid place-items-center p-10 text-muted">
        <Spinner size={20} />
      </div>
    )
  if (!create && error) return <p className="rounded-xl border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300">{errorText(error)}</p>
  const set = <K extends keyof ModelForm>(k: K, v: ModelForm[K]) => {
    setF((x) => ({ ...x, [k]: v }))
    setTouched((t) => (t.has(k) ? t : new Set(t).add(k)))
    setServerErr(({ [k]: _, ...rest }) => rest)
  }
  const isPreset = create || !!row?.base_model_id
  const dirty = JSON.stringify(f) !== JSON.stringify(initial)
  const allErrors: Errors = { ...validateModel(f, { create, isPreset, existingIds }), ...serverErr }
  // A new model shows an error once its field is touched; saved models show every error.
  const errors = create ? Object.fromEntries(Object.entries(allErrors).filter(([k]) => touched.has(k) || k in serverErr)) : allErrors
  const firstError = Object.values(allErrors)[0]
  const tabHasError = (keys: string[]) => keys.some((k) => k in allErrors)
  const save = async () => {
    if (firstError) return toast(firstError, 'error')
    let body
    try {
      body = formToBody(f)
    } catch (e) {
      return toast(errorText(e), 'error')
    }
    setSaving(true)
    try {
      const saved = create ? await api<ModelRow>('/api/v1/models/create', { method: 'POST', json: body }) : await api<ModelRow>(`/api/v1/models/model/update?id=${encodeURIComponent(f.id)}`, { method: 'POST', json: body })
      await Promise.all([qc.invalidateQueries({ queryKey: ['ws'] }), qc.invalidateQueries({ queryKey: ['models'] })])
      toast('Model saved')
      if (create) navigate(editLink(saved.id), { replace: true })
    } catch (e) {
      const fe = serverFieldError(e)
      if (fe) {
        setServerErr({ [fe[0]]: fe[1] })
        setTab('general')
      }
      toast(errorText(e), 'error')
    } finally {
      setSaving(false)
    }
  }
  const remove = async () => {
    try {
      await api(`/api/v1/models/model/delete?id=${encodeURIComponent(f.id)}`, { method: 'DELETE' })
      await Promise.all([qc.invalidateQueries({ queryKey: ['ws'] }), qc.invalidateQueries({ queryKey: ['models'] })])
      navigate('/workspace/models')
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }
  const hasRow = (row?.created_at ?? 0) > 0 // a discovered model without settings comes back unsaved
  const model = models.find((m) => m.id === f.id)
  const icon: Model = { ...(model ?? { id: f.id || '?', name: f.name || (create ? 'New model' : f.id) }), profileImageUrl: f.image || model?.profileImageUrl }
  const showAccess = me?.role === 'admin' && !!f.id && !create
  const tabs = [
    { key: 'general' as const, label: 'General', icon: IdCard, error: tabHasError(['id', 'name', 'baseModelId']) },
    { key: 'prompt' as const, label: 'Prompt', icon: FileText, error: tabHasError(['paramsText']) },
    { key: 'capabilities' as const, label: 'Capabilities', icon: Sparkles },
    { key: 'tools' as const, label: 'Tools', icon: Wrench, badge: <Count n={f.servers.length + Object.values(f.builtins).filter((v) => v === 'on').length} /> },
    { key: 'knowledge' as const, label: 'Knowledge', icon: Library, badge: <Count n={f.knowledge.length} />, error: tabHasError(['knowledge']) },
    { key: 'suggestions' as const, label: 'Suggestions', icon: Lightbulb, badge: <Count n={f.suggestions.length} /> },
    ...(showAccess ? [{ key: 'access' as const, label: 'Access', icon: UserCog }] : []),
  ]
  const current = tabs.some((t) => t.key === tab) ? tab : 'general'
  return (
    <form
      className="space-y-5"
      onSubmit={(e) => {
        e.preventDefault()
        void save()
      }}
      data-testid="model-editor"
    >
      <EditorHeader
        back={
          <Link to="/workspace/models" className="grid size-9 shrink-0 place-items-center rounded-lg border border-line bg-surface text-muted hover:bg-surface-2 hover:text-fg" aria-label="Back to models" title="Back to models">
            <ArrowLeft size={16} />
          </Link>
        }
        icon={<ModelIcon m={icon} size="md" />}
        title={
          <>
            <span className="truncate">{create ? 'New model' : f.name || f.id}</span>
            {row?.managed && <ManagedBadge compact />}
          </>
        }
        subtitle={create ? 'An assistant built on a base model' : <span className="font-mono">{f.id}</span>}
        actions={
          <>
            {dirty && <span className="hidden text-sm text-muted md:inline">Unsaved changes</span>}
            {dirty && !create && (
              <button type="button" className={cx(btn, 'hidden sm:inline-flex')} onClick={() => setF(initial)}>
                Discard
              </button>
            )}
            {hasRow && (
              <>
                <Confirm open={confirmDelete} title="Delete model?" body={`${f.name || f.id} is deleted. Chats keep their messages.`} onConfirm={remove} onClose={() => setConfirmDelete(false)} />
                <IconButton label="Delete model" onClick={() => setConfirmDelete(true)}>
                  <Trash2 size={16} />
                </IconButton>
              </>
            )}
            <button type="submit" className={btnPrimary} disabled={saving || !!firstError} title={firstError} data-testid="save-model">
              {saving ? <Spinner /> : null} Save
            </button>
          </>
        }
      >
        <TabBar tabs={tabs} value={current} onChange={setTab} label="Model settings" />
      </EditorHeader>
      {row?.managed && current === 'general' && <ManagedNote what="This model" />}
      {current === 'general' && <General f={f} set={set} create={create} isPreset={isPreset} row={row} errors={errors} />}
      {current === 'prompt' && <PromptTab f={f} set={set} isPreset={isPreset} errors={errors} />}
      {current === 'capabilities' && <Capabilities f={f} set={set} />}
      {current === 'tools' && <Tools f={f} set={set} />}
      {current === 'knowledge' && <KnowledgeRefs f={f} set={set} error={errors.knowledge} />}
      {current === 'suggestions' && <Suggestions f={f} set={set} />}
      {current === 'access' && showAccess && (
        <Section title="Access" icon={UserCog} hint="Who can use or edit this model." className="max-w-4xl">
          <AccessEditor url={`/api/v1/grants/model/${encodeURIComponent(f.id)}`} canEdit />
        </Section>
      )}
    </form>
  )
}

type SetFn = <K extends keyof ModelForm>(k: K, v: ModelForm[K]) => void

const check = 'size-4 shrink-0 accent-[var(--accent)]'
/** A checkbox drawn as a selectable tile. */
const checkTile = 'flex cursor-pointer items-center gap-2.5 rounded-xl border border-line px-3 py-2 text-sm transition-colors hover:bg-surface-2/60 has-[:checked]:border-accent/50 has-[:checked]:bg-accent-soft'
const human = (s: string) => s.replaceAll('_', ' ').replace(/^./, (c) => c.toUpperCase())

function General({ f, set, create, isPreset, row, errors }: { f: ModelForm; set: SetFn; create: boolean; isPreset: boolean; row?: ModelRow; errors: Errors }) {
  const { data: models = [] } = useModels()
  const onImage = (file?: File) => {
    if (!file) return
    if (file.size > 256 * 1024) return toast('Pick an image under 256 KB', 'error')
    const r = new FileReader()
    r.onload = () => set('image', String(r.result))
    r.readAsDataURL(file)
  }
  return (
    <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <Section
        title="Identity"
        icon={IdCard}
        hint={!create && !isPreset ? `Settings for the base model${row?.connection_id ? ` from connection ${row.connection_id}` : ''}. They apply to every chat with it and to presets built on it.` : 'How the model appears in the model picker.'}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name" error={errors.name}>
            <input className={input} value={f.name} required aria-invalid={!!errors.name} onChange={(e) => set('name', e.target.value)} data-testid="model-name" />
          </Field>
          <Field label="Model id" error={errors.id} hint={create ? 'Lowercase letters, digits and . _ : -; used in URLs and the API and cannot change later.' : undefined}>
            <input className={cx(input, 'font-mono')} value={f.id} required aria-invalid={!!errors.id} disabled={!create} onChange={(e) => set('id', e.target.value)} data-testid="model-id" />
          </Field>
          {isPreset && (
            <Field label="Base model" className="sm:col-span-2" error={errors.baseModelId}>
              <Select
                value={f.baseModelId}
                onChange={(v) => set('baseModelId', v)}
                aria-label="Base model"
                placeholder="Choose..."
                data-testid="base-model"
                options={[
                  ...models.filter((m) => m.id !== f.id).map((m) => ({ value: m.id, label: (m.name || m.id) + (m.hidden ? ' (hidden)' : ''), icon: <ModelIcon m={m} size="sm" /> })),
                  ...(f.baseModelId && !models.some((m) => m.id === f.baseModelId) ? [{ value: f.baseModelId, label: `${f.baseModelId} (unavailable)` }] : []),
                ]}
              />
            </Field>
          )}
          <Field label="Description" className="sm:col-span-2" hint="Shown under the name in the model picker and on the new-chat screen.">
            <textarea className={cx(input, 'min-h-20 resize-y')} value={f.description} onChange={(e) => set('description', e.target.value)} aria-label="Description" />
          </Field>
          <Field label="Image" hint="An image URL, or upload one (256 KB max)." className="sm:col-span-2">
            <div className="flex items-center gap-2">
              {f.image ? <img src={f.image} alt="" className="size-10 shrink-0 rounded-xl border border-line object-cover" /> : <span className="grid size-10 shrink-0 place-items-center rounded-xl border border-dashed border-line text-muted"><Bot size={18} /></span>}
              <input className={input} value={f.image.startsWith('data:') ? '(uploaded image)' : f.image} onChange={(e) => set('image', e.target.value)} aria-label="Image URL" />
              <label className={cx(btn, 'relative cursor-pointer')}>
                Upload
                <input type="file" accept="image/*" className="sr-only" onChange={(e) => onImage(e.target.files?.[0])} />
              </label>
              {f.image && (
                <IconButton label="Remove image" onClick={() => set('image', '')}>
                  <Trash2 size={15} />
                </IconButton>
              )}
            </div>
          </Field>
        </div>
        {(f.baseModelId || (!isPreset && f.id)) && <TestModel model={isPreset ? f.baseModelId : f.id} />}
      </Section>
      <Section title="Visibility" icon={Eye}>
        <label className="flex items-start justify-between gap-4 text-sm">
          <span>
            <span className="block font-medium">Show in the model picker</span>
            <span className="block text-xs text-muted">Hidden models still work through links and the API.</span>
          </span>
          <Switch checked={!f.hidden} onChange={(v) => set('hidden', !v)} label="Show in the model picker" />
        </label>
        <label className="flex items-start justify-between gap-4 border-t border-line pt-4 text-sm">
          <span>
            <span className="block font-medium">Active</span>
            <span className="block text-xs text-muted">Inactive models cannot be used at all.</span>
          </span>
          <Switch checked={f.active} onChange={(v) => set('active', v)} label="Active" />
        </label>
      </Section>
    </div>
  )
}

function PromptTab({ f, set, isPreset, errors }: { f: ModelForm; set: SetFn; isPreset: boolean; errors: Errors }) {
  return (
    <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <Section title="System prompt" icon={FileText} hint={<>Variables: {'{{CURRENT_DATE}} {{CURRENT_TIME}} {{CURRENT_WEEKDAY}} {{USER_NAME}} {{USER_EMAIL}} {{USER_LANGUAGE}}'}</>}>
        <textarea
          className={cx(input, 'min-h-[22rem] resize-y font-mono text-[13px] leading-relaxed xl:min-h-[calc(100dvh-22rem)]')}
          value={f.system}
          onChange={(e) => set('system', e.target.value)}
          aria-label="System prompt"
          spellCheck={false}
          placeholder={isPreset ? 'Empty: inherit the base model prompt' : ''}
        />
        <p className="text-right text-xs tabular-nums text-muted">{f.system.length.toLocaleString()} characters</p>
      </Section>
      <Section title="Parameters" icon={SlidersHorizontal} hint="A JSON object. Request values win; these fill the gaps (num_ctx, think, temperature, reasoning_effort, compact_token_threshold...).">
        <textarea
          className={cx(input, 'min-h-64 resize-y font-mono text-[13px]')}
          value={f.paramsText}
          onChange={(e) => set('paramsText', e.target.value)}
          aria-label="Parameters (JSON)"
          aria-invalid={!!errors.paramsText}
          spellCheck={false}
          placeholder={'{"temperature": 0.3, "num_ctx": 65536, "think": false, "reasoning_effort": "low"}'}
        />
        <FieldError error={errors.paramsText} />
      </Section>
    </div>
  )
}

function Capabilities({ f, set }: { f: ModelForm; set: SetFn }) {
  const toggleDefault = (id: string, on: boolean) => set('defaultFeatures', on ? [...new Set([...f.defaultFeatures, id])] : f.defaultFeatures.filter((d) => d !== id))
  return (
    <div className="grid items-start gap-5 xl:grid-cols-2">
      <Section title="Capabilities" icon={Sparkles} hint="What the model can take and show.">
        <div className="grid gap-2 sm:grid-cols-2">
          {CAPABILITIES.map((c) => (
            <label key={c} className={checkTile}>
              <input type="checkbox" className={check} checked={f.capabilities[c] === true} onChange={(e) => set('capabilities', { ...f.capabilities, [c]: e.target.checked })} />
              {human(c)}
            </label>
          ))}
        </div>
      </Section>
      <Section title="Features" icon={Blocks}>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Web search" hint="Native: the provider's own search (Responses connections).">
            <Select
              value={f.webSearch}
              onChange={(v) => set('webSearch', v as ModelForm['webSearch'])}
              aria-label="Web search"
              options={[
                { value: 'off', label: 'Off' },
                { value: 'builtin', label: 'Kera web tools' },
                { value: 'native', label: 'Native' },
              ]}
            />
          </Field>
          <Field label="Chat attachments" hint="Tools: the model reads files with tools. Full: text goes into the prompt.">
            <Select
              value={f.fileContext}
              onChange={(v) => set('fileContext', v as ModelForm['fileContext'])}
              aria-label="Chat attachments"
              options={[
                { value: 'tools', label: 'Read with tools' },
                { value: 'full', label: 'Whole text in the prompt' },
              ]}
            />
          </Field>
        </div>
        <div className="grid gap-2 sm:grid-cols-2">
          <label className="flex items-center justify-between gap-3 rounded-xl border border-line px-3 py-2 text-sm">
            <span className="flex items-center gap-2">
              <SquareTerminal size={15} className="text-muted" /> Code interpreter
            </span>
            <Switch checked={f.codeInterpreter} onChange={(v) => set('codeInterpreter', v)} label="Code interpreter" />
          </label>
          <label className="flex items-center justify-between gap-3 rounded-xl border border-line px-3 py-2 text-sm">
            <span className="flex items-center gap-2">
              <Brain size={15} className="text-muted" /> Memory
            </span>
            <Switch checked={f.memory} onChange={(v) => set('memory', v)} label="Memory" />
          </label>
        </div>
        <div className="space-y-2 border-t border-line pt-4">
          <p className="text-xs font-medium text-muted">On by default in new chats</p>
          <div className="flex flex-wrap gap-2">
            {(['web_search', 'code_interpreter', 'memory'] as const).map((id) => (
              <label key={id} className={checkTile}>
                <input type="checkbox" className={check} checked={f.defaultFeatures.includes(id)} onChange={(e) => toggleDefault(id, e.target.checked)} />
                {human(id)}
              </label>
            ))}
          </div>
        </div>
      </Section>
    </div>
  )
}

function Tools({ f, set }: { f: ModelForm; set: SetFn }) {
  const { data: servers = [], isError } = useTools()
  const known = servers.filter((s) => !s.id.startsWith('builtin:'))
  const ids = [...new Set([...known.map((s) => s.id.replace(/^server:/, '')), ...f.servers])]
  const [extra, setExtra] = useState('')
  const addExtra = () => {
    if (!extra.trim()) return
    set('servers', [...new Set([...f.servers, extra.trim()])])
    setExtra('')
  }
  return (
    <div className="grid items-start gap-5 xl:grid-cols-2">
      <Section title="Tool servers" icon={Wrench} hint="MCP and OpenAPI servers this model may call.">
        {ids.length === 0 && <p className="text-sm text-muted">{isError ? 'Tool servers could not be listed.' : 'No tool servers configured.'}</p>}
        <div className="grid gap-2 sm:grid-cols-2">
          {ids.map((id) => (
            <label key={id} className={checkTile}>
              <input type="checkbox" className={check} checked={f.servers.includes(id)} onChange={(e) => set('servers', e.target.checked ? [...f.servers, id] : f.servers.filter((s) => s !== id))} />
              <span className="min-w-0 truncate">{known.find((s) => s.id.replace(/^server:/, '') === id)?.name ?? id}</span>
            </label>
          ))}
        </div>
        <div className="flex gap-2">
          <input
            className={cx(input, 'max-w-xs py-1.5')}
            placeholder="Tool server id"
            value={extra}
            onChange={(e) => setExtra(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), addExtra())}
            aria-label="Add a tool server by id"
          />
          <button type="button" className={btn} disabled={!extra.trim()} onClick={addExtra}>
            <Plus size={14} /> Add
          </button>
        </div>
      </Section>
      <Section title="Built-in tools" icon={Blocks} hint="Default follows the server settings.">
        <div className="grid gap-2 sm:grid-cols-2 2xl:grid-cols-3">
          {[...new Set([...BUILTINS, ...Object.keys(f.builtins)])].map((b) => (
            <label key={b} className="flex items-center justify-between gap-2 rounded-xl border border-line py-1 pl-3 pr-1 text-sm">
              <span className="truncate">{human(b)}</span>
              <Select
                aria-label={`Built-in ${b}`}
                value={f.builtins[b] ?? 'default'}
                onChange={(v) => set('builtins', { ...f.builtins, [b]: v as Toggle })}
                size="sm"
                variant="pill"
                tone={f.builtins[b] === 'on' ? 'accent' : 'grey'}
                className="shrink-0"
                options={[
                  { value: 'default', label: 'default' },
                  { value: 'on', label: 'on' },
                  { value: 'off', label: 'off' },
                ]}
              />
            </label>
          ))}
        </div>
      </Section>
    </div>
  )
}

function KnowledgeRefs({ f, set, error }: { f: ModelForm; set: SetFn; error?: string }) {
  const { data: colls = [] } = useCollections()
  const label = (k: { id?: string; name?: string }) => colls.find((c) => c.id === k.id || (!k.id && c.name === k.name))?.name ?? k.name ?? k.id ?? ''
  const attached = (id: string, name: string) => f.knowledge.some((k) => k.id === id || (!k.id && k.name === name))
  return (
    <Section
      title="Knowledge"
      icon={Library}
      hint="Collections the model searches or reads in full."
      aside={
        <Select
          aria-label="Attach knowledge"
          className="w-64 max-w-full"
          value=""
          placeholder="Attach a collection..."
          onChange={(v) => v && set('knowledge', [...f.knowledge, { id: v, mode: '' }])}
          options={colls.filter((c) => !attached(c.id, c.name)).map((c) => ({ value: c.id, label: c.name }))}
        />
      }
    >
      <FieldError error={error} />
      {f.knowledge.length === 0 && <p className="text-sm text-muted">No knowledge attached.</p>}
      <ul className="grid gap-2 lg:grid-cols-2">
        {f.knowledge.map((k, i) => {
          const c = colls.find((c) => c.id === k.id || (!k.id && c.name === k.name))
          return (
            <li key={`${k.id ?? ''}|${k.name ?? ''}`} className="flex flex-wrap items-center gap-2 rounded-xl border border-line p-2.5 text-sm sm:flex-nowrap">
              <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-accent-soft text-accent dark:text-accent-bright">
                <Library size={16} />
              </span>
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5">
                  <span className="truncate font-medium">{label(k)}</span>
                  {!k.id && <Badge title="Referenced by name">by name</Badge>}
                  {!c && <Badge tone="amber">not found</Badge>}
                </span>
                {c && <span className="block text-xs text-muted">{c.status.files} file{c.status.files === 1 ? '' : 's'}</span>}
              </span>
              <Select
                aria-label="Knowledge mode"
                value={k.mode && k.mode !== 'auto' ? k.mode : ''}
                onChange={(v) => set('knowledge', f.knowledge.map((x, j) => (j === i ? { ...x, mode: v } : x)))}
                size="sm"
                className="w-auto shrink-0"
                options={[
                  { value: '', label: 'Automatic', description: 'Relevant parts in the prompt when a reranker works; otherwise the model searches with tools' },
                  { value: 'retrieve', label: 'Relevant parts in the prompt' },
                  { value: 'tools', label: 'Model searches with tools' },
                  { value: 'full', label: 'Whole documents in the prompt' },
                ]}
              />
              <IconButton label="Detach" onClick={() => set('knowledge', f.knowledge.filter((_, j) => j !== i))}>
                <Trash2 size={15} />
              </IconButton>
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

function Suggestions({ f, set }: { f: ModelForm; set: SetFn }) {
  const upd = (i: number, v: Partial<ModelForm['suggestions'][number]>) => set('suggestions', f.suggestions.map((s, j) => (j === i ? { ...s, ...v } : s)))
  return (
    <Section
      title="Suggestions"
      icon={Lightbulb}
      hint="Starter prompts on the new-chat screen."
      aside={
        <button type="button" className={btn} onClick={() => set('suggestions', [...f.suggestions, { title: [], content: '' }])}>
          <Plus size={14} /> Add
        </button>
      }
    >
      {f.suggestions.length === 0 && <p className="text-sm text-muted">No starter suggestions.</p>}
      <div className="grid gap-3 lg:grid-cols-2">
        {f.suggestions.map((s, i) => (
          <div key={i} className="grid gap-2 rounded-xl border border-line bg-surface-2/20 p-3 sm:grid-cols-[1fr_1fr_auto]">
            <input className={input} placeholder="Title" value={s.title?.[0] ?? ''} onChange={(e) => upd(i, { title: [e.target.value, s.title?.[1] ?? ''] })} aria-label="Suggestion title" />
            <input className={input} placeholder="Subtitle" value={s.title?.[1] ?? ''} onChange={(e) => upd(i, { title: [s.title?.[0] ?? '', e.target.value] })} aria-label="Suggestion subtitle" />
            <IconButton label="Remove suggestion" onClick={() => set('suggestions', f.suggestions.filter((_, j) => j !== i))}>
              <Trash2 size={15} />
            </IconButton>
            <textarea className={cx(input, 'min-h-20 resize-y sm:col-span-3')} placeholder="Prompt sent when clicked" value={s.content} onChange={(e) => upd(i, { content: e.target.value })} aria-label="Suggestion prompt" />
          </div>
        ))}
      </div>
    </Section>
  )
}

/** Sends "Reply with OK" to a model through the chat API (temporary: nothing is saved) and shows the latency. */
function TestModel({ model }: { model: string }) {
  const [st, setSt] = useState<{ busy?: boolean; ms?: number; reply?: string; error?: string }>({})
  useEffect(() => setSt({}), [model])
  const run = async () => {
    setSt({ busy: true })
    const t0 = performance.now()
    try {
      const r = await api<{ choices?: { message?: { content?: string } }[] }>('/api/chat/completions', {
        method: 'POST',
        json: { model, messages: [{ role: 'user', content: 'Reply with OK' }], stream: false, temporary: true },
      })
      setSt({ ms: Math.round(performance.now() - t0), reply: r?.choices?.[0]?.message?.content?.trim() })
    } catch (e) {
      setSt({ error: errorText(e) })
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line pt-4 text-sm" data-testid="model-test">
      <button type="button" className={btn} onClick={() => void run()} disabled={st.busy} data-testid="test-model">
        {st.busy ? <Spinner /> : <Activity size={14} />} Test
      </button>
      {!st.busy && st.ms === undefined && !st.error && <span className="text-xs text-muted">Sends "Reply with OK" to <span className="font-mono">{model}</span>; nothing is saved.</span>}
      {st.ms !== undefined && (
        <span className="min-w-0 text-green-700 dark:text-green-400" data-testid="test-ok">
          Responded in {st.ms.toLocaleString()} ms{st.reply ? <span className="text-muted">: “{st.reply.slice(0, 60)}”</span> : null}
        </span>
      )}
      {st.error && (
        <span className="min-w-0 break-words text-red-600 dark:text-red-400" data-testid="test-error">
          {st.error}
        </span>
      )}
    </div>
  )
}
