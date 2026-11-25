// Admin > Audit: who did what, when, to what. A ledger of plain sentences (audit.ts builds them), newest first.
import { useEffect, useId, useState } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router'
import { ChevronDown, CircleDot, Database, Download, History, KeyRound, LogIn, ScrollText, Search, ShieldX, SlidersHorizontal, TriangleAlert, Users, Workflow, X, type LucideIcon } from 'lucide-react'
import { api, apiFetch } from '@/api/client'
import { cx } from '@/lib/format'
import { Select } from '@/ui/Select'
import { Spinner } from '@/ui/misc'
import { useSettings, useSaveSettings } from './api'
import {
  actorName,
  auditQuery,
  categories,
  categoryOf,
  describe,
  exactTime,
  metaRows,
  outcomeOptions,
  targetName,
  when,
  type AuditEvent,
  type Category,
  type Filters,
} from './audit'
import { useDebounced } from './Users'
import { attempt, Badge, Btn, Card, inputAuto, inputCls, Loading, Page } from './ui'

const icons: Record<Category, LucideIcon> = { signin: LogIn, users: Users, secrets: KeyRound, pipelines: Workflow, settings: SlidersHorizontal, data: Database, other: CircleDot }
const catLabel = (c: Category) => categories.find((x) => x.key === c)?.label ?? 'Other'
const kindBadge: Record<string, string> = { api_key: 'API key', service: 'Integration', system: 'Command line' }

/** Colour belongs to the outcome alone: a refused event is red, a failed one amber, and each also says so in words. */
function OutcomeChip({ outcome }: { outcome: AuditEvent['outcome'] }) {
  if (outcome === 'ok') return null
  const denied = outcome === 'denied'
  const Icon = denied ? ShieldX : TriangleAlert
  return (
    <span
      data-outcome={outcome}
      className={cx(
        'inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[10.5px] font-bold uppercase leading-none tracking-[0.08em] ring-1 ring-inset ring-current/15',
        denied ? 'bg-bad-soft text-bad-ink' : 'bg-warn-soft text-warn-ink',
      )}
    >
      <Icon size={11} strokeWidth={2.5} aria-hidden /> {denied ? 'Refused' : 'Failed'}
    </span>
  )
}

function CategoryIcon({ cat }: { cat: Category }) {
  const Icon = icons[cat]
  return (
    <span title={catLabel(cat)} className="grid size-8 shrink-0 place-items-center rounded-lg bg-surface-2 text-accent-ink ring-1 ring-inset ring-line">
      <Icon size={15} aria-hidden />
      <span className="sr-only">{catLabel(cat)}: </span>
    </span>
  )
}

/** One event: the sentence, then when and how it ended; the button opens the details. */
export function AuditRow({ e, open, onToggle, onActor }: { e: AuditEvent; open: boolean; onToggle: () => void; onActor?: (e: AuditEvent) => void }) {
  const detailId = useId()
  const bad = e.outcome !== 'ok'
  return (
    <li className={cx('relative', bad && (e.outcome === 'denied' ? 'bg-bad-soft/35' : 'bg-warn-soft/35'))} data-testid="audit-row">
      {bad && <span aria-hidden className={cx('absolute inset-y-0 left-0 w-1', e.outcome === 'denied' ? 'bg-bad' : 'bg-warn')} />}
      <button
        type="button"
        aria-expanded={open}
        aria-controls={detailId}
        onClick={onToggle}
        className="flex w-full items-start gap-3 px-4 py-3 text-left transition-colors hover:bg-surface-2/60 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent sm:px-5"
      >
        <CategoryIcon cat={categoryOf(e.action)} />
        <span className="min-w-0 flex-1">
          <span className="block text-sm leading-relaxed text-fg [overflow-wrap:anywhere]">
            {describe(e).map((p, i) =>
              p.strong ? (
                <strong key={i} className="font-semibold">
                  {p.text}
                </strong>
              ) : (
                <span key={i}>{p.text}</span>
              ),
            )}
          </span>
          <span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
            <time dateTime={e.at} title={exactTime(e.at)} className="tabular-nums">
              {when(e.at)}
            </time>
            <OutcomeChip outcome={e.outcome} />
            {kindBadge[e.actor_kind] && <Badge>{kindBadge[e.actor_kind]}</Badge>}
          </span>
        </span>
        <ChevronDown size={16} aria-hidden className={cx('mt-1.5 shrink-0 text-muted transition-transform motion-reduce:transition-none', open && 'rotate-180')} />
      </button>
      {open && <AuditDetails id={detailId} e={e} onActor={onActor} />}
    </li>
  )
}

