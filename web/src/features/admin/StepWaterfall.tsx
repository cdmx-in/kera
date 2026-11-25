// The step waterfall: one row per script step on a shared time axis. A row opens in place to its description, error and a tailing log.
import { useState } from 'react'
import { ChevronRight, Terminal } from 'lucide-react'
import { cx } from '@/lib/format'
import { countChips, duration, lacksAbout, stageAbout, stageTitle, type Run, type Step, type StepStatus } from './pipelines'
import { axisTicks, axisLabel, barOf, slowerThanUsual, stepMs } from './runview'
import { StatusChip } from './status'
import { StepDetail, type StepCtx } from './StepLog'

/** Columns of a row: spine, name, time track, duration and chevron. In a section narrower than 36rem (a container query, so a narrow column on a wide screen counts) the track drops to its own line. */
export const rowCols = 'grid grid-cols-[2rem_minmax(0,1fr)_auto] @xl:grid-cols-[2rem_minmax(0,2fr)_minmax(0,3fr)_7.5rem]'

const barLook: Record<StepStatus, string> = {
  pending: '',
  skipped: '',
  ok: 'bg-ok/70 group-hover/row:bg-ok',
  warning: 'bg-warn',
  failed: 'bg-bad',
  // a barber pole while it runs, with a soft glow at the leading edge
  running: 'bg-accent bg-[linear-gradient(45deg,rgb(255_255_255/0.22)_25%,transparent_25%,transparent_50%,rgb(255_255_255/0.22)_50%,rgb(255_255_255/0.22)_75%,transparent_75%)] [background-size:1rem_1rem] shadow-[0_0_12px_-1px_var(--accent)] motion-safe:animate-stripes',
}

/** The node on the spine: a filled dot for what ran, hollow for what has not (dashed when it never will). The status itself is the chip's icon and word. */
export function StepDot({ status }: { status: StepStatus }) {
  return (
    <span
      aria-hidden
      className={cx(
        'absolute left-[1.1rem] top-5 size-[9px] -translate-x-1/2 -translate-y-1/2 rounded-full',
        status === 'ok' && 'bg-ok',
        status === 'warning' && 'bg-warn',
        status === 'failed' && 'bg-bad',
        status === 'running' && 'bg-current text-accent motion-safe:animate-beacon',
        status === 'pending' && 'border border-muted bg-surface',
        status === 'skipped' && 'border border-dashed border-muted bg-surface',
      )}
    />
  )
}

/** The vertical line the dots sit on, broken at the first and last row. */
export const spine = (i: number, n: number) => cx('absolute left-[1.1rem] w-px bg-line', n === 1 ? 'hidden' : i === 0 ? 'top-5 bottom-0' : i === n - 1 ? 'top-0 h-5' : 'inset-y-0')

export function CountChips({ step }: { step: Step }) {
  const tone = { red: 'bg-bad-soft text-bad-ink', amber: 'bg-warn-soft text-warn-ink', green: 'bg-ok-soft text-ok-ink', accent: 'bg-accent-soft text-accent-ink', grey: 'bg-surface-2 text-muted' }
  return (
    <>
      {countChips(step).map((c) => (
        <span key={c.text} className={cx('rounded px-1.5 py-px text-[11px] font-medium tabular-nums', tone[c.tone])}>
          {c.text}
        </span>
      ))}
    </>
  )
}

