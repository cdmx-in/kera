// Admin > Pipelines > one pipeline: header, run history, the flow run view
// (a hand-built diagram: script steps, then one node per collection) and the
// step drawer with its log. Everything is read-only; the page polls while a run is open.
import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useSearchParams, type NavigateFunction } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowRight, Ban, Check, ChevronDown, ChevronLeft, Circle, CircleAlert, CircleDashed, Clock, Copy, KeyRound, Library, Loader2, Pencil, Play, RefreshCw, Server, Terminal, Trash2, TriangleAlert, Workflow, type LucideIcon } from 'lucide-react'
import { Chip } from '@/features/workspace/ui'
import { copyText, cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { attempt, Badge, Btn, Card, Confirm, Loading, ManagedBadge, Page } from './ui'
import { CopyBlock, editUrl, lateText, pipelinesBase, StatusBadge, TokenDialog, tones } from './Pipelines'
import { Notice } from './Notice'
import { EnvTab } from './PipelineEnv'
import { nextRunText } from './PipelineSchedule'
import { NoRunnerHint } from './PipelineRunners'
import { ScriptTab } from './PipelineScript'
import { relative, when } from './settings/Integrations'
import {
  atBottom,
  cancellable,
  countChips,
  countLabel,
  duration,
  isNotebookRun,
  everyText,
  failedStep,
  isStuck,
  logTail,
  queuedText,
  runCommand,
  runStatus,
  runSummary,
  scheduleSummary,
  scriptLabel,
  stateOf,
  statusLabel,
  triggerLabel,
  upToDate,
  useNow,
  usePipeline,
  usePipelineApi,
  useRun,
  useRunners,
  useRuns,
  useStep,
  useWide,
  type Pipeline,
  type Run,
  type Step,
  type StepStatus,
} from './pipelines'

/**
 * Closing the step drawer. When this page pushed the step (it marks the entry), go Back one entry so the
 * history is as before; a deep-linked step has nothing to go back to, so its ?step= is dropped in place.
 * The caller skips this when no step is open: Dialog also reports the close that follows a URL change.
 */
export function leaveStep(nav: NavigateFunction, state: unknown, runUrl: string) {
  if ((state as { drawer?: boolean } | null)?.drawer) void nav(-1)
  else void nav(runUrl, { replace: true })
}

const urlOf = (name: string, run?: number, step?: number) => `${pipelinesBase}/${encodeURIComponent(name)}${run != null ? `/${run}` : ''}${step != null ? `?step=${step}` : ''}`

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

export { Notice }

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

type Tab = 'runs' | 'script' | 'env'
const tabs: [Tab, string][] = [
  ['runs', 'Runs'],
  ['script', 'Script'],
  ['env', 'Environment'],
]
const tabUrl = (name: string, tab: Tab) => `${urlOf(name)}${tab === 'runs' ? '' : `?tab=${tab}`}`
const tabCls = (on: boolean) => cx('whitespace-nowrap rounded-md px-2.5 py-1 text-sm', on ? 'bg-accent-soft text-accent dark:text-accent-bright' : 'text-muted hover:bg-surface-2 hover:text-fg')

function Loaded({ p, runParam }: { p: Pipeline; runParam?: string }) {
  const nav = useNavigate()
  const navState = useLocation().state
  const qc = useQueryClient()
  const pa = usePipelineApi()
  const [params] = useSearchParams()
  const [limit, setLimit] = useState(30)
  const [token, setToken] = useState<string | null>(null)
  const [ask, setAsk] = useState<Ask>(null)
  const [starting, setStarting] = useState(false)
  const kera = p.run_mode === 'kera'
  const tab: Tab = kera ? (tabs.find(([k]) => k === params.get('tab'))?.[0] ?? 'runs') : 'runs'
  const runners = useRunners(kera)
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
  const now = useNow(run?.status === 'running' ? 1000 : 0)
  const state = stateOf(p)
  const stepParam = params.get('step')
  const seq = run && stepParam !== null && /^\d+$/.test(stepParam) ? Number(stepParam) : null
  const refused = run?.counts?.refused ?? 0
  const runNow = async () => {
    setStarting(true)
    try {
      const r = await pa.runNow(p.name)
      toast('Run queued')
      nav(urlOf(p.name, r.id))
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
      <div className="flex flex-wrap items-start gap-3">
        {/* a readable width, so the buttons wrap under the title on a tablet instead of squeezing it */}
        <span className="grid size-11 shrink-0 place-items-center rounded-xl bg-accent-soft text-accent">
          <Workflow size={22} aria-hidden />
        </span>
        <div className="min-w-[min(100%,18rem)] flex-1">
          <h2 className="flex flex-wrap items-center gap-2 break-words text-lg font-semibold tracking-tight">
            {p.title || p.name} <StatusBadge status={state} /> {p.managed && <ManagedBadge />}
          </h2>
          <p className="mt-0.5 text-[13px] text-muted">
            <code>{p.name}</code> · Owner {p.owner.name || p.owner.email} ·{' '}
            {kera ? (
              <span data-testid="kera-timetable">
                Run by Kera: {p.schedule.scheduled ? scheduleSummary(p.schedule) : 'not scheduled'}
                {p.schedule.scheduled && p.schedule.next_run_at && <span title={nextRunText(p.schedule)}>, next run {relative(p.schedule.next_run_at)}</span>}
              </span>
            ) : p.every_minutes > 0 ? (
              `Expected every ${everyText(p.every_minutes)}`
            ) : (
              'No expected interval'
            )}
            {p.last_ok_at && <span title={when(p.last_ok_at)}> · Last good run {relative(p.last_ok_at)}</span>}
          </p>
          <div className="mt-2 flex flex-wrap gap-1">
            {p.targets.map((t) => (
              <Chip key={t.id} tone={t.missing ? 'red' : 'grey'} title={t.missing ? 'This collection was deleted' : undefined}>
                {t.missing ? 'Deleted collection' : t.name}
              </Chip>
            ))}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Btn variant="ghost" aria-label="Refresh" onClick={() => void qc.invalidateQueries({ queryKey: ['admin', 'pipelines'] })}>
            <RefreshCw size={14} />
          </Btn>
          {kera && (
            <Btn variant="primary" onClick={() => void runNow()} disabled={starting || !p.enabled} title={p.enabled ? 'Queue a run now, outside the timetable' : 'Switched off'} data-testid="run-now">
              <Play size={14} aria-hidden /> Run now
            </Btn>
          )}
          <Btn onClick={() => nav(editUrl(p.name))} data-testid="pipeline-edit">
            <Pencil size={14} aria-hidden /> Edit
          </Btn>
          <Btn onClick={() => setAsk('rotate')} data-testid="pipeline-rotate">
            <KeyRound size={14} aria-hidden /> Rotate token
          </Btn>
          <Btn onClick={() => setAsk('delete')} className="text-red-600 dark:text-red-400" data-testid="pipeline-delete">
            <Trash2 size={14} aria-hidden /> Delete
          </Btn>
        </div>
      </div>

      {kera && (
        <nav aria-label="Pipeline sections" className="flex flex-wrap gap-1 border-b border-line pb-2" data-testid="pipeline-tabs">
          {tabs.map(([k, l]) => (
            <Link key={k} to={tabUrl(p.name, k)} aria-current={tab === k ? 'page' : undefined} className={tabCls(tab === k)}>
              {l}
            </Link>
          ))}
        </nav>
      )}

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
      {tab === 'env' && <EnvTab name={p.name} />}

      {tab === 'runs' && !kera && <LetKeraRun onSetUp={() => nav(`${editUrl(p.name)}#how-it-runs`)} />}
      {tab === 'runs' && kera && <NoRunnerHint runners={runners.data?.runners} />}
      {tab === 'runs' && !showHistory && (
        <Card title="This pipeline has not run yet" hint={kera ? 'Press Run now to try it, or wait for the timetable.' : 'The first run shows up here as a flow of steps with their logs.'}>
          <div className="p-5" data-testid="never-ran">
            {kera ? (
              <p className="text-sm text-muted">
                {p.schedule.scheduled ? `Next run: ${nextRunText(p.schedule)}.` : 'It is not scheduled, so it runs only when you press Run now.'} The run shows up here as a flow of steps with their logs.
              </p>
            ) : (
              <HowTo p={p} />
            )}
            {notebookRuns > 0 && (
              <label className="mt-3 flex cursor-pointer items-center gap-2 text-xs text-muted">
                <input type="checkbox" checked={showNotebook} onChange={(e) => setShowNotebook(e.target.checked)} className="size-4 accent-[var(--accent)]" data-testid="show-notebook-runs" />
                Show notebook runs ({notebookRuns})
              </label>
            )}
          </div>
        </Card>
      )}
      {tab === 'runs' && showHistory && (
        <>
          {!kera && (
            <details className="rounded-xl border border-line bg-surface text-sm">
              <summary className="cursor-pointer select-none px-4 py-2.5 font-medium">How to run</summary>
              <div className="border-t border-line p-4">
                <HowTo p={p} />
              </div>
            </details>
          )}
          <div className="grid gap-4 lg:grid-cols-[18rem_minmax(0,1fr)] lg:items-start">
            <History runs={shownRuns} loading={runs.isLoading} more={!!runs.data?.more} selected={run?.id ?? runId} name={p.name} now={now} onMore={() => setLimit((l) => Math.min(l + 30, 100))} notebookRuns={notebookRuns} showNotebook={showNotebook} onShowNotebook={setShowNotebook} />
            {run ? (
              <div className={cx('min-w-0', rq.isPlaceholderData && 'opacity-60')}>
                <RunView
                  p={p}
                  run={run}
                  seq={seq}
                  now={now}
                  onOpen={(s) => nav(urlOf(p.name, run.id, s), { state: { drawer: true } })}
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
                <Link to={urlOf(p.name)} className="underline">
                  Newest run
                </Link>
              </Notice>
            ) : (
              <Loading q={rq} />
            )}
          </div>
        </>
      )}

      <StepDrawer name={p.name} run={run} seq={seq} now={now} onClose={() => run && seq !== null && leaveStep(nav, navState, urlOf(p.name, run.id))} />
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

// Run history ---------------------------------------------------------------------------

export function History({
  runs,
  loading,
  more,
  selected,
  name,
  now,
  onMore,
  notebookRuns = 0,
  showNotebook = false,
  onShowNotebook,
}: {
  runs: Run[]
  loading: boolean
  more: boolean
  selected: number | undefined
  name: string
  now: number
  onMore: () => void
  /** how many runs of the loaded ones came from the script notebook */
  notebookRuns?: number
  showNotebook?: boolean
  onShowNotebook?: (v: boolean) => void
}) {
  const wide = useWide()
  const [open, setOpen] = useState(false)
  const list = (
    <nav aria-label="Run history" data-testid="run-history">
      {loading && !runs.length ? (
        <p className="px-5 py-4 text-sm text-muted">Loading runs…</p>
      ) : (
        <ul className="max-h-[70dvh] divide-y divide-line overflow-y-auto [scrollbar-color:var(--color-line)_transparent] [scrollbar-width:thin]">
          {runs.map((r) => (
            <li key={r.id}>
              <Link
                to={urlOf(name, r.id)}
                onClick={() => setOpen(false)}
                aria-current={r.id === selected ? 'page' : undefined}
                data-testid="run-item"
                data-status={r.status}
                data-queued={r.queued ? 'true' : undefined}
                className={cx('block space-y-1 border-l-[3px] px-4 py-3 text-sm hover:bg-surface-2/60', runLook(r).rail, r.id === selected && 'bg-accent-soft')}
              >
                <span className="flex flex-wrap items-center gap-1.5">
                  <StatusBadge status={runStatus(r)} />
                  {r.dry_run && <Badge tone="accent">Dry run</Badge>}
                  <span className="ml-auto text-xs text-muted">#{r.id}</span>
                </span>
                <span className="block text-xs text-muted" title={when(r.started_at)}>
                  {relative(r.started_at)} · {duration(r.started_at, r.finished_at, now)} · {triggerLabel(r.trigger)}
                  {r.script_version ? ` · ${scriptLabel(r.script_version)}` : ''}
                </span>
                <span className="line-clamp-2 break-words text-xs" title={r.status === 'failed' ? r.error : undefined}>
                  {runSummary(r)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {onShowNotebook && notebookRuns > 0 && (
        <label className="flex cursor-pointer items-center gap-2 border-t border-line px-4 py-2.5 text-xs text-muted">
          <input type="checkbox" checked={showNotebook} onChange={(e) => onShowNotebook(e.target.checked)} className="size-4 accent-[var(--accent)]" data-testid="show-notebook-runs" />
          Show notebook runs ({notebookRuns})
        </label>
      )}
      {more && (
        <div className="border-t border-line p-3">
          <Btn className="w-full" onClick={onMore} data-testid="load-older">
            Load older
          </Btn>
        </div>
      )}
    </nav>
  )
  if (wide) return <Card title="Run history">{list}</Card>
  return (
    <details open={open} onToggle={(e) => setOpen(e.currentTarget.open)} className="overflow-hidden rounded-2xl border border-line bg-surface">
      <summary className="cursor-pointer select-none px-5 py-3.5 text-[15px] font-semibold">
        Run history <span className="font-normal text-muted">({runs.length}{more ? '+' : ''})</span>
      </summary>
      <div className="border-t border-line">{list}</div>
    </details>
  )
}

// The selected run --------------------------------------------------------------------

/** How each status looks: icon, tinted tile, the colour of its word and the rail on the left edge of a card. */
const look: Record<StepStatus, { icon: LucideIcon; tile: string; text: string; rail: string }> = {
  pending: { icon: Circle, tile: 'bg-surface-2 text-muted', text: 'text-muted', rail: 'border-l-line' },
  running: { icon: Loader2, tile: 'bg-accent-soft text-accent', text: 'text-accent', rail: 'border-l-accent' },
  ok: { icon: Check, tile: 'bg-green-500/15 text-green-700 dark:text-green-400', text: 'text-green-700 dark:text-green-400', rail: 'border-l-green-500' },
  warning: { icon: TriangleAlert, tile: 'bg-amber-500/15 text-amber-700 dark:text-amber-300', text: 'text-amber-700 dark:text-amber-300', rail: 'border-l-amber-500' },
  failed: { icon: CircleAlert, tile: 'bg-red-500/15 text-red-700 dark:text-red-400', text: 'text-red-700 dark:text-red-400', rail: 'border-l-red-500' },
  skipped: { icon: CircleDashed, tile: 'bg-surface-2 text-muted', text: 'text-muted', rail: 'border-l-line' },
}

const queuedLook = { icon: Clock, tile: 'bg-surface-2 text-muted', text: 'text-muted', rail: 'border-l-line' }
/** How a run looks: its status, or the waiting look of a queued run (its status is still running). */
const runLook = (r: Pick<Run, 'status' | 'queued'>) => (r.queued && r.status === 'running' ? queuedLook : (look[r.status as StepStatus] ?? look.pending))

/** The headline numbers of a run, as tiles: [count key, label, colour when above zero, shown even at zero]. */
const stats: [string, string, 'text-green-700 dark:text-green-400' | 'text-amber-700 dark:text-amber-300' | 'text-red-700 dark:text-red-400' | 'text-accent' | '', boolean][] = [
  ['listed', 'Files', '', true],
  ['uploaded', 'Changed', 'text-green-700 dark:text-green-400', true],
  ['adopted', 'Adopted', '', true],
  ['unchanged', 'Unchanged', '', true],
  ['removed', 'Removed', 'text-amber-700 dark:text-amber-300', true],
  ['failed', 'Failed', 'text-red-700 dark:text-red-400', true],
  ['refused', 'Not removed', 'text-amber-700 dark:text-amber-300', false],
  ['waiting', 'Waiting', 'text-accent', false],
]

/** "root@build-01.example.com: bash /srv/refresh.sh" for the run header; empty for a run that reported nothing. */
export function runWhere(run: Pick<Run, 'source'>): string {
  const s = run.source ?? {}
  const who = [s.user, s.host].filter(Boolean).join('@')
  return [who, s.command].filter(Boolean).join(': ')
}

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

export function RunHeader({ run, now, runner, onCancel }: { run: Run; now: number; runner?: string; onCancel?: () => void }) {
  const l = runLook(run)
  const waiting = run.queued && run.status === 'running'
  const c = run.counts ?? {}
  const tiles = stats.filter(([k, , , always]) => (always ? c[k] !== undefined : (c[k] ?? 0) > 0))
  return (
    <div className="rounded-2xl border border-line bg-surface p-4 sm:p-5">
      <div className="flex flex-wrap items-center gap-3">
        <span className={cx('grid size-11 shrink-0 place-items-center rounded-xl', l.tile)}>
          <l.icon size={22} aria-hidden className={run.status === 'running' && !waiting ? 'animate-spin' : ''} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="flex flex-wrap items-center gap-2 text-base font-semibold">
            Run #{run.id}
            <StatusBadge status={runStatus(run)} />
            {run.cancel_requested && run.status === 'running' && <Badge tone="amber">Stopping…</Badge>}
            {run.dry_run && <Badge tone="accent">Dry run</Badge>}
          </h3>
          <p className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-xs text-muted" title={when(run.started_at)}>
            <span>Started {relative(run.started_at)}</span>·
            <span className="inline-flex items-center gap-1">
              <Clock size={11} aria-hidden /> {duration(run.started_at, run.finished_at, now)}
            </span>
            ·<span>{triggerLabel(run.trigger)}</span>
            {run.script_version ? (
              <>
                ·<span data-testid="run-script-version">{scriptLabel(run.script_version)}</span>
              </>
            ) : null}
            {runner ? (
              <>
                ·<span data-testid="run-runner">Runner {runner}</span>
              </>
            ) : null}
          </p>
          {runWhere(run) && (
            <p className="mt-0.5 truncate font-mono text-xs text-muted" title={runWhere(run)} data-testid="run-where">
              {runWhere(run)}
            </p>
          )}
        </div>
        {onCancel && cancellable(run) && (
          <Btn onClick={onCancel} disabled={run.cancel_requested} data-testid="cancel-run">
            {run.cancel_requested ? <Loader2 size={14} className="animate-spin" aria-hidden /> : <Ban size={14} aria-hidden />} {run.cancel_requested ? 'Stopping…' : 'Cancel run'}
          </Btn>
        )}
      </div>
      {tiles.length > 0 && (
        <dl className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6" data-testid="run-stats">
          {tiles.map(([k, label, hot]) => (
            <div key={k} className="rounded-xl border border-line bg-surface-2/40 px-3 py-2">
              <dt className="text-xs text-muted">{label}</dt>
              <dd className={cx('text-xl font-semibold tabular-nums', c[k] > 0 ? hot : 'text-muted')}>{c[k]}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  )
}

function RunView({ p, run, seq, now, refused, runner, onOpen, onClose, onCancel, onAllow }: { p: Pipeline; run: Run; seq: number | null; now: number; refused: number; runner?: string; onOpen: (seq: number) => void; onClose: () => void; onCancel: () => void; onAllow?: () => void }) {
  const steps = [...(run.steps ?? [])].sort((a, b) => a.seq - b.seq)
  const open = run.status === 'running'
  const blame = failedStep(steps)
  const current = upToDate(run.counts)
  return (
    <div className="space-y-4" data-testid="run-view" data-status={run.status}>
      <RunHeader run={run} now={now} runner={runner} onCancel={onCancel} />

      {open && run.queued && (
        <Notice tone="grey" icon={Clock} role="status" testId="queued-notice">
          <p className="font-medium">{queuedText}</p>
          <p className="mt-1">A runner picks it up within a few seconds. If none does in 10 minutes, the run fails.</p>
        </Notice>
      )}

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
      {run.status === 'failed' && <FailedBanner name={p.name} run={run} step={blame} onOpen={onOpen} />}
      {run.status === 'warning' && (
        <Notice
          tone="amber"
          icon={TriangleAlert}
          role="status"
          testId="warning-notice"
          actions={
            onAllow && (
              <Btn onClick={onAllow} data-testid="allow-once">
                Allow removing {refused} {refused === 1 ? 'file' : 'files'} once
              </Btn>
            )
          }
        >
          <p className="font-medium">Finished with a warning</p>
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

      {steps.length ? <Flow steps={steps} seq={seq} now={now} onOpen={onOpen} /> : <p className="rounded-xl border border-dashed border-line p-4 text-sm text-muted">No steps reported yet.</p>}
    </div>
  )
}

/** Ref callback: show the end of a scroll box (the decisive lines of a log tail come last). */
const toEnd = (el: HTMLElement | null) => {
  if (el) el.scrollTop = el.scrollHeight
}

/** The red banner of a failed run: the error in plain words, the failed step's last lines, a way to its step. */
function FailedBanner({ name, run, step, onOpen }: { name: string; run: Run; step: Step | undefined; onOpen: (seq: number) => void }) {
  const tail = logTail(useStep(name, run.id, step?.seq, !!step).data?.log)
  return (
    <Notice
      tone="red"
      icon={CircleAlert}
      role="alert"
      testId="failed-banner"
      actions={
        step && (
          <Btn onClick={() => onOpen(step.seq)} data-testid="open-step">
            Open step
          </Btn>
        )
      }
    >
      <p className="font-medium">Run failed</p>
      <p className="mt-1 break-words">{run.error || 'No error text was recorded.'}</p>
      {tail && (
        <>
          <p className="mt-2 text-xs">Last lines of {step?.name}</p>
          <pre ref={toEnd} className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-black/5 p-2 text-xs dark:bg-black/30" data-testid="failed-tail">
            {tail}
          </pre>
        </>
      )}
    </Notice>
  )
}

// Flow diagram --------------------------------------------------------------------------

const ring: Record<StepStatus, string> = {
  pending: 'border-line',
  running: 'border-accent/50',
  ok: 'border-line',
  warning: 'border-amber-400/60 dark:border-amber-700',
  failed: 'border-red-400/60 dark:border-red-700',
  skipped: 'border-dashed border-line text-muted',
}

function FlowNode({ s, on, now, onClick }: { s: Step; on: boolean; now: number; onClick: () => void }) {
  const l = look[s.status]
  const chips = countChips(s)
  return (
    <button
      type="button"
      aria-pressed={on}
      data-testid="flow-node"
      data-status={s.status}
      data-kind={s.kera ? 'kera' : 'script'}
      onClick={onClick}
      className={cx(
        'flex h-full min-w-0 flex-1 items-start gap-3 rounded-xl border border-l-4 bg-surface px-3 py-2.5 text-left text-sm outline-none transition-colors hover:bg-surface-2/60 focus-visible:border-accent focus-visible:ring-3 focus-visible:ring-accent-soft aria-pressed:border-accent aria-pressed:ring-2 aria-pressed:ring-accent',
        ring[s.status],
        l.rail,
      )}
    >
      <span className={cx('grid size-8 shrink-0 place-items-center rounded-lg', l.tile)}>
        <l.icon size={16} aria-hidden className={s.status === 'running' ? 'animate-spin' : ''} />
      </span>
      <span className="min-w-0 flex-1 space-y-1">
        <span className="line-clamp-2 block break-words font-medium leading-snug" title={s.name}>
          {s.name}
        </span>
        <span className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted">
          <span className={cx('font-medium', l.text)}>{statusLabel[s.status]}</span>
          {s.started_at && (
            <>
              ·
              <span className="inline-flex items-center gap-1">
                <Clock size={11} aria-hidden /> {duration(s.started_at, s.finished_at, now)}
              </span>
            </>
          )}
        </span>
        {chips.length > 0 && (
          <span className="flex flex-wrap gap-1">
            {chips.map((c) => (
              <Badge key={c.text} tone={c.tone}>
                {c.text}
              </Badge>
            ))}
          </span>
        )}
      </span>
    </button>
  )
}

const Connector = () => (
  <span aria-hidden className="hidden shrink-0 items-center px-1.5 text-muted sm:inline-flex">
    <ArrowRight size={16} />
  </span>
)

/** A lane of the diagram: a small tinted icon, its name and one line of what it is. */
function LaneTitle({ icon: Icon, tone, title, hint, id }: { icon: LucideIcon; tone: keyof typeof tones; title: string; hint: string; id?: string }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <span className={cx('grid size-6 place-items-center rounded-md', tones[tone])}>
        <Icon size={13} aria-hidden />
      </span>
      <span id={id} className="text-xs font-semibold uppercase tracking-wide">
        {title}
      </span>
      <span className="text-xs text-muted">{hint}</span>
    </div>
  )
}

// ponytail: a linear chain plus a fan to the collections; add an `after` column and a layout when steps branch.
export function Flow({ steps, seq, now, onOpen }: { steps: Step[]; seq: number | null; now: number; onOpen: (seq: number) => void }) {
  // the implicit "start" step comes first, whatever order an older run recorded it in
  const plain = steps.filter((s) => !s.kera)
  const script = [...plain.filter((s) => s.name === 'start'), ...plain.filter((s) => s.name !== 'start')]
  const kera = steps.filter((s) => s.kera)
  const node = (s: Step) => <FlowNode s={s} on={s.seq === seq} now={now} onClick={() => onOpen(s.seq)} />
  return (
    <div data-testid="flow" className="space-y-1 rounded-2xl border border-line bg-surface p-4 sm:p-5">
      {script.length > 0 && (
        <section className="space-y-2.5">
          <LaneTitle icon={Terminal} tone="violet" title="Script" hint="The steps your script reports" />
          <ol aria-label="Script steps" className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-stretch">
            {script.map((s, i) => (
              <li key={s.seq} className="flex items-stretch sm:min-w-[12rem] sm:flex-1">
                {node(s)}
                {i < script.length - 1 && <Connector />}
              </li>
            ))}
          </ol>
        </section>
      )}
      {script.length > 0 && kera.length > 0 && (
        <div aria-hidden className="flex flex-col items-center py-1 text-muted">
          <span className="h-3 w-px bg-line" />
          <ChevronDown size={16} />
        </div>
      )}
      {kera.length > 0 && (
        <div role="group" aria-labelledby="flow-kera" className="space-y-2.5 rounded-xl border border-line bg-surface-2/40 p-3 sm:p-4">
          <LaneTitle id="flow-kera" icon={Library} tone="sky" title="Kera" hint={`${kera.length} ${kera.length === 1 ? 'collection' : 'collections'} kept in sync`} />
          <ul className="grid grid-cols-[repeat(auto-fill,minmax(14rem,1fr))] gap-2">
            {kera.map((s) => (
              <li key={s.seq} className="flex">
                {node(s)}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

// Step drawer ---------------------------------------------------------------------------

function StepDrawer({ name, run, seq, now, onClose }: { name: string; run: Run | undefined; seq: number | null; now: number; onClose: () => void }) {
  const known = run?.steps?.find((s) => s.seq === seq)
  const q = useStep(name, run?.id, seq ?? undefined, seq !== null)
  const step = q.data ?? known
  return (
    <Dialog open={seq !== null} onClose={onClose} drawer label="Step details" testId="step-drawer">
      {seq !== null && (
        <div className="flex h-full flex-col">
          <DialogHeader title={step?.name ?? `Step ${seq}`} onClose={onClose} />
          {step ? (
            <StepBody step={step} now={now} loadFailed={!!q.error} />
          ) : (
            <p className="p-4 text-sm text-muted" role={q.error ? 'alert' : undefined}>
              {q.error ? 'This step does not exist in this run.' : 'Loading…'}
            </p>
          )}
        </div>
      )}
    </Dialog>
  )
}

/** loadFailed: the step request errored, so a missing log will not arrive. */
export function StepBody({ step, now, loadFailed }: { step: Step; now: number; loadFailed?: boolean }) {
  const box = useRef<HTMLPreElement>(null)
  const follow = useRef(true) // stays true until the reader scrolls up
  const log = step.log ?? ''
  useEffect(() => {
    const el = box.current
    if (el && follow.current) el.scrollTop = el.scrollHeight
  }, [log])
  const counts = Object.entries(step.counts ?? {})
  const waiting = step.status === 'pending' || step.status === 'running'
  return (
    <div className="flex-1 space-y-4 overflow-y-auto p-4 text-sm" data-testid="step-body" data-status={step.status}>
      <p className="text-xs text-muted">{step.kera ? 'Kera · collection' : 'Script step'}</p>
      <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-1.5">
        <dt className="text-muted">Status</dt>
        <dd>
          <StatusBadge status={step.status} />
        </dd>
        <dt className="text-muted">Started</dt>
        <dd title={step.started_at ?? undefined}>{step.started_at ? `${when(step.started_at)} (${relative(step.started_at)})` : 'Not started'}</dd>
        <dt className="text-muted">Duration</dt>
        <dd>{duration(step.started_at, step.finished_at, now) || '–'}</dd>
      </dl>
      {step.error && (
        <p role="alert" className="break-words rounded-lg border border-red-300 bg-red-50 p-3 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300" data-testid="step-error">
          {step.error}
        </p>
      )}
      {counts.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted">Counts</h3>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1" data-testid="step-counts">
            {counts.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted">{countLabel(k)}</dt>
                <dd className="tabular-nums">{v}</dd>
              </div>
            ))}
          </dl>
        </section>
      )}
      <section>
        <div className="mb-1 flex items-center gap-2">
          <h3 className="flex-1 text-xs font-semibold uppercase tracking-wide text-muted">Log</h3>
          <Btn className="px-2 py-1 text-xs" disabled={!log} aria-label="Copy log" onClick={async () => (await copyText(log)) && toast('Copied')}>
            <Copy size={12} aria-hidden /> Copy
          </Btn>
        </div>
        <p className="mb-1 text-xs text-muted">Showing the last 256 KiB. Times are UTC.{step.log_dropped && ' Earlier lines were dropped.'}</p>
        {log ? (
          <pre
            ref={box}
            tabIndex={0}
            onScroll={(e) => (follow.current = atBottom(e.currentTarget))}
            className="max-h-[50dvh] min-h-24 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-bg-soft p-3 text-xs"
            data-testid="step-log"
          >
            {log}
          </pre>
        ) : (
          // the run payload carries no log: undefined means "not loaded yet", never "empty"
          <p className="text-muted" role={step.log === undefined && loadFailed ? 'alert' : undefined}>
            {step.log === undefined ? (loadFailed ? 'Could not load the log.' : 'Loading log…') : waiting ? 'No log lines yet.' : 'This step wrote no log.'}
          </p>
        )}
      </section>
    </div>
  )
}