/** The structured record behind a sentence: names and addresses only, never a value. */
export function AuditDetails({ id, e, onActor }: { id?: string; e: AuditEvent; onActor?: (e: AuditEvent) => void }) {
  const rows: [string, React.ReactNode][] = [
    ['When', exactTime(e.at)],
    ['Action', <code key="a" className="rounded bg-surface-2 px-1 py-0.5 text-xs">{e.action}</code>],
    ['Who', [actorName(e), kindBadge[e.actor_kind] ?? (e.actor_kind === 'anonymous' ? 'not signed in' : ''), e.actor_id].filter(Boolean).join(' · ')],
  ]
  if (targetName(e)) rows.push(['Target', [e.target_type.replace(/_/g, ' '), targetName(e), e.target_label && e.target_id !== e.target_label ? e.target_id : ''].filter(Boolean).join(' · ')])
  rows.push(['Outcome', e.outcome === 'ok' ? 'Succeeded' : e.outcome === 'denied' ? 'Refused' : 'Failed'])
  if (e.ip) rows.push(['IP address', e.ip])
  if (e.user_agent) rows.push(['Browser or client', e.user_agent])
  for (const [k, v] of metaRows(e.meta)) rows.push([k[0].toUpperCase() + k.slice(1), v])
  return (
    <div id={id} className="border-t border-line/70 bg-bg-soft/60 px-4 py-3 sm:pl-16 sm:pr-5">
      <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-xs font-medium text-muted sm:pt-0.5">{k}</dt>
            <dd className="mb-1 min-w-0 [overflow-wrap:anywhere] sm:mb-0">{v}</dd>
          </div>
        ))}
      </dl>
      {onActor && e.actor_id && (
        <Btn variant="ghost" className="mt-2 -ml-3" onClick={() => onActor(e)}>
          <History size={14} aria-hidden /> Everything by {actorName(e)}
        </Btn>
      )}
    </div>
  )
}

/** A text filter typed into freely; the URL follows after a pause. */
function TextFilter({ value, onChange, label, placeholder, icon }: { value: string; onChange: (v: string) => void; label: string; placeholder: string; icon?: boolean }) {
  const [draft, setDraft] = useState(value)
  const settled = useDebounced(draft, 350)
  useEffect(() => {
    if (settled !== value) onChange(settled) // only a settled draft moves the URL
  }, [settled])
  return (
    <label className="relative block min-w-0">
      <span className="sr-only">{label}</span>
      {icon && <Search size={15} aria-hidden className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />}
      <input type="search" value={draft} onChange={(ev) => setDraft(ev.target.value)} placeholder={placeholder} className={cx(inputCls, icon && 'pl-8')} />
    </label>
  )
}

const today = () => new Date().toISOString().slice(0, 10)