function Row({ s, i, n, run, ctx, span, open, onToggle, flash, many }: { s: Step; i: number; n: number; run: Run; ctx: StepCtx; span: number; open: boolean; onToggle: () => void; flash: boolean; many: boolean }) {
  const t0 = new Date(run.started_at).getTime()
  const bar = barOf(s, t0, span, ctx.now)
  const title = stageTitle(s, ctx.stages)
  const about = stageAbout(s, ctx.stages)
  const took = stepMs(s, ctx.now)
  const slow = slowerThanUsual(took, ctx.usual?.[s.name])
  const grid = axisTicks(span)
  return (
    <li
      id={`step-${s.seq}`}
      data-testid="step-row"
      data-status={s.status}
      data-kind="script"
      className={cx('group/row relative scroll-mt-24 motion-safe:animate-rise', flash && 'bg-accent-soft', many && '[contain-intrinsic-size:auto_3.6rem] [content-visibility:auto]')}
      style={{ animationDelay: `${Math.min(i, 14) * 28}ms` }}
    >
      <span aria-hidden className={spine(i, n)} />
      <StepDot status={s.status} />
      <button
        type="button"
        aria-expanded={open}
        aria-controls={`step-panel-${s.seq}`}
        onClick={onToggle}
        className={cx(rowCols, 'w-full items-start gap-y-1 px-3 py-2.5 text-left outline-none transition-colors hover:bg-surface-2/50 focus-visible:bg-surface-2/50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent', s.status === 'skipped' || s.status === 'pending' ? 'text-muted' : '')}
      >
        <span aria-hidden />
        <span className="min-w-0">
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span className={cx('min-w-0 truncate text-sm font-semibold', s.status === 'skipped' || s.status === 'pending' ? 'text-muted' : 'text-fg')} title={title}>
              {title}
            </span>
            <CountChips step={s} />
            {slow && (
              <span className="rounded bg-warn-soft px-1.5 py-px text-[11px] font-medium tabular-nums text-warn-ink" title="Longer than this step usually takes">
                usually {duration('1970-01-01T00:00:00Z', new Date(ctx.usual![s.name]).toISOString())}
              </span>
            )}
          </span>
          <span className={cx('mt-0.5 block truncate text-xs', about ? 'text-muted' : 'italic text-muted')} title={about || undefined}>
            {about || (lacksAbout(s, ctx.stages) ? 'No description yet' : '')}
          </span>
        </span>
        <span aria-hidden className="relative col-span-2 col-start-2 row-start-2 block h-5 @xl:col-span-1 @xl:col-start-3 @xl:row-start-1 @xl:-my-2.5 @xl:h-auto @xl:self-stretch" style={{ backgroundImage: 'linear-gradient(to right, var(--color-line) 1px, transparent 1px)', backgroundSize: `${(grid.step / span) * 100}% 100%`, backgroundRepeat: 'repeat-x' }}>
          {bar && (
            <span
              className={cx('absolute top-1/2 h-2.5 -translate-y-1/2 rounded-[3px] motion-safe:transition-[left,width] motion-safe:duration-1000 motion-safe:ease-linear', barLook[s.status])}
              style={{ left: `${bar.left}%`, width: `${bar.width}%` }}
            />
          )}
        </span>
        <span className="col-start-3 row-start-1 flex items-start justify-end gap-1.5 @xl:col-start-4">
          <span className="flex flex-col items-end gap-1">
            <span className="text-sm font-medium tabular-nums text-fg">{s.started_at ? duration(s.started_at, s.finished_at, ctx.now) : '–'}</span>
            <StatusChip status={s.status} bare />
          </span>
          <ChevronRight size={14} aria-hidden className={cx('mt-0.5 shrink-0 text-muted transition-transform motion-reduce:transition-none', open && 'rotate-90')} />
        </span>
      </button>
      {open && (
        <div id={`step-panel-${s.seq}`} role="region" aria-label={`${title}: details`} className="pb-4 pl-12 pr-3">
          <StepDetail step={s} ctx={ctx} />
        </div>
      )}
    </li>
  )
}

/** The time axis above the rows: round marks (0, 2 min, 4 min...) over the track column. */
function Axis({ span }: { span: number }) {
  const { marks } = axisTicks(span)
  return (
    <div className="px-3 pb-1" aria-hidden>
      <div className="ml-8 @xl:ml-0 @xl:grid @xl:grid-cols-[2rem_minmax(0,2fr)_minmax(0,3fr)_7.5rem]">
        <div className="relative h-4 @xl:col-start-3">
          {marks.map((m, i) => (
            // on a narrow track every other label is dropped, so "1 min" and "1 min 30 s" never run into each other
            <span key={m.ms} className={cx('absolute top-0 whitespace-nowrap text-[10px] tabular-nums text-muted', m.pct > 88 ? '-translate-x-full' : m.pct > 0 ? '-translate-x-1/2' : '', i % 2 === 1 && '@max-3xl:hidden')} style={{ left: `${m.pct}%` }}>
              {axisLabel(m.ms)}
            </span>
          ))}
        </div>
      </div>
    </div>
  )
}

/**
 * `steps` are the script's, in order. `span` is the length of the shared axis (see axisSpan).
 * A list of more than 12 steps can be narrowed to the ones that failed or warned.
 */
export function StepWaterfall({ run, steps, ctx, span, isOpen, onToggle, flash }: { run: Run; steps: Step[]; ctx: StepCtx; span: number; isOpen: (s: Step) => boolean; onToggle: (s: Step) => void; flash: number | null }) {
  const [problems, setProblems] = useState(false)
  const bad = steps.filter((s) => s.status === 'failed' || s.status === 'warning')
  const list = problems ? bad : steps
  const ok = steps.filter((s) => s.status === 'ok').length
  return (
    <section data-testid="flow" aria-labelledby="waterfall-title" className="@container overflow-hidden rounded-2xl border border-line bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)]">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 pb-2 pt-3.5">
        <span className="grid size-6 place-items-center rounded-md bg-surface-2 text-muted">
          <Terminal size={13} aria-hidden />
        </span>
        <h3 id="waterfall-title" className="text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">
          Script
        </h3>
        <span className="text-xs text-muted tabular-nums">
          {steps.length} {steps.length === 1 ? 'step' : 'steps'}
          {steps.length > 1 && ok > 0 && ` · ${ok} OK`}
          {bad.length > 0 && ` · ${bad.length} to look at`}
        </span>
        {steps.length > 12 && bad.length > 0 && (
          <button type="button" aria-pressed={problems} onClick={() => setProblems(!problems)} className="ml-auto rounded-md border border-line px-2 py-1 text-xs font-medium text-muted outline-none hover:bg-surface-2 hover:text-fg focus-visible:ring-2 focus-visible:ring-accent aria-pressed:border-accent aria-pressed:bg-accent-soft aria-pressed:text-accent-ink">
            Only failed or warning
          </button>
        )}
      </header>
      <Axis span={span} />
      <ol aria-label="Script steps" className="divide-y divide-line/70 border-t border-line">
        {list.map((s, i) => (
          <Row key={s.seq} s={s} i={i} n={list.length} run={run} ctx={ctx} span={span} open={isOpen(s)} onToggle={() => onToggle(s)} flash={flash === s.seq} many={list.length > 30} />
        ))}
      </ol>
    </section>
  )
}

