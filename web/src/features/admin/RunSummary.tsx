// The head of the run view: one sentence of state, a big stopwatch, a progress rail with one segment per step, and quiet facts.
import { useState } from 'react'
import { Link } from 'react-router'
import { Ban, CalendarClock, Clock, FileCode2, Loader2, MousePointerClick, NotebookPen, Server, Terminal, Upload, type LucideIcon } from 'lucide-react'
import { cx } from '@/lib/format'
import { Btn } from './ui'
import { runnersUrl } from './Pipelines'
import { relative, when } from './settings/Integrations'
import { cancellable, duration, isNotebookRun, scriptLabel, stageTitle, triggerLabel, type Run, type Stage, type Step } from './pipelines'
import { clock, isCancelled, isClosedByAdmin, orderedSteps, runChip, runMs, runSentence, stepsDone, waitedText } from './runview'
import { fillOf, inkOf, StatusChip, statusTone, statusWord } from './status'

/** "root@build-01.example.com: bash /srv/refresh.sh" for the run header; empty for a run that reported nothing. */
export function runWhere(run: Pick<Run, 'source'>): string {
  const s = run.source ?? {}
  const who = [s.user, s.host].filter(Boolean).join('@')
  return [who, s.command].filter(Boolean).join(': ')
}

const triggerIcon: Record<string, LucideIcon> = { schedule: CalendarClock, manual: MousePointerClick, script: Terminal, push: Upload, test: NotebookPen }

function Meta({ icon: Icon, mono, title, testId, children }: { icon?: LucideIcon; mono?: boolean; title?: string; testId?: string; children: React.ReactNode }) {
  return (
    <li title={title} data-testid={testId} className={cx('inline-flex max-w-full items-center gap-1.5 rounded-md border border-line bg-surface-2/50 px-2 py-1 text-[11.5px] text-muted', mono && 'font-mono text-[11px]')}>
      {Icon && <Icon size={12} aria-hidden className="shrink-0" />}
      <span className="truncate">{children}</span>
    </li>
  )
}

/** The failed run's error in a calm block, with a way to the step that failed. A long one is cut to a few lines until asked for. */
function Failure({ run, onJump, failedSeq }: { run: Run; onJump: (seq: number) => void; failedSeq: number | undefined }) {
  const [all, setAll] = useState(false)
  const text = run.error || 'No error text was recorded.'
  const long = text.length > 320 || text.split('\n').length > 5
  return (
    <div className="mt-4 rounded-lg border border-bad/30 bg-bad-soft p-3" data-testid="failed-banner">
      <p className="text-[10.5px] font-bold uppercase tracking-[0.08em] text-bad-ink">What went wrong</p>
      <p className={cx('mt-1 whitespace-pre-wrap break-words text-[13px] leading-relaxed', !all && long && 'line-clamp-4')}>{text}</p>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        {failedSeq !== undefined && (
          <Btn className="px-2.5 py-1 text-xs" onClick={() => onJump(failedSeq)} data-testid="open-step">
            Show the failed step
          </Btn>
        )}
        {long && (
          <button type="button" onClick={() => setAll(!all)} aria-expanded={all} className="rounded text-xs font-medium text-bad-ink underline-offset-2 hover:underline">
            {all ? 'Show less' : 'Show all'}
          </button>
        )}
      </div>
    </div>
  )
}

