// Forms over runtime settings (GET/POST /api/v1/configs/settings).
import { useState, type ReactNode } from 'react'
import { RotateCcw } from 'lucide-react'
import { useModels } from '@/api/queries'
import { Switch } from '@/ui/misc'
import { cx } from '@/lib/format'
import { useConnections, useSettings, useSaveSettings, type Setting } from '../api'
import { humanize } from '../lib'
import { attempt, Btn, Card, inputCls, Loading, ManagedBadge, managedWarning } from '../ui'
import { Select } from '@/ui/Select'
import { ModelIcon } from '@/features/header/ModelPicker'

export type Kind = 'bool' | 'int' | 'float' | 'text' | 'textarea' | 'json' | 'lines' | 'secret' | 'model' | 'connection' | { select: [string, string][] }

export interface FieldSpec {
  key: string
  label?: string
  hint?: string
  kind: Kind
}

export interface Draft {
  settings?: Map<string, Setting>
  isLoading: boolean
  error: unknown
  get(key: string): unknown
  set(key: string, v: unknown): void
  dirty: boolean
  changed(key: string): boolean
  /** Posts the changed keys; number fields listed in kinds are converted first. */
  save(kinds?: Map<string, Kind>): Promise<void>
  discard(): void
  reset(key: string): Promise<void>
}

/** Edits a set of settings locally until save(); only changed keys are posted. */
export function useDraft(): Draft {
  const q = useSettings()
  const saveSettings = useSaveSettings()
  const [draft, setDraft] = useState<Record<string, unknown>>({})
  return {
    settings: q.data,
    isLoading: q.isLoading,
    error: q.error,
    get: (key) => (key in draft ? draft[key] : q.data?.get(key)?.value),
    set: (key, v) => setDraft((d) => ({ ...d, [key]: v })),
    dirty: Object.keys(draft).length > 0,
    changed: (key) => key in draft,
    discard: () => setDraft({}),
    save: async (kinds) => {
      if (await attempt(() => saveSettings(Object.fromEntries(Object.entries(draft).map(([k, v]) => [k, kinds?.has(k) ? coerce(kinds.get(k)!, v, k) : v]))), 'Settings saved')) setDraft({})
    },
    reset: async (key) => {
      if (await attempt(() => saveSettings({ [key]: null }), `${key} reset to default`)) setDraft(({ [key]: _, ...rest }) => rest)
    },
  }
}

/** Converts number fields typed as text; throws on invalid input. */
export function coerce(kind: Kind, v: unknown, key: string): unknown {
  if (kind === 'lines') return typeof v === 'string' ? v.split('\n').map((l) => l.trim()).filter(Boolean) : v
  if (kind === 'json') {
    if (typeof v !== 'string') return v
    try {
      return JSON.parse(v)
    } catch {
      throw new Error(`${key} must be valid JSON`)
    }
  }
  if (kind !== 'int' && kind !== 'float') return v
  const n = typeof v === 'number' ? v : Number(String(v).trim())
  if (String(v).trim() === '' || !Number.isFinite(n) || (kind === 'int' && !Number.isInteger(n))) throw new Error(`${key} must be a ${kind === 'int' ? 'whole number' : 'number'}`)
  return n
}

export function SettingsForm({ sections, children, single }: { sections: { title: string; hint?: string; fields: FieldSpec[]; footer?: ReactNode }[]; children?: (d: Draft) => ReactNode; single?: boolean }) {
  const d = useDraft()
  const kinds = new Map(sections.flatMap((s) => s.fields.map((f) => [f.key, f.kind] as const)))
  if (d.isLoading || d.error) return <Loading q={d} />
  return (
    <div className="space-y-4">
      {children?.(d)}
      {/* Two columns of cards on wide screens; a card holding a text area spans both. */}
      <div className={cx('grid items-start gap-4', !single && 'xl:grid-cols-2')}>
        {sections.map((s) => (
          <Card key={s.title} title={s.title} hint={s.hint} className={cx(s.fields.some((f) => f.kind === 'textarea' || f.kind === 'json') && 'xl:col-span-2')}>
            <div className="divide-y divide-line">
              {s.fields.map((f) => (
                <SettingField key={f.key} spec={f} d={d} />
              ))}
              {s.footer}
            </div>
          </Card>
        ))}
      </div>
      <SaveBar d={d} onSave={() => d.save(kinds)} />
    </div>
  )
}

export function SaveBar({ d, onSave }: { d: Pick<Draft, 'dirty' | 'discard' | 'save'>; onSave?: () => Promise<void> }) {
  if (!d.dirty) return null
  return (
    <div className="sticky bottom-0 z-10 -mx-1 flex items-center justify-end gap-2 rounded-xl border border-line bg-surface/95 p-3 shadow-pop backdrop-blur" data-testid="save-bar">
      <span className="flex-1 text-sm text-muted">Unsaved changes</span>
      <Btn onClick={d.discard}>Discard</Btn>
      <Btn variant="primary" onClick={() => void (onSave ? onSave() : d.save())} data-testid="save-settings">
        Save
      </Btn>
    </div>
  )
}

