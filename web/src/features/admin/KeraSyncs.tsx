// The Kera half of a run: one compact row per collection kept in sync, with a segmented bar of what happened to its files.
import { ChevronRight, Library } from 'lucide-react'
import { cx } from '@/lib/format'
import { changeSummary, duration, type Counts, type Step } from './pipelines'
import { syncQuiet, syncSegments } from './runview'
import { StatusChip } from './status'
import { spine, StepDot } from './StepWaterfall'
import { StepDetail, type StepCtx } from './StepLog'

const segTone = { idle: 'bg-muted/30', ok: 'bg-ok', accent: 'bg-accent', warn: 'bg-warn', bad: 'bg-bad' }
const dotTone = { idle: 'bg-muted/45', ok: 'bg-ok', accent: 'bg-accent', warn: 'bg-warn', bad: 'bg-bad' }

const cols = 'grid grid-cols-[2rem_minmax(0,1fr)_auto] @xl:grid-cols-[2rem_minmax(0,2fr)_minmax(0,3fr)_7.5rem]'

/** The bar and its legend. A collection where nothing moved is one quiet line instead. */
export function SyncBar({ counts }: { counts: Counts }) {
  const segs = syncSegments(counts)
  const quiet = syncQuiet(counts)
  const listed = counts.listed ?? 0
  if (quiet)
    return (
      <span className="block">
        <span aria-hidden className="block h-1.5 rounded-full bg-line/80" />
        <span className="mt-1 block text-[11px] tabular-nums text-muted">{listed ? `${listed.toLocaleString()} files, all unchanged` : 'Nothing listed'}</span>
      </span>
    )
  return (
    <span className="block">
      <span role="img" aria-label={segs.map((s) => `${s.n} ${s.label}`).join(', ')} className="flex h-1.5 gap-px overflow-hidden rounded-full bg-line/60">
        {segs.map((s) => (
          <span key={s.key} className={cx('block h-full first:rounded-l-full last:rounded-r-full motion-safe:transition-[width] motion-safe:duration-700', segTone[s.tone])} style={{ width: `${s.pct}%`, minWidth: 3 }} />
        ))}
      </span>
      <span className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] tabular-nums text-muted">
        {segs.map((s) => (
          <span key={s.key} className="inline-flex items-center gap-1">
            <span aria-hidden className={cx('size-1.5 rounded-full', dotTone[s.tone])} />
            <b className="font-semibold text-fg/90">{s.n.toLocaleString()}</b> {s.label}
          </span>
        ))}
      </span>
    </span>
  )
}

function Row({ s, i, n, ctx, open, onToggle, flash }: { s: Step; i: number; n: number; ctx: StepCtx; open: boolean; onToggle: () => void; flash: boolean }) {
  const waiting = s.counts?.waiting ?? 0
  const refused = s.counts?.refused ?? 0
  const idle = s.status === 'pending' || s.status === 'skipped'
  return (
    <li id={`step-${s.seq}`} data-testid="step-row" data-status={s.status} data-kind="kera" className={cx('relative scroll-mt-24 motion-safe:animate-rise', flash && 'bg-accent-soft')} style={{ animationDelay: `${Math.min(i, 14) * 28}ms` }}>
      <span aria-hidden className={spine(i, n)} />
      <StepDot status={s.status} />
      <button
        type="button"
        aria-expanded={open}
        aria-controls={`step-panel-${s.seq}`}
        onClick={onToggle}
        className={cx(cols, 'w-full items-start gap-y-1.5 px-3 py-2.5 text-left outline-none transition-colors hover:bg-surface-2/50 focus-visible:bg-surface-2/50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent', idle && 'text-muted')}
      >
        <span aria-hidden />
        <span className="min-w-0">
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span className={cx('min-w-0 truncate text-sm font-semibold', idle ? 'text-muted' : 'text-fg')} title={s.name}>
              {s.name}
            </span>
            {waiting > 0 && (
              <span className="inline-flex items-center gap-1 rounded bg-accent-soft px-1.5 py-px text-[11px] font-medium tabular-nums text-accent-ink">
                <span aria-hidden className="size-1.5 rounded-full bg-current motion-safe:animate-pulse" /> Indexing · {waiting} waiting
              </span>
            )}
            {refused > 0 && <span className="rounded bg-warn-soft px-1.5 py-px text-[11px] font-medium tabular-nums text-warn-ink">{refused} not removed</span>}
          </span>
        </span>
        <span className="col-span-2 col-start-2 row-start-2 block @xl:col-span-1 @xl:col-start-3 @xl:row-start-1">{idle && !s.counts?.listed ? <span className="text-[11px] text-muted">{s.status === 'skipped' ? 'Not synced: an earlier step failed' : 'Waiting for the script to finish'}</span> : <SyncBar counts={s.counts ?? {}} />}</span>
        <span className="col-start-3 row-start-1 flex items-start justify-end gap-1.5 @xl:col-start-4">
          <span className="flex flex-col items-end gap-1">
            <span className="text-sm font-medium tabular-nums text-fg">{s.started_at ? duration(s.started_at, s.finished_at, ctx.now) : '–'}</span>
            <StatusChip status={s.status} bare />
          </span>
          <ChevronRight size={14} aria-hidden className={cx('mt-0.5 shrink-0 text-muted transition-transform motion-reduce:transition-none', open && 'rotate-90')} />
        </span>
      </button>
      {open && (
        <div id={`step-panel-${s.seq}`} role="region" aria-label={`${s.name}: details`} className="pb-4 pl-12 pr-3">
          <StepDetail step={s} ctx={ctx} />
        </div>
      )}
    </li>
  )
}

export function KeraSyncs({ steps, counts, ctx, isOpen, onToggle, flash }: { steps: Step[]; counts: Counts; ctx: StepCtx; isOpen: (s: Step) => boolean; onToggle: (s: Step) => void; flash: number | null }) {
  const sum = changeSummary(counts)
  return (
    <section role="group" aria-labelledby="flow-kera" data-testid="kera-syncs" className="@container overflow-hidden rounded-2xl border border-line bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)]">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-line px-4 py-3">
        <span className="grid size-6 place-items-center rounded-md bg-accent-soft text-accent-ink">
          <Library size={13} aria-hidden />
        </span>
        <h3 id="flow-kera" className="text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">
          Kera
        </h3>
        <span className="text-xs text-muted">
          {steps.length} {steps.length === 1 ? 'collection' : 'collections'} kept in sync
        </span>
        <span className="ml-auto text-xs tabular-nums text-muted" data-testid="kera-summary">
          {(counts.listed ?? 0) > 0 && `${counts.listed.toLocaleString()} files · `}
          {sum === 'No changes' ? 'nothing changed' : sum}
        </span>
      </header>
      <ul className="divide-y divide-line/70">
        {steps.map((s, i) => (
          <Row key={s.seq} s={s} i={i} n={steps.length} ctx={ctx} open={isOpen(s)} onToggle={() => onToggle(s)} flash={flash === s.seq} />
        ))}
      </ul>
    </section>
  )
}