export function RunSummary({
  run,
  steps = [],
  stages,
  now,
  runner,
  typical = null,
  online,
  onCancel,
  onJump,
}: {
  run: Run
  steps?: Step[]
  stages?: Stage[]
  now: number
  runner?: string
  /** the usual length of a good run, ms */
  typical?: number | null
  /** whether a runner is online (undefined: not known) */
  online?: boolean
  onCancel?: () => void
  onJump?: (seq: number) => void
}) {
  const chip = runChip(run)
  const tone = statusTone(chip)
  const open = run.status === 'running'
  const waiting = open && run.queued
  const took = runMs(run, now)
  const sentence = runSentence(run, steps, stages, now)
  const [head, ...rest] = sentence.split(' · ')
  const segs = orderedSteps(steps)
  const firstKera = segs.findIndex((s) => s.kera)
  const { done, skipped, total } = stepsDone(steps)
  const blame = steps.find((s) => !s.kera && s.status === 'failed') ?? steps.find((s) => s.status === 'failed')
  const where = runWhere(run)
  const src = run.source ?? {}
  const who = [src.user, src.host].filter(Boolean).join('@')
  const Trig = triggerIcon[run.trigger]
  const slow = typical !== null && took > typical * 1.3 && took - typical > 30_000
  const waited = waitedText(run)
  return (
    <section className="@container relative overflow-hidden rounded-2xl border border-line bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)]" data-testid="run-header" data-status={run.status}>
      <span aria-hidden className={cx('absolute inset-x-0 top-0 h-[3px]', fillOf[tone], chip === 'running' && 'bg-[linear-gradient(45deg,rgb(255_255_255/0.28)_25%,transparent_25%,transparent_50%,rgb(255_255_255/0.28)_50%,rgb(255_255_255/0.28)_75%,transparent_75%)] [background-size:1rem_1rem] motion-safe:animate-stripes')} />
      <div className="p-4 pt-5 sm:p-5 sm:pt-6">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">Run #{run.id}</h3>
          <StatusChip status={chip} />
          {run.dry_run && !isNotebookRun(run) && <StatusChip status="dry" />}
          {isNotebookRun(run) && <StatusChip status="notebook" />}
        </div>

        <div className="mt-3 grid gap-x-8 gap-y-3 @xl:grid-cols-[minmax(0,1fr)_auto] @xl:items-end">
          <div className="min-w-0">
            <p aria-live="polite" className="text-lg font-semibold leading-snug tracking-tight text-fg @xl:text-[1.4rem]" data-testid="run-sentence">
              <span className={inkOf[tone]}>{head}</span>
              {rest.map((r, i) => (
                <span key={i} className="font-medium">
                  <span className="mx-1.5 text-muted/60">·</span>
                  {r}
                </span>
              ))}
            </p>
            <p className="mt-1.5 text-xs text-muted" title={when(run.started_at)}>
              {waiting ? 'Queued' : 'Started'} {relative(run.started_at)} <span className="mx-1 text-muted/50">·</span> {when(run.started_at)}
            </p>
          </div>
          <div className="text-left @xl:text-right" data-testid="run-clock">
            <p aria-hidden className="text-4xl font-light leading-none tracking-tight tabular-nums text-fg @xl:text-[3.25rem]">
              {clock(took)}
            </p>
            <p className="sr-only">{duration(run.started_at, run.finished_at, now)}</p>
            <p className="mt-1.5 text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">{open ? (waiting ? 'Waiting' : 'Elapsed') : 'Total time'}</p>
          </div>
        </div>

        {segs.length > 0 && (
          <div className="mt-5">
            <ol aria-label="Progress, one segment per step" className={cx('flex', segs.length > 30 ? 'gap-px' : 'gap-[3px]')} data-testid="progress-rail">
              {segs.map((g, i) => {
                const label = g.kera ? g.name : stageTitle(g, stages)
                const t = statusTone(g.status)
                return (
                  <li key={g.seq} className={cx('min-w-[2px] flex-1', i === firstKera && i > 0 && 'ml-2')}>
                    <button
                      type="button"
                      onClick={() => onJump?.(g.seq)}
                      tabIndex={-1} // a pointer shortcut: the step rows below are the keyboard's way in
                      aria-label={`${label}: ${statusWord(g.status)}`}
                      title={`${label} · ${statusWord(g.status).toUpperCase()}`}
                      data-status={g.status}
                      className={cx(
                        'block h-2.5 w-full rounded-[3px] outline-none ring-offset-2 ring-offset-surface transition-colors focus-visible:ring-2 focus-visible:ring-accent',
                        g.status === 'pending' ? 'bg-line' : g.status === 'skipped' ? 'bg-line/60' : fillOf[t],
                        g.status === 'ok' && 'opacity-80 hover:opacity-100',
                        g.status === 'running' && 'bg-[linear-gradient(90deg,transparent,rgb(255_255_255/0.55),transparent)] bg-no-repeat [background-size:60%_100%] motion-safe:animate-sweep',
                      )}
                    />
                  </li>
                )
              })}
            </ol>
            <p className="mt-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 text-xs tabular-nums text-muted">
              <span>
                {open ? `${done} of ${total} ${total === 1 ? 'step' : 'steps'} done` : `${done - skipped} of ${total} ${total === 1 ? 'step' : 'steps'} ran`}
                {!open && skipped > 0 && ` · ${skipped} skipped`}
              </span>
              {typical !== null && (
                <span data-testid="run-typical">
                  Typically {duration('1970-01-01T00:00:00Z', new Date(typical).toISOString())}
                  {slow && <span className="ml-1.5 font-semibold text-warn-ink">{open ? 'running longer than usual' : 'slower than usual'}</span>}
                </span>
              )}
            </p>
          </div>
        )}

        {run.status === 'failed' && !isCancelled(run) && !isClosedByAdmin(run) && <Failure run={run} onJump={onJump ?? (() => {})} failedSeq={blame?.seq} />}

        {waiting && (
          <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-line bg-surface-2/60 p-3 text-sm" data-testid="queued-notice">
            <Server size={16} aria-hidden className="shrink-0 text-muted" />
            <div className="min-w-[min(100%,14rem)] flex-1">
              <p className="font-medium">{online === false ? 'No runner is online' : 'A runner picks it up within a few seconds'}</p>
              <p className="mt-0.5 text-xs text-muted">{online === false ? 'The run starts as soon as a runner connects. If none does in 10 minutes, the run fails.' : 'If none does in 10 minutes, the run fails.'}</p>
            </div>
            {online === false && (
              <Link to={runnersUrl} className="inline-flex items-center gap-1.5 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium hover:bg-surface-2">
                <Server size={14} aria-hidden /> Runners
              </Link>
            )}
          </div>
        )}

        <div className="mt-4 flex flex-wrap items-center gap-2">
          <ul className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5" aria-label="About this run">
            {!isNotebookRun(run) && (
              <Meta icon={Trig} testId="run-trigger">
                {triggerLabel(run.trigger)}
              </Meta>
            )}
            {run.script_version ? (
              <Meta icon={FileCode2} testId="run-script-version">
                {scriptLabel(run.script_version)}
              </Meta>
            ) : null}
            {runner ? (
              <Meta icon={Server} testId="run-runner">
                Runner {runner}
              </Meta>
            ) : null}
            {waited && (
              <Meta icon={Clock} title="Time between the run being queued and a runner taking it; it is part of the total time" testId="run-waited">
                {waited}
              </Meta>
            )}
            {who && (
              <Meta mono title={where} testId="run-where">
                {who}
              </Meta>
            )}
            {src.command && (
              <Meta mono title={src.command}>
                {src.command}
              </Meta>
            )}
          </ul>
          {onCancel && cancellable(run) && (
            <Btn onClick={onCancel} disabled={run.cancel_requested} data-testid="cancel-run">
              {run.cancel_requested ? <Loader2 size={14} className="motion-safe:animate-spin" aria-hidden /> : <Ban size={14} aria-hidden />} {run.cancel_requested ? 'Stopping…' : 'Cancel run'}
            </Btn>
          )}
        </div>
      </div>
    </section>
  )
}