/** Label column with the managed badge, warning and reset link. */
export function SettingRow({ k, label, hint, d, children, wide }: { k: string; label?: string; hint?: string; d: Draft; children: ReactNode; wide?: boolean }) {
  const s = d.settings?.get(k)
  const changed = d.changed(k)
  return (
    <div
      className={cx(
        'group relative gap-x-6 gap-y-2 px-5 py-4 transition-colors hover:bg-surface-2/40',
        wide ? 'space-y-3' : 'flex flex-wrap items-center justify-between sm:flex-nowrap',
        changed && 'before:absolute before:inset-y-2 before:left-0 before:w-0.5 before:rounded-full before:bg-accent',
      )}
      data-testid={`setting-${k}`}
    >
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          {/* The raw key stays available as a tooltip for admins who edit the config file. */}
          <span className="text-sm font-medium" title={k}>
            {label ?? humanize(k.split('.').slice(-1)[0])}
          </span>
          {s?.managed && <ManagedBadge compact />}
          {s?.set && !s.secret && (
            <button
              type="button"
              onClick={() => void d.reset(k)}
              title="Reset to the default"
              className="inline-flex items-center gap-1 rounded px-1 text-xs text-muted opacity-0 transition-opacity hover:text-fg focus-visible:opacity-100 group-hover:opacity-100"
            >
              <RotateCcw size={11} /> Reset
            </button>
          )}
        </div>
        {hint && <p className="mt-0.5 text-[13px] leading-snug text-muted">{hint}</p>}
        {s?.managed && changed && <p className="mt-1 text-xs text-amber-700 dark:text-amber-400">{managedWarning}</p>}
      </div>
      <div className={cx(wide ? 'w-full' : 'w-full sm:w-80 sm:shrink-0', !wide && 'sm:flex sm:justify-end')}>{children}</div>
    </div>
  )
}

function SettingField({ spec, d }: { spec: FieldSpec; d: Draft }) {
  const { key, kind } = spec
  const v = d.get(key)
  const s = d.settings?.get(key)
  const wide = kind === 'textarea' || kind === 'json' || kind === 'lines'
  let control: ReactNode
  if (kind === 'bool') control = <Switch checked={!!v} onChange={(x) => d.set(key, x)} label={spec.label ?? key} />
  else if (kind === 'textarea')
    control = <textarea value={String(v ?? '')} onChange={(e) => d.set(key, e.target.value)} rows={6} aria-label={spec.label ?? key} className={cx(inputCls, 'font-mono text-xs')} />
  else if (kind === 'json')
    control = (
      <textarea
        value={typeof v === 'string' ? v : JSON.stringify(v ?? null, null, 2)}
        onChange={(e) => d.set(key, e.target.value)}
        rows={10}
        spellCheck={false}
        aria-label={spec.label ?? key}
        className={cx(inputCls, 'font-mono text-xs')}
      />
    )
  else if (kind === 'lines')
    control = (
      <textarea
        value={typeof v === 'string' ? v : Array.isArray(v) ? v.join('\n') : ''}
        onChange={(e) => d.set(key, e.target.value)}
        rows={Math.min(8, Math.max(3, (Array.isArray(v) ? v.length : String(v ?? '').split('\n').length) + 1))}
        spellCheck={false}
        placeholder="One per line"
        aria-label={spec.label ?? key}
        className={cx(inputCls, 'font-mono text-xs')}
      />
    )
  else if (kind === 'secret')
    control = (
      <div className="flex gap-2">
        <input
          type="password"
          autoComplete="new-password"
          aria-label={spec.label ?? key}
          value={d.changed(key) ? String(v ?? '') : ''}
          placeholder={d.changed(key) && v === '' ? 'Will be removed on save' : s?.set ? '•••••••• stored (type to replace)' : 'Not set'}
          onChange={(e) => d.set(key, e.target.value)}
          className={inputCls}
        />
        {s?.set && (
          <Btn variant="ghost" title="Remove the stored value on save" onClick={() => d.set(key, '')}>
            Clear
          </Btn>
        )}
      </div>
    )
  else if (kind === 'model') control = <ModelSelect value={String(v ?? '')} onChange={(x) => d.set(key, x)} label={spec.label ?? key} />
  else if (kind === 'connection') control = <ConnectionSelect value={String(v ?? '')} onChange={(x) => d.set(key, x)} label={spec.label ?? key} />
  else if (typeof kind === 'object')
    control = (
      <Select value={String(v ?? '')} onChange={(x) => d.set(key, x)} aria-label={spec.label ?? key} options={kind.select.map(([value, label]) => ({ value, label }))} />
    )
  else
    control = (
      <input
        type={kind === 'text' ? 'text' : 'number'}
        step={kind === 'float' ? 'any' : 1}
        value={v == null ? '' : String(v)}
        onChange={(e) => d.set(key, e.target.value)}
        aria-label={spec.label ?? key}
        className={inputCls}
      />
    )
  return (
    <SettingRow k={key} label={spec.label} hint={spec.hint} d={d} wide={wide}>
      {control}
    </SettingRow>
  )
}

export function ModelSelect({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  const models = useModels().data ?? []
  const known = models.some((m) => m.id === value)
  return (
    <Select
      value={value}
      onChange={onChange}
      aria-label={label}
      options={[
        { value: '', label: '(none)' },
        ...(!known && value ? [{ value, label: value }] : []),
        ...models.map((m) => ({ value: m.id, label: (m.name || m.id) + (m.hidden ? ' (hidden)' : ''), icon: <ModelIcon m={m} size="sm" /> })),
      ]}
    />
  )
}

function ConnectionSelect({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  const conns = useConnections().data?.connections ?? []
  return (
    <Select
      value={value}
      onChange={onChange}
      aria-label={label}
      options={[
        { value: '', label: '(none)' },
        ...(!conns.some((c) => c.id === value) && value ? [{ value, label: value }] : []),
        ...conns.map((c) => ({ value: c.id, label: `${c.id} (${c.kind})` })),
      ]}
    />
  )
}