/** Saves text as a file through a temporary link. */
function saveFile(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  Object.assign(document.createElement('a'), { href: url, download: name }).click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function AuditPage() {
  const [params, setParams] = useSearchParams()
  const f: Filters = { actor: params.get('actor') ?? '', category: params.get('cat') ?? '', outcome: params.get('outcome') ?? '', from: params.get('from') ?? '', to: params.get('to') ?? '', q: params.get('q') ?? '' }
  const actorChip = params.get('name') // set by the Users page's Activity link: actor holds an id
  const set = (patch: Record<string, string>) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        for (const [k, v] of Object.entries(patch)) {
          if (v) next.set(k, v)
          else next.delete(k)
        }
        return next
      },
      { replace: true },
    )
  const [reset, setReset] = useState(0) // remounts the text filters when everything is cleared
  const [openId, setOpenId] = useState<string | null>(null)
  const qs = auditQuery(f).toString()
  const events = useInfiniteQuery({
    queryKey: ['admin', 'audit', qs],
    initialPageParam: '',
    queryFn: ({ pageParam }) => api<{ events: AuditEvent[]; next: string }>(`/api/v1/admin/audit?${qs}${pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ''}`),
    getNextPageParam: (last) => last.next || undefined,
  })
  const list = events.data?.pages.flatMap((p) => p.events) ?? []
  const filtered = Object.values(f).some(Boolean)
  const [exporting, setExporting] = useState(false)
  const exportCsv = async () => {
    setExporting(true)
    await attempt(async () => saveFile(`kera-audit-${today()}.csv`, await (await apiFetch(`/api/v1/admin/audit/export?${qs}`)).text()))
    setExporting(false)
  }

  return (
    <Page
      title="Audit log"
      actions={
        <Btn onClick={() => void exportCsv()} disabled={exporting} data-testid="audit-export">
          {exporting ? <Spinner /> : <Download size={15} aria-hidden />} Export CSV
        </Btn>
      }
    >
      <p className="-mt-3 max-w-3xl text-sm text-muted">
        Who did what, and when: sign-ins, admin changes, stored secrets, pipelines and sharing. Names of what changed are kept; secret values, passwords and message content never are.
      </p>

      <div role="search" aria-label="Filter the audit log" className="grid grid-cols-2 gap-2 md:grid-cols-[minmax(12rem,1.2fr)_minmax(9rem,1fr)_minmax(9rem,1fr)_auto] xl:grid-cols-[minmax(12rem,1.1fr)_minmax(9rem,0.9fr)_minmax(9rem,0.9fr)_auto_minmax(12rem,1.4fr)]" key={reset}>
        <div className="col-span-2 md:col-span-1">
          {actorChip ? (
            <span className="flex h-full min-h-8 items-center gap-1.5 rounded-lg border border-accent/40 bg-accent-soft py-1 pl-2.5 pr-1 text-sm text-accent-ink">
              <History size={14} aria-hidden className="shrink-0" />
              <span className="min-w-0 flex-1 truncate">
                Activity of <b className="font-semibold">{actorChip}</b>
              </span>
              <button type="button" aria-label={`Stop showing only ${actorChip}`} onClick={() => set({ actor: '', name: '' })} className="grid size-6 shrink-0 place-items-center rounded-md hover:bg-surface/60 focus-visible:outline-2 focus-visible:outline-accent">
                <X size={14} aria-hidden />
              </button>
            </span>
          ) : (
            <TextFilter value={f.actor} onChange={(v) => set({ actor: v })} label="Who: email or name" placeholder="Who: email or name" />
          )}
        </div>
        <Select value={f.category} onChange={(v) => set({ cat: v })} aria-label="What" options={[{ value: '', label: 'Everything' }, ...categories.map((c) => ({ value: c.key, label: c.label }))]} />
        <Select value={f.outcome} onChange={(v) => set({ outcome: v })} aria-label="Outcome" options={outcomeOptions} />
        <div className="col-span-2 flex min-w-0 items-center gap-1.5 md:col-span-1">
          <input type="date" aria-label="From day" value={f.from} max={f.to || undefined} onChange={(ev) => set({ from: ev.target.value })} className={cx(inputAuto, 'min-w-0 flex-1')} />
          <span className="text-xs text-muted" aria-hidden>
            to
          </span>
          <input type="date" aria-label="To day" value={f.to} min={f.from || undefined} onChange={(ev) => set({ to: ev.target.value })} className={cx(inputAuto, 'min-w-0 flex-1')} />
        </div>
        <div className="col-span-2 md:col-span-3 xl:col-span-1">
          <TextFilter value={f.q} onChange={(v) => set({ q: v })} label="Search the audit log" placeholder="Search" icon />
        </div>
      </div>

      <Card
        title={<span className="flex items-center gap-2">Events {events.data && <span className="text-xs font-normal text-muted">{list.length ? `${list.length}${events.hasNextPage ? '+' : ''} shown, newest first` : ''}</span>}</span>}
        actions={
          filtered && (
            <Btn variant="ghost" onClick={() => (setParams({}, { replace: true }), setReset((n) => n + 1))}>
              <X size={14} aria-hidden /> Clear filters
            </Btn>
          )
        }
      >
        {events.isLoading || events.error ? (
          <div className="p-4">
            <Loading q={events} />
          </div>
        ) : list.length ? (
          <ol aria-label="Audit events" className="divide-y divide-line/70">
            {list.map((e) => (
              <AuditRow
                key={e.id}
                e={e}
                open={openId === e.id}
                onToggle={() => setOpenId(openId === e.id ? null : e.id)}
                onActor={(x) => (set({ actor: x.actor_id, name: x.actor_label || x.actor_id }), setReset((n) => n + 1))}
              />
            ))}
          </ol>
        ) : (
          <AuditEmpty filtered={filtered} />
        )}
        {events.hasNextPage && (
          <div className="flex justify-center border-t border-line/70 p-3">
            <Btn onClick={() => void events.fetchNextPage()} disabled={events.isFetchingNextPage}>
              {events.isFetchingNextPage && <Spinner />} Load more
            </Btn>
          </div>
        )}
      </Card>
      <p className="sr-only" aria-live="polite">
        {events.data ? `${list.length} events shown` : ''}
      </p>
      <Retention />
    </Page>
  )
}

