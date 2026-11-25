// Step 2: which assistant does the work (cards), its switches, and the knowledge it may search.
import { useMemo, useState, type Dispatch } from 'react'
import { Check, Library, Search, ShieldCheck } from 'lucide-react'
import { useModels } from '@/api/queries'
import type { FileRef, Model } from '@/api/types'
import { cx } from '@/lib/format'
import { iconFor } from '@/lib/icons'
import { useComposer } from '@/stores/composer'
import { isChatModel } from '@/features/chat/model-select'
import { ModelIcon } from '@/features/header/ModelPicker'
import { useCollections, type Collection } from '@/features/workspace/api'
import { input } from '@/features/workspace/ui'
import type { Action, Draft } from '../draft'
import type { Errors } from '../steps'
import { Block, Pill, RemovableChip, StepHeader, choiceCard } from '../ui'

/** The assistants a person may pick: chat models, and a hidden one only when it is already the choice. */
export const pickableModels = (models: Model[] | undefined, current: string) => (models ?? []).filter((m) => isChatModel(m) && (!m.hidden || m.id === current))

export function AssistantStep({ draft, dispatch, errors, onPick }: { draft: Draft; dispatch: Dispatch<Action>; errors: Errors; onPick: (m: Model) => void }) {
  const models = useModels()
  const model = useComposer((s) => s.model)
  const features = useComposer((s) => s.features)
  const setFeature = useComposer((s) => s.setFeature)
  const [query, setQuery] = useState('')
  const all = pickableModels(models.data, model)
  const q = query.trim().toLowerCase()
  const shown = q ? all.filter((m) => `${m.name} ${m.id} ${m.info?.meta?.description ?? ''}`.toLowerCase().includes(q)) : all
  const current = all.find((m) => m.id === model)

  return (
    <div className="space-y-7">
      <StepHeader title="Who should do it?">Pick the assistant. Each one has its own instructions, knowledge and tools, and some run on this server so your data stays here.</StepHeader>

      <Block title="Assistant">
        {all.length > 6 && (
          <div className="relative max-w-sm">
            <Search size={15} aria-hidden className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted" />
            <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search assistants" aria-label="Search assistants" className={cx(input, 'pl-9')} data-testid="assistant-search" />
          </div>
        )}
        <div role="radiogroup" aria-label="Assistant" aria-invalid={!!errors.model} className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3" data-testid="assistants">
          {shown.map((m) => (
            <label key={m.id} data-testid="model-option" data-model-id={m.id} className={choiceCard}>
              <input type="radio" name="assistant" value={m.id} checked={m.id === model} onChange={() => onPick(m)} className="peer sr-only" />
              <ModelIcon m={m} size="md" />
              <span className="flex min-w-0 flex-1 flex-col pr-5">
                <span className="truncate font-medium">{m.name || m.id}</span>
                {m.info?.meta?.description && <span className="mt-0.5 line-clamp-2 text-xs leading-snug text-muted">{m.info.meta.description}</span>}
                <span className="mt-auto flex pt-2">
                  {m.local ? (
                    <Pill tone="accent" title="Runs on this server: your data stays here">
                      <ShieldCheck size={11} aria-hidden /> On server
                    </Pill>
                  ) : (
                    <Pill tone="grey" title="Runs at an outside provider: what the assistant reads is sent there">
                      Hosted
                    </Pill>
                  )}
                </span>
              </span>
              <Check size={16} aria-hidden className="absolute right-3 top-3 text-accent opacity-0 peer-checked:opacity-100" />
            </label>
          ))}
        </div>
        {models.data && !shown.length && <p className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-sm text-muted">{all.length ? 'No assistant matches that search.' : 'No assistants are available to you yet. Ask an admin to give you access to one.'}</p>}
        {errors.model && (
          <p role="alert" data-testid="field-error" className="text-xs font-medium text-red-600 dark:text-red-400">
            {errors.model}
          </p>
        )}
      </Block>

      {!!current?.features?.length && (
        <Block title="Abilities" hint={`Switches this assistant offers. They start as ${current.name || 'its'} defaults.`}>
          <div className="flex flex-wrap gap-2" role="group" aria-label="Abilities">
            {current.features.map((f) => {
              const on = !!features[f.id]
              const Icon = iconFor(f.icon)
              return (
                <button
                  key={f.id}
                  type="button"
                  data-testid={`pill-${f.id}`}
                  aria-pressed={on}
                  onClick={() => setFeature(f.id, !on)}
                  className={cx('inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-sm transition-colors motion-reduce:transition-none', on ? 'border-accent bg-accent-soft text-fg' : 'border-line text-muted hover:bg-surface-2')}
                >
                  <Icon size={14} aria-hidden />
                  {f.label}
                </button>
              )
            })}
          </div>
        </Block>
      )}

      <Knowledge value={draft.collections} onAdd={(c) => dispatch({ type: 'addCollection', collection: c })} onRemove={(id) => dispatch({ type: 'removeCollection', id })} />
    </div>
  )
}

/** Knowledge collections the user can read, attached to the goal as {type:'collection'} files. */
export function Knowledge({ value, onAdd, onRemove }: { value: FileRef[]; onAdd: (c: FileRef) => void; onRemove: (id: string) => void }) {
  const list = useCollections()
  const [query, setQuery] = useState('')
  const q = query.trim().toLowerCase()
  const shown = useMemo(() => (list.data ?? []).filter((c) => !q || `${c.name} ${c.description}`.toLowerCase().includes(q)), [list.data, q])
  if (!list.data?.length) return null
  const picked = new Set(value.map((v) => v.id))
  return (
    <Block title="Knowledge" hint="Optional. Collections the assistant can search while it works. Only ones you can read are listed.">
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-2" aria-label="Selected knowledge">
          {value.map((c) => (
            <RemovableChip key={c.id} testId="knowledge-chip" id={c.id} label={`Remove ${c.name}`} onRemove={() => onRemove(c.id)}>
              {c.name}
            </RemovableChip>
          ))}
        </ul>
      )}
      <div className="overflow-hidden rounded-xl border border-line bg-surface">
        {list.data.length > 5 && (
          <div className="relative border-b border-line">
            <Search size={15} aria-hidden className="pointer-events-none absolute left-3.5 top-1/2 -translate-y-1/2 text-muted" />
            <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search knowledge" aria-label="Search knowledge" data-testid="knowledge-picker" className="w-full bg-transparent py-2.5 pl-10 pr-3 text-sm outline-none focus-visible:outline-none" />
          </div>
        )}
        <ul className="max-h-60 divide-y divide-line overflow-y-auto" aria-label="Knowledge collections">
          {shown.map((c: Collection) => (
            <li key={c.id}>
              <label className="flex cursor-pointer items-center gap-3 px-3.5 py-2.5 hover:bg-surface-2/60 has-[:focus-visible]:bg-surface-2">
                <input type="checkbox" checked={picked.has(c.id)} onChange={(e) => (e.target.checked ? onAdd({ id: c.id, name: c.name, type: 'collection' }) : onRemove(c.id))} data-testid={`knowledge-${c.id}`} className="size-4 shrink-0 accent-[var(--accent)]" />
                <Library size={15} aria-hidden className="shrink-0 text-muted" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{c.name}</span>
                  {c.description && <span className="block truncate text-xs text-muted">{c.description}</span>}
                </span>
                <span className="shrink-0 text-xs tabular-nums text-muted">{c.status.completed === 1 ? '1 file' : `${c.status.completed} files`}</span>
              </label>
            </li>
          ))}
          {!shown.length && <li className="px-4 py-5 text-center text-sm text-muted">No collection matches that search.</li>}
        </ul>
      </div>
    </Block>
  )
}
