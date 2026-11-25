// What opens under a step row: its description, error, counts and a log you can tail. Used by the step waterfall and by the collection rows.
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { ArrowDownToLine, CircleX, Copy, Maximize2, WrapText } from 'lucide-react'
import { copyText, cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { relative } from './settings/Integrations'
import { atBottom, countLabel, duration, stageAbout, stageTitle, useStep, type Stage, type Step } from './pipelines'
import { StatusChip } from './status'

/** What a step panel needs to know about its run. */
export interface StepCtx {
  pipeline: string
  runId: number
  stages: Stage[]
  now: number
  /** the edit page's stages section: where "Add" a description leads */
  editHref: string
  /** what each step name usually takes, in ms */
  usual?: Record<string, number>
}

/** Lines shown at once; "Show earlier" lifts the cap. A log is at most 256 KiB, but thousands of rows are slow to lay out. */
const tailLines = 1500
const time = (s: string) => new Date(s).toLocaleTimeString(undefined, { timeStyle: 'medium' })

const level = (t: string): 'bad' | 'warn' | '' => (/\b(error|errors|fatal|exception|traceback|panic|failed)\b/i.test(t) ? 'bad' : /\bwarn(ing)?\b/i.test(t) ? 'warn' : '')

function Line({ n, text }: { n: number; text: string }) {
  const m = /^(\d\d:\d\d:\d\d) ([\s\S]*)$/.exec(text)
  const l = level(text)
  return (
    <div className="flex gap-3 px-2.5 hover:bg-white/5">
      <span aria-hidden className="w-9 shrink-0 select-none text-right tabular-nums text-gray-500">
        {n}
      </span>
      <span className={cx('min-w-0 flex-1', l === 'bad' && 'text-red-300', l === 'warn' && 'text-amber-300')}>
        {m ? (
          <>
            <span className="text-gray-400">{m[1]}</span> {m[2]}
          </>
        ) : (
          text || ' '
        )}
      </span>
    </div>
  )
}

const tool = 'inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-medium text-gray-300 outline-none hover:bg-white/10 hover:text-white focus-visible:ring-2 focus-visible:ring-accent-bright aria-pressed:bg-white/15 aria-pressed:text-white disabled:opacity-40'

/**
 * The log of one step in a terminal-like panel (dark in both themes): line numbers, times dimmed, errors and warnings tinted,
 * Wrap and Follow toggles, Copy and a large view. Following a running step polls the step route like the page does (every 3 s).
 */
export function LogPanel({ pipeline, runId, step, title, tall, inDialog }: { pipeline: string; runId: number; step: Step; title: string; tall?: boolean; inDialog?: boolean }) {
  const q = useStep(pipeline, runId, step.seq, true)
  const cur = q.data ?? step
  const log = cur.log
  const live = cur.status === 'running' || cur.status === 'pending'
  const box = useRef<HTMLDivElement>(null)
  const landed = useRef(false)
  const [wrap, setWrap] = useState(true)
  const [follow, setFollow] = useState(live)
  const [all, setAll] = useState(false)
  const [big, setBig] = useState(false)
  const lines = useMemo(() => (log ? log.replace(/\n$/, '').split('\n') : []), [log])
  const shown = all ? lines : lines.slice(-tailLines)
  const first = lines.length - shown.length

  // follow the end while following; a log that has just arrived shows its end (the decisive lines come last)
  useEffect(() => {
    const el = box.current
    if (!el || log === undefined) return
    if (follow || !landed.current) el.scrollTop = el.scrollHeight
    landed.current = true
  }, [log, follow, wrap, all])

  return (
    <div data-testid="step-log-panel">
      <div className="overflow-hidden rounded-lg border border-line bg-gray-950 text-gray-100">
        <div className="flex flex-wrap items-center gap-x-1 gap-y-0.5 border-b border-white/10 bg-gray-900 px-2.5 py-1">
          <span className="text-[10.5px] font-bold uppercase tracking-[0.08em] text-gray-400">Log</span>
          {lines.length > 0 && <span className="text-[11px] tabular-nums text-gray-500">{lines.length.toLocaleString()} lines</span>}
          {cur.log_dropped && <span className="ml-1 rounded bg-amber-400/15 px-1.5 py-0.5 text-[10px] font-bold uppercase tracking-[0.08em] text-amber-300">Earlier lines dropped</span>}
          <span className="ml-auto flex items-center gap-0.5">
            <button type="button" className={tool} aria-pressed={wrap} onClick={() => setWrap(!wrap)}>
              <WrapText size={12} aria-hidden /> Wrap
            </button>
            <button
              type="button"
              className={tool}
              aria-pressed={follow}
              title={live ? 'Keep the newest line in view' : 'Jump to the end'}
              onClick={() => {
                setFollow(true)
                const el = box.current
                if (el) el.scrollTop = el.scrollHeight
              }}
            >
              <ArrowDownToLine size={12} aria-hidden /> Follow
            </button>
            <button type="button" className={tool} disabled={!log} aria-label="Copy log" onClick={async () => (await copyText(log ?? '')) && toast('Copied')}>
              <Copy size={12} aria-hidden /> Copy
            </button>
            {!inDialog && (
              <button type="button" className={tool} aria-label="Open the log in a large view" onClick={() => setBig(true)}>
                <Maximize2 size={12} aria-hidden /> Large
              </button>
            )}
          </span>
        </div>
        {log ? (
          <div
            ref={box}
            tabIndex={0}
            role="log"
            aria-live="off"
            aria-label={`Log of ${title}`}
            onScroll={(e) => live && setFollow(atBottom(e.currentTarget))}
            className={cx('overflow-auto py-1.5 font-mono text-[12px] leading-5 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-bright', tall ? 'max-h-[70dvh]' : 'max-h-80')}
            data-testid="step-log"
          >
            {first > 0 && (
              <button type="button" onClick={() => setAll(true)} className="mb-1 block w-full px-2.5 py-1 text-left text-[11px] text-accent-bright hover:underline">
                Show {first.toLocaleString()} earlier lines
              </button>
            )}
            <div className={wrap ? 'whitespace-pre-wrap break-words' : 'w-max min-w-full whitespace-pre'}>
              {shown.map((t, i) => (
                <Line key={first + i} n={first + i + 1} text={t} />
              ))}
              {live && <span aria-hidden className="ml-[3.75rem] inline-block h-3.5 w-1.5 translate-y-0.5 bg-gray-400 motion-safe:animate-caret" />}
            </div>
          </div>
        ) : (
          // the run payload carries no log: undefined means "not loaded yet", never "empty"
          <p className="px-3 py-3 text-xs text-gray-400" role={log === undefined && q.error ? 'alert' : undefined}>
            {log === undefined ? (q.error ? 'Could not load the log.' : 'Loading log…') : live ? 'No log lines yet.' : 'This step wrote no log.'}
            {live && log !== undefined && <span aria-hidden className="ml-1 inline-block h-3 w-1.5 translate-y-0.5 bg-gray-400 motion-safe:animate-caret" />}
          </p>
        )}
      </div>
      <p className="mt-1 text-[11px] text-muted">Showing the last 256 KiB. Times are UTC.{cur.log_dropped && ' Earlier lines were dropped.'}</p>
      {big && (
        <Dialog open onClose={() => setBig(false)} label={`Log of ${title}`} testId="step-log-dialog" className="w-[min(72rem,calc(100vw-2rem))]">
          <DialogHeader title={title} onClose={() => setBig(false)} />
          <div className="p-3">
            <LogPanel pipeline={pipeline} runId={runId} step={step} title={title} tall inDialog />
          </div>
        </Dialog>
      )}
    </div>
  )
}

const facts = 'text-[10.5px] font-bold uppercase tracking-[0.08em] text-muted'

/** The panel under an open step row. `loadFailed`-style states are the log panel's own. */
export function StepDetail({ step, ctx }: { step: Step; ctx: StepCtx }) {
  const about = stageAbout(step, ctx.stages)
  const counts = Object.entries(step.counts ?? {})
  const usual = ctx.usual?.[step.name]
  const took = duration(step.started_at, step.finished_at, ctx.now)
  return (
    <div className="space-y-3 text-sm" data-testid="step-body" data-status={step.status}>
      <div>
        {about ? (
          <p className="max-w-3xl text-[13px] leading-relaxed text-fg/80" data-testid="step-about">
            {about}
          </p>
        ) : (
          <p className="text-[13px] text-muted" data-testid="step-no-about">
            <span className="italic">No description yet.</span>{' '}
            <Link to={ctx.editHref} className="font-medium text-accent hover:underline">
              Add one
            </Link>
          </p>
        )}
        <dl className="mt-2 flex flex-wrap items-center gap-x-5 gap-y-1.5">
          <div className="flex items-center gap-2">
            <dt className="sr-only">Status</dt>
            <dd>
              <StatusChip status={step.status} />
            </dd>
          </div>
          <div>
            <dt className={facts}>Started</dt>
            <dd className="tabular-nums" title={step.started_at ?? undefined}>
              {step.started_at ? `${time(step.started_at)} · ${relative(step.started_at)}` : 'Not started'}
            </dd>
          </div>
          <div>
            <dt className={facts}>Took</dt>
            <dd className="tabular-nums">{took || '–'}</dd>
          </div>
          {usual !== undefined && step.status !== 'running' && (
            <div>
              <dt className={facts}>Usually</dt>
              <dd className="tabular-nums">{duration('1970-01-01T00:00:00Z', new Date(usual).toISOString())}</dd>
            </div>
          )}
          {counts.map(([k, v]) => (
            <div key={k} data-testid="step-count">
              <dt className={facts}>{countLabel(k)}</dt>
              <dd className="tabular-nums">{v.toLocaleString()}</dd>
            </div>
          ))}
        </dl>
      </div>
      {step.error && (
        <div className="rounded-lg border border-bad/30 bg-bad-soft p-3" data-testid="step-error">
          <p className="flex items-center gap-1.5 text-[10.5px] font-bold uppercase tracking-[0.08em] text-bad-ink">
            <CircleX size={12} strokeWidth={2.75} aria-hidden /> Error
          </p>
          <p className="mt-1 whitespace-pre-wrap break-words text-[13px] leading-relaxed">{step.error}</p>
        </div>
      )}
      <LogPanel pipeline={ctx.pipeline} runId={ctx.runId} step={step} title={stageTitle(step, ctx.stages)} />
    </div>
  )
}