export function AuditEmpty({ filtered }: { filtered: boolean }) {
  return (
    <div className="mx-auto max-w-md space-y-2 px-6 py-12 text-center" data-testid="audit-empty">
      <ScrollText size={30} aria-hidden className="mx-auto text-muted" />
      <h4 className="text-sm font-semibold">{filtered ? 'No events match these filters' : 'Nothing recorded yet'}</h4>
      <p className="text-sm text-muted">{filtered ? 'Widen the dates or clear a filter.' : 'Sign-ins and changes made in the admin console appear here as they happen.'}</p>
    </div>
  )
}

/** audit.retention_days, edited in place. */
function Retention() {
  const settings = useSettings()
  const save = useSaveSettings()
  const stored = settings.data?.get('audit.retention_days')
  const current = Number(stored?.value ?? stored?.default ?? 365)
  const [draft, setDraft] = useState<string | null>(null)
  const id = useId()
  if (!stored) return null
  const value = draft ?? String(current)
  const n = Number(value)
  const valid = value.trim() !== '' && Number.isInteger(n) && n >= 0
  return (
    <form
      className="flex flex-wrap items-center gap-2 text-sm text-muted"
      onSubmit={async (ev) => {
        ev.preventDefault()
        if (valid && (await attempt(() => save({ 'audit.retention_days': n }), 'Retention saved'))) setDraft(null)
      }}
    >
      <label htmlFor={id}>Keep events for</label>
      <input id={id} type="number" min={0} step={1} inputMode="numeric" value={value} onChange={(ev) => setDraft(ev.target.value)} aria-invalid={!valid} className={cx(inputCls.replace('w-full ', 'w-24 '), 'tabular-nums')} />
      <span>days, then delete them ({current === 0 ? 'now kept forever' : '0 keeps them forever'}).</span>
      {draft !== null && draft !== String(current) && (
        <Btn type="submit" variant="primary" disabled={!valid}>
          Save
        </Btn>
      )}
    </form>
  )
}
