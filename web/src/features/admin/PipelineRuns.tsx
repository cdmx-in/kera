// Admin > Pipelines > one pipeline: masthead, health strip, run history and the run view (summary, step waterfall, collection syncs).
// Everything is read-only apart from the run controls; the page polls while a run is open. The pieces live in their own files:
// PipelineMasthead, HealthStrip, RunList, RunSummary, StepWaterfall, KeraSyncs, StepLog.
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Clock, Play, Server, TriangleAlert } from 'lucide-react'
import { cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { attempt, Btn, Confirm, Loading, Page } from './ui'
import { CopyBlock, editUrl, lateText, pipelinesBase, runUrl, TokenDialog } from './Pipelines'
import { Notice } from './Notice'
import { EnvTab } from './PipelineEnv'
import { nextRunText } from './PipelineSchedule'
import { NoRunnerHint } from './PipelineRunners'
import { PipelineMasthead, tabsFor, type Tab } from './PipelineMasthead'
import { ScriptTab } from './PipelineScript'
import { HealthStrip } from './HealthStrip'
import { History } from './RunList'
import { runWhere, RunSummary } from './RunSummary'
import { StepWaterfall } from './StepWaterfall'
import { KeraSyncs } from './KeraSyncs'
import type { StepCtx } from './StepLog'
import { relative } from './settings/Integrations'
import { axisSpan, baselineRun, keraSteps, opensByDefault, scriptSteps, typicalMs, usualSteps } from './runview'
import { isStuck, isNotebookRun, runCommand, runStatus, upToDate, useNow, usePipeline, usePipelineApi, useRun, useRunners, useRuns, stageTitle, type Pipeline, type Run, type Step } from './pipelines'

export { History, Notice, runWhere }

/** /admin/pipelines/:name[/:run][?step=seq] */
export function PipelineDetail({ name, run }: { name: string; run?: string }) {
  const p = usePipeline(name)
  if (!p.data)
    return (
      <Page>
        <Back />
        <Loading q={p} />
      </Page>
    )
  return <Loaded p={p.data} runParam={run} />
}

function Back() {
  return (
    <Link to={pipelinesBase} className="inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
      <ChevronLeft size={14} aria-hidden /> Pipelines
    </Link>
  )
}

/** The command that starts a run, for a pipeline that has none yet and for the "How to run" disclosure. */
function HowTo({ p }: { p: Pipeline }) {
  return (
    <div className="space-y-2 text-sm">
      <p>
        Start it from cron, CI or by hand with the pipeline&apos;s token (it starts with <code>{p.token_prefix}</code>; keep it in an env file rather than on the command line).
      </p>
      <CopyBlock text={runCommand(p, location.origin)} label="command" />
      <p className="text-xs text-muted">
        To wrap a script that fetches first, run <code>kera pipeline run -- ./script.sh</code> and print step markers; see <code>docs/pipelines.md</code> in the Kera repository.
      </p>
    </div>
  )
}

type Ask = 'rotate' | 'delete' | 'close' | 'allow' | 'cancel' | null

const tabUrl = (name: string, tab: Tab) => `${runUrl(name)}${tab === 'runs' ? '' : `?tab=${tab}`}`

/** The call to action on a pipeline that something else starts. */
function LetKeraRun({ onSetUp }: { onSetUp: () => void }) {
  return (
    <Notice
      tone="accent"
      icon={Server}
      testId="let-kera-run"
      actions={
        <Btn onClick={onSetUp} data-testid="let-kera-run-setup">
          Set up how it runs
        </Btn>
      }
    >
      <p className="font-semibold">Let Kera run this</p>
      <p className="mt-0.5 text-xs">Keep the script and a timetable in Kera instead of a cron job. A runner executes it; the runs, steps and logs look the same as they do now.</p>
    </Notice>
  )
}

/** A pipeline that has not run: a ghost of what a run will look like, and the way to the first one. */
function NeverRan({ p, kera, starting, onRunNow, notebookRuns, showNotebook, onShowNotebook }: { p: Pipeline; kera: boolean; starting: boolean; onRunNow: () => void; notebookRuns: number; showNotebook: boolean; onShowNotebook: (v: boolean) => void }) {
  return (
    <section className="relative overflow-hidden rounded-2xl border border-dashed border-line bg-surface p-6 sm:p-8" data-testid="never-ran">
      <div aria-hidden className="pointer-events-none absolute inset-y-0 right-0 hidden w-2/5 flex-col justify-center gap-3 pr-8 opacity-60 md:flex">
        {[ [8, 22], [30, 38], [64, 20], [80, 14] ].map(([l, w], i) => (
          <div key={i} className="relative h-3 rounded-full bg-[linear-gradient(to_right,var(--color-line)_1px,transparent_1px)] [background-size:20%_100%]">
            <span className="absolute inset-y-0 rounded-full border border-dashed border-muted/60" style={{ left: `${l}%`, width: `${w}%` }} />
          </div>
        ))}
      </div>
      <div className="relative max-w-xl">
        <p className="text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">No runs yet</p>
        <h3 className="mt-1 text-lg font-semibold tracking-tight">This pipeline has not run yet</h3>
        {kera ? (
          <p className="mt-2 text-sm text-muted">
            {p.schedule.scheduled ? `Next run: ${nextRunText(p.schedule)}.` : 'It is not scheduled, so it runs only when you press Run now.'} The run shows up here as a timeline of steps with their logs.
          </p>
        ) : (
          <p className="mt-2 text-sm text-muted">The first run shows up here as a timeline of steps with their logs.</p>
        )}
        {kera ? (
          <div className="mt-4 flex flex-wrap gap-2">
            <Btn variant="primary" onClick={onRunNow} disabled={starting || !p.enabled} data-testid="never-ran-run-now">
              <Play size={14} aria-hidden /> Run now
            </Btn>
            <Link to={`${tabUrl(p.name, 'script')}`} className="inline-flex items-center gap-1.5 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium hover:bg-surface-2">
              Open the script
            </Link>
          </div>
        ) : (
          <div className="mt-4">
            <HowTo p={p} />
          </div>
        )}
        {notebookRuns > 0 && (
          <label className="mt-4 flex cursor-pointer items-center gap-2 text-xs text-muted">
            <input type="checkbox" checked={showNotebook} onChange={(e) => onShowNotebook(e.target.checked)} className="size-4 accent-[var(--accent)]" data-testid="show-notebook-runs" />
            Show notebook runs ({notebookRuns})
          </label>
        )}
      </div>
    </section>
  )
}

function Loaded({ p, runParam }: { p: Pipeline; runParam?: string }) {
  const nav = useNavigate()
  const qc = useQueryClient()
  const pa = usePipelineApi()
  const [params] = useSearchParams()
  const [limit, setLimit] = useState(50)
  const [token, setToken] = useState<string | null>(null)
  const [ask, setAsk] = useState<Ask>(null)
  const [starting, setStarting] = useState(false)
  const kera = p.run_mode === 'kera'
  const tab: Tab = tabsFor(kera).find(([k]) => k === params.get('tab'))?.[0] ?? 'runs'
  const runners = useRunners(kera)
  const online = runners.data ? runners.data.runners.some((r) => r.online) : undefined
  const runnerName = (id: string | undefined) => (id ? runners.data?.runners.find((r) => r.id === id)?.name : undefined)
  const runs = useRuns(p.name, limit)
  // runs behind the script notebook (Test, Run all) are dry and not the pipeline's last run: hidden until asked for
  const [showNotebook, setShowNotebook] = useState(false)
  const allRuns = runs.data?.runs ?? []
  const notebookRuns = allRuns.filter(isNotebookRun).length
  const shownRuns = showNotebook ? allRuns : allRuns.filter((r) => !isNotebookRun(r))
  // the history shows once the pipeline has run for real, or when notebook runs are asked for and there are some
  const showHistory = p.last_run !== null || (showNotebook && shownRuns.length > 0)
  const runId = runParam !== undefined ? Number(runParam) : shownRuns[0]?.id
  const noRun = runParam !== undefined && !Number.isInteger(runId)
  const rq = useRun(p.name, runId)
  const run = rq.data
  const now = useNow(run?.status === 'running' || allRuns.some((r) => r.status === 'running') ? 1000 : 0)
  // what is usual: the median length of the good runs, and what each step took in the newest good run
  const typical = typicalMs(allRuns)
  const base = baselineRun(allRuns, run?.id)
  const baseRun = useRun(p.name, base?.id)
  const usual = base && baseRun.data?.id === base.id ? usualSteps(baseRun.data.steps) : undefined
  const stepParam = params.get('step')
  const focus = run && stepParam !== null && /^\d+$/.test(stepParam) ? Number(stepParam) : null
  const refused = run?.counts?.refused ?? 0
  const runNow = async () => {
    setStarting(true)
    try {
      const r = await pa.runNow(p.name)
      toast('Run queued')
      nav(runUrl(p.name, r.id))
    } catch (e) {
      toast((e as Error).message, 'error') // 409 run_active says which run is still going
    } finally {
      setStarting(false)
    }
  }
  const canAllow = !!run && run.id === p.last_run?.id && run.status !== 'running' && refused > 0 && !p.allow_mass_delete

  // when the open run changes state (a runner claims it, a stop is asked for, it closes), the list, the header and the history catch up at once
  const before = useRef<string>(undefined)
  useEffect(() => {
    const now = run ? `${run.id}:${runStatus(run)}:${run.cancel_requested}` : undefined
    if (run && before.current?.startsWith(`${run.id}:`) && before.current !== now) void qc.invalidateQueries({ queryKey: ['admin', 'pipelines'] })
    before.current = now
  }, [run, qc])

  return (
    <Page>
      <Back />
      <PipelineMasthead
        p={p}
        tab={tab}
        tabHref={(t) => tabUrl(p.name, t)}
        runners={kera ? runners.data?.runners : undefined}
        starting={starting}
        onRunNow={() => void runNow()}
        onEdit={() => nav(editUrl(p.name))}
        onRotate={() => setAsk('rotate')}
        onDelete={() => setAsk('delete')}
        onRefresh={() => void qc.invalidateQueries({ queryKey: ['admin', 'pipelines'] })}
      />

      {!p.enabled && <Notice tone="grey">{kera ? 'This pipeline is switched off: the timetable starts no runs and runners skip it. Edit it to switch it on.' : 'This pipeline is switched off: Kera refuses its token. Edit it to switch it on.'}</Notice>}
      {p.late && p.enabled && !p.running && (
        <Notice tone="amber" icon={TriangleAlert} testId="late-notice">
          <strong>Late.</strong> {lateText(p)}
        </Notice>
      )}
      {p.adopt && (
        <Notice tone="accent" actions={<Btn onClick={() => void attempt(() => pa.once(p.name, { adopt: false }), 'Adopt turned off')}>Turn off</Btn>}>
          Adopt is on: the next run takes over same-named files that no pipeline owns. It clears itself after a good run.
        </Notice>
      )}
      {p.allow_mass_delete && (
        <Notice tone="accent" actions={<Btn onClick={() => void attempt(() => pa.once(p.name, { mass_delete: false }), 'Removal no longer allowed')}>Turn off</Btn>}>
          Removal is allowed once: the next run may remove files that would otherwise be refused. It clears itself after a good run.
        </Notice>
      )}

      {tab === 'script' && <ScriptTab name={p.name} />}
      {tab === 'env' && <EnvTab name={p.name} kera={kera} shared={p.env_by_token} />}

      {tab === 'runs' && !kera && !showHistory && <LetKeraRun onSetUp={() => nav(`${editUrl(p.name)}#how-it-runs`)} />}
      {/* a queued run says it itself, with the link to the runners */}
      {tab === 'runs' && kera && !(run?.queued && run.status === 'running') && <NoRunnerHint runners={runners.data?.runners} />}
      {tab === 'runs' && !showHistory && <NeverRan p={p} kera={kera} starting={starting} onRunNow={() => void runNow()} notebookRuns={notebookRuns} showNotebook={showNotebook} onShowNotebook={setShowNotebook} />}
      {tab === 'runs' && showHistory && (
        <>
          <HealthStrip runs={shownRuns} selected={run?.id ?? runId} urlOf={(id) => runUrl(p.name, id)} now={now} more={!!runs.data?.more} />
          <div className="grid gap-4 lg:grid-cols-[17rem_minmax(0,1fr)] lg:items-start xl:grid-cols-[18.5rem_minmax(0,1fr)]">
            <History runs={shownRuns} loading={runs.isLoading} more={!!runs.data?.more} selected={run?.id ?? runId} name={p.name} now={now} onMore={() => setLimit((l) => Math.min(l + 50, 100))} notebookRuns={notebookRuns} showNotebook={showNotebook} onShowNotebook={setShowNotebook} />
            {run ? (
              <div className={cx('min-w-0', rq.isPlaceholderData && 'opacity-60 transition-opacity')}>
                <RunView
                  key={run.id}
                  p={p}
                  run={run}
                  focus={focus}
                  now={now}
                  typical={typical}
                  usual={usual}
                  online={kera ? online : undefined}
                  onClose={() => setAsk('close')}
                  onCancel={() => setAsk('cancel')}
                  onAllow={canAllow ? () => setAsk('allow') : undefined}
                  refused={refused}
                  runner={runnerName(run.runner)}
                />
              </div>
            ) : rq.error || noRun ? (
              <Notice tone="red" role="alert">
                {runParam !== undefined ? `There is no run ${runParam} in this pipeline.` : (rq.error as Error).message}{' '}
                <Link to={runUrl(p.name)} className="underline">
                  Newest run
                </Link>
              </Notice>
            ) : (
              <Loading q={rq} />
            )}
          </div>
          {!kera && (
            <>
              <details className="rounded-xl border border-line bg-surface text-sm">
                <summary className="cursor-pointer select-none px-4 py-2.5 font-medium">How to run</summary>
                <div className="border-t border-line p-4">
                  <HowTo p={p} />
                </div>
              </details>
              <LetKeraRun onSetUp={() => nav(`${editUrl(p.name)}#how-it-runs`)} />
            </>
          )}
        </>
      )}

      {token && <TokenDialog pipeline={p} token={token} onClose={() => setToken(null)} />}
      <Confirm
        open={ask === 'rotate'}
        title="Rotate the token?"
        confirmLabel="Rotate"
        body="The current token stops working at once. Every script, cron job and CI secret that uses it must be given the new one, which is shown once."
        onConfirm={async () => setToken((await pa.rotate(p.name)).token)}
        onClose={() => setAsk(null)}
      />
      <Confirm
        open={ask === 'delete'}
        title={`Delete ${p.name}?`}
        body="The pipeline and its run history are deleted. The collections and their files stay; files this pipeline uploaded are handed back and nothing replaces or removes them any more."
        onConfirm={async () => {
          await pa.remove(p.name)
          nav(pipelinesBase)
        }}
        onClose={() => setAsk(null)}
      />
      <Confirm
        open={ask === 'close'}
        title="Close this run?"
        confirmLabel="Close run"
        body="The run is marked failed. A step that was still running fails with it, steps that never started are skipped, and nothing is removed from the collections. Do this only if the script has stopped."
        onConfirm={async () => void (run && (await pa.close(p.name, run.id)))}
        onClose={() => setAsk(null)}
      />
      <Confirm
        open={ask === 'cancel'}
        title={`Cancel run #${run?.id}?`}
        confirmLabel="Cancel run"
        body={run?.queued ? 'No runner has started it, so it is closed at once.' : 'The runner stops the script and everything it started, usually within a few seconds. Whatever the script already changed stays changed.'}
        onConfirm={async () => void (run && (await pa.cancel(p.name, run.id)))}
        onClose={() => setAsk(null)}
      />
      <Confirm
        open={ask === 'allow'}
        title={`Allow removing ${refused} ${refused === 1 ? 'file' : 'files'} once?`}
        confirmLabel="Allow once"
        body={`The last run did not remove ${refused === 1 ? 'a file' : `${refused} files`} because they were missing from the folder and that is more than half of the pipeline's files. If the folder is right, the next run removes them. If it is not, fix the folder first.`}
        onConfirm={async () => void (await pa.once(p.name, { mass_delete: true }))}
        onClose={() => setAsk(null)}
      />
    </Page>
  )
}

// The selected run --------------------------------------------------------------------

const reduced = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches

/**
 * Which step panels are open. A running step and a failed one open by themselves (the running one follows its log);
 * a click on a row decides for that row from then on. `focus` is the ?step= deep link: it opens its row and scrolls to it.
 */
export function useOpenSteps(steps: Step[], focus: number | null) {
  const [manual, setManual] = useState<Record<number, boolean>>({})
  const [flash, setFlash] = useState<number | null>(null)
  const isOpen = (s: Step) => manual[s.seq] ?? opensByDefault(s, focus)
  const reveal = (seq: number) => {
    setManual((m) => ({ ...m, [seq]: true }))
    setFlash(seq)
    setTimeout(() => document.getElementById(`step-${seq}`)?.scrollIntoView({ block: 'center', behavior: reduced() ? 'auto' : 'smooth' }), 60)
    setTimeout(() => setFlash((f) => (f === seq ? null : f)), 1800)
  }
  const landed = useRef(false)
  useEffect(() => {
    if (focus !== null && !landed.current && steps.some((s) => s.seq === focus)) {
      landed.current = true
      reveal(focus)
    }
    // reveal only reads setters
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus, steps.length])
  return { isOpen, toggle: (s: Step) => setManual((m) => ({ ...m, [s.seq]: !isOpen(s) })), reveal, flash }
}

function RunView({
  p,
  run,
  focus,
  now,
  typical,
  usual,
  online,
  refused,
  runner,
  onClose,
  onCancel,
  onAllow,
}: {
  p: Pipeline
  run: Run
  focus: number | null
  now: number
  typical: number | null
  usual: Record<string, number> | undefined
  online: boolean | undefined
  refused: number
  runner?: string
  onClose: () => void
  onCancel: () => void
  onAllow?: () => void
}) {
  const steps = run.steps ?? []
  const script = scriptSteps(steps)
  const colls = keraSteps(steps)
  const open = run.status === 'running'
  const current = upToDate(run.counts)
  const { isOpen, toggle, reveal, flash } = useOpenSteps(steps, focus)
  const warned = steps.find((s) => s.status === 'warning')
  const ctx: StepCtx = { pipeline: p.name, runId: run.id, stages: p.stages ?? [], now, editHref: `${editUrl(p.name)}#stages`, usual }
  const span = axisSpan(run, script, now, open ? typical : null)
  return (
    <div className="space-y-4" data-testid="run-view" data-status={run.status}>
      <RunSummary run={run} steps={steps} stages={p.stages} now={now} runner={runner} typical={typical} online={online} onCancel={onCancel} onJump={reveal} />

      {isStuck(run, now) && (
        <Notice
          tone="amber"
          icon={TriangleAlert}
          testId="stuck-notice"
          actions={
            <Btn onClick={onClose} data-testid="close-run">
              Close run
            </Btn>
          }
        >
          Nothing has been reported since {new Date(run.active_at).toLocaleTimeString(undefined, { timeStyle: 'short' })} ({relative(run.active_at)}). If the script has stopped, close the run; Kera closes it by itself after 30 minutes.
        </Notice>
      )}
      {run.status === 'warning' && (
        <Notice
          tone="amber"
          icon={TriangleAlert}
          role="status"
          testId="warning-notice"
          actions={
            <span className="flex flex-wrap gap-2">
              {warned && (
                <Btn onClick={() => reveal(warned.seq)} data-testid="open-warning-step">
                  Show the step
                </Btn>
              )}
              {onAllow && (
                <Btn onClick={onAllow} data-testid="allow-once">
                  Allow removing {refused} {refused === 1 ? 'file' : 'files'} once
                </Btn>
              )}
            </span>
          }
        >
          <p className="font-medium">{warned ? `Warning at ${stageTitle(warned, p.stages)}` : 'Finished with a warning'}</p>
          {run.error && <p className="mt-1 break-words">{run.error}</p>}
        </Notice>
      )}
      {run.dry_run && (
        <p className="text-sm text-muted" data-testid="dry-run-note">
          Dry run: nothing was written. The counts show what a real run would do.
        </p>
      )}
      {!open && !run.dry_run && run.status !== 'failed' && current > 0 && (
        <p className="text-sm text-muted" data-testid="no-changes">
          Nothing changed: {current} {current === 1 ? 'file' : 'files'} already up to date.
        </p>
      )}

      {script.length > 0 && <StepWaterfall run={run} steps={script} ctx={ctx} span={span} isOpen={isOpen} onToggle={toggle} flash={flash} />}
      {colls.length > 0 && <KeraSyncs steps={colls} counts={run.counts ?? {}} ctx={ctx} isOpen={isOpen} onToggle={toggle} flash={flash} />}
      {steps.length === 0 && (
        <div className="rounded-2xl border border-dashed border-line p-5 text-sm text-muted" data-testid="no-steps">
          <p className="flex items-center gap-2 font-medium text-fg">
            <Clock size={14} aria-hidden /> {run.queued && open ? 'Nothing has started yet' : open ? 'No steps reported yet' : 'This run reported no steps'}
          </p>
          <p className="mt-1">{run.queued && open ? 'The steps appear here when a runner starts the script.' : open ? 'They appear here as the script reports them.' : 'The script ended without printing a step marker, so there is nothing to lay out on the timeline.'}</p>
        </div>
      )}
    </div>
  )
}

