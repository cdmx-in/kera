// Admin > Pipelines > one pipeline: header, run history, the flow run view
// (a hand-built diagram: script steps, then one node per collection) and the
// step drawer with its log. Everything is read-only; the page polls while a run is open.
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useSearchParams, type NavigateFunction } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, CircleAlert, Copy, KeyRound, Pencil, RefreshCw, Trash2, TriangleAlert, type LucideIcon } from 'lucide-react'
import { Chip } from '@/features/workspace/ui'
import { copyText, cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { attempt, Badge, Btn, Card, Confirm, Loading, ManagedBadge, Page } from './ui'
import { CopyBlock, lateText, PipelineForm, pipelinesBase, StatusBadge, TokenDialog } from './Pipelines'
import { relative, when } from './settings/Integrations'
import {
  atBottom,
  countLabel,
  duration,
  everyText,
  failedStep,
  isStuck,
  logTail,
  mainCount,
  runCommand,
  runSummary,
  stateOf,
  triggerLabel,
  upToDate,
  useNow,
  usePipeline,
  usePipelineApi,
  useRun,
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

const noticeTone = {
  red: 'border-red-300 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300',
  amber: 'border-amber-300 bg-amber-50 text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300',
  accent: 'border-accent/30 bg-accent-soft',
  grey: 'border-line bg-surface-2/50 text-muted',
}

export function Notice({ tone, icon: Icon, actions, role, testId, children }: { tone: keyof typeof noticeTone; icon?: LucideIcon; actions?: ReactNode; role?: 'alert' | 'status'; testId?: string; children: ReactNode }) {
  return (
    <div role={role} data-testid={testId} className={cx('flex flex-wrap items-start gap-x-3 gap-y-2 rounded-xl border px-4 py-3 text-sm', noticeTone[tone])}>
      {Icon && <Icon size={16} className="mt-0.5 shrink-0" aria-hidden />}
      {/* the text keeps a readable width, so the actions wrap under it on a phone; text-fg because a Btn sets only its (app theme) background, and the tone colours follow the OS scheme */}
      <div className="min-w-[min(100%,14rem)] flex-1">{children}</div>
      {actions && <div className="text-fg">{actions}</div>}
    </div>
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

type Ask = 'rotate' | 'delete' | 'close' | 'allow' | null

function Loaded({ p, runParam }: { p: Pipeline; runParam?: string }) {
  const nav = useNavigate()
  const navState = useLocation().state
  const qc = useQueryClient()
  const pa = usePipelineApi()
  const [params] = useSearchParams()
  const [limit, setLimit] = useState(30)
  const [edit, setEdit] = useState(false)
  const [token, setToken] = useState<string | null>(null)
  const [ask, setAsk] = useState<Ask>(null)
  const runs = useRuns(p.name, limit)
  const runId = runParam !== undefined ? Number(runParam) : runs.data?.runs[0]?.id
  const noRun = runParam !== undefined && !Number.isInteger(runId)
  const rq = useRun(p.name, runId)
  const run = rq.data
  const now = useNow(run?.status === 'running' ? 1000 : 0)
  const state = stateOf(p)
  const stepParam = params.get('step')
  const seq = run && stepParam !== null && /^\d+$/.test(stepParam) ? Number(stepParam) : null
  const refused = run?.counts?.refused ?? 0
  const canAllow = !!run && run.id === p.last_run?.id && run.status !== 'running' && refused > 0 && !p.allow_mass_delete

  // when the open run closes, the list, the header and the history catch up at once
  const before = useRef<string>(undefined)
  useEffect(() => {
    if (before.current === 'running' && run && run.status !== 'running') void qc.invalidateQueries({ queryKey: ['admin', 'pipelines'] })
    before.current = run?.status
  }, [run, qc])

  return (
    <Page>
      <Back />
      <div className="flex flex-wrap items-start gap-3">
        {/* a readable width, so the buttons wrap under the title on a tablet instead of squeezing it */}
        <div className="min-w-[min(100%,18rem)] flex-1">
          <h2 className="flex flex-wrap items-center gap-2 break-words text-lg font-semibold tracking-tight">
            {p.title || p.name} <StatusBadge status={state} /> {p.managed && <ManagedBadge />}
          </h2>
          <p className="mt-0.5 text-[13px] text-muted">
            <code>{p.name}</code> · Owner {p.owner.name || p.owner.email} · {p.every_minutes > 0 ? `Expected every ${everyText(p.every_minutes)}` : 'No expected interval'}
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
          <Btn onClick={() => setEdit(true)} data-testid="pipeline-edit">
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

      {!p.enabled && <Notice tone="grey">This pipeline is switched off: Kera refuses its token. Edit it to switch it on.</Notice>}
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

      {p.last_run === null ? (
        <Card title="This pipeline has not run yet" hint="The first run shows up here as a flow of steps with their logs.">
          <div className="p-5" data-testid="never-ran">
            <HowTo p={p} />
          </div>
        </Card>
      ) : (
        <>
          <details className="rounded-xl border border-line bg-surface text-sm">
            <summary className="cursor-pointer select-none px-4 py-2.5 font-medium">How to run</summary>
            <div className="border-t border-line p-4">
              <HowTo p={p} />
            </div>
          </details>
          <div className="grid gap-4 lg:grid-cols-[18rem_minmax(0,1fr)] lg:items-start">
            <History runs={runs.data?.runs ?? []} loading={runs.isLoading} more={!!runs.data?.more} selected={run?.id ?? runId} name={p.name} now={now} onMore={() => setLimit((l) => Math.min(l + 30, 100))} />
            {run ? (
              <div className={cx('min-w-0', rq.isPlaceholderData && 'opacity-60')}>
                <RunView
                  p={p}
                  run={run}
                  seq={seq}
                  now={now}
                  onOpen={(s) => nav(urlOf(p.name, run.id, s), { state: { drawer: true } })}
                  onClose={() => setAsk('close')}
                  onAllow={canAllow ? () => setAsk('allow') : undefined}
                  refused={refused}
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
      {edit && <PipelineForm pipeline={p} onClose={() => setEdit(false)} onSaved={() => setEdit(false)} />}
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

export function History({ runs, loading, more, selected, name, now, onMore }: { runs: Run[]; loading: boolean; more: boolean; selected: number | undefined; name: string; now: number; onMore: () => void }) {
  const wide = useWide()
  const [open, setOpen] = useState(false)
  const list = (
    <nav aria-label="Run history" data-testid="run-history">
      {loading && !runs.length ? (
        <p className="px-5 py-4 text-sm text-muted">Loading runs…</p>
      ) : (
        <ul className="max-h-[70dvh] divide-y divide-line overflow-y-auto">
          {runs.map((r) => (
            <li key={r.id}>
              <Link
                to={urlOf(name, r.id)}
                onClick={() => setOpen(false)}
                aria-current={r.id === selected ? 'page' : undefined}
                data-testid="run-item"
                data-status={r.status}
                className={cx('block space-y-1 px-4 py-3 text-sm hover:bg-surface-2/60', r.id === selected && 'bg-accent-soft')}
              >
                <span className="flex flex-wrap items-center gap-1.5">
                  <StatusBadge status={r.status} />
                  {r.dry_run && <Badge tone="accent">Dry run</Badge>}
                  <span className="ml-auto text-xs text-muted">#{r.id}</span>
                </span>
                <span className="block text-xs text-muted" title={when(r.started_at)}>
                  {relative(r.started_at)} · {duration(r.started_at, r.finished_at, now)} · {triggerLabel(r.trigger)}
                </span>
                <span className="line-clamp-2 break-words text-xs" title={r.status === 'failed' ? r.error : undefined}>
                  {runSummary(r)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
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

function RunView({ p, run, seq, now, refused, onOpen, onClose, onAllow }: { p: Pipeline; run: Run; seq: number | null; now: number; refused: number; onOpen: (seq: number) => void; onClose: () => void; onAllow?: () => void }) {
  const steps = [...(run.steps ?? [])].sort((a, b) => a.seq - b.seq)
  const open = run.status === 'running'
  const blame = failedStep(steps)
  const current = upToDate(run.counts)
  return (
    <div className="space-y-4" data-testid="run-view" data-status={run.status}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 className="text-[15px] font-semibold">Run #{run.id}</h3>
        <StatusBadge status={run.status} />
        {run.dry_run && <Badge tone="accent">Dry run</Badge>}
        <span className="text-xs text-muted" title={when(run.started_at)}>
          Started {relative(run.started_at)} · {duration(run.started_at, run.finished_at, now)} · {triggerLabel(run.trigger)}
        </span>
      </div>

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
  running: 'border-accent',
  ok: 'border-line',
  warning: 'border-amber-400 dark:border-amber-700',
  failed: 'border-red-400 dark:border-red-700',
  skipped: 'border-dashed border-line text-muted',
}

function FlowNode({ s, on, now, onClick }: { s: Step; on: boolean; now: number; onClick: () => void }) {
  const count = mainCount(s)
  return (
    <button
      type="button"
      aria-pressed={on}
      data-testid="flow-node"
      data-status={s.status}
      data-kind={s.kera ? 'kera' : 'script'}
      onClick={onClick}
      className={cx(
        'flex w-full flex-col gap-1 sm:w-44 rounded-xl border bg-surface px-3 py-2 text-left text-sm outline-none hover:bg-surface-2/60 focus-visible:border-accent focus-visible:ring-3 focus-visible:ring-accent-soft aria-pressed:border-accent aria-pressed:ring-2 aria-pressed:ring-accent',
        ring[s.status],
      )}
    >
      <span className="line-clamp-2 break-words font-medium" title={s.name}>
        {s.name}
      </span>
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <StatusBadge status={s.status} />
        <span className="text-xs text-muted">{duration(s.started_at, s.finished_at, now)}</span>
      </span>
      <span className="min-h-4 truncate text-xs text-muted">{count}</span>
    </button>
  )
}

const Connector = () => (
  <span aria-hidden className="mx-1 hidden shrink-0 items-center text-muted sm:inline-flex">
    <span className="h-px w-3 bg-current opacity-50" />
    <ChevronRight size={14} />
  </span>
)

// ponytail: a linear chain plus a fan to the collections; add an `after` column and a layout when steps branch.
export function Flow({ steps, seq, now, onOpen }: { steps: Step[]; seq: number | null; now: number; onOpen: (seq: number) => void }) {
  const script = steps.filter((s) => !s.kera)
  const kera = steps.filter((s) => s.kera)
  const node = (s: Step) => <FlowNode s={s} on={s.seq === seq} now={now} onClick={() => onOpen(s.seq)} />
  return (
    <div data-testid="flow" className="flex flex-col gap-y-5 rounded-2xl border border-line bg-surface p-4 sm:flex-row sm:flex-wrap sm:items-center">
      {script.length > 0 && (
        <ol aria-label="Script steps" className="flex min-w-0 flex-col gap-y-3 sm:flex-row sm:flex-wrap sm:items-center">
          {script.map((s, i) => (
            <li key={s.seq} className="flex items-center sm:w-auto">
              {node(s)}
              {i < script.length - 1 && <Connector />}
            </li>
          ))}
        </ol>
      )}
      {kera.length > 0 && (
        <div
          role="group"
          aria-labelledby="flow-kera"
          className="relative flex min-w-0 flex-col gap-2 py-3 pl-5 sm:min-w-[min(100%,24rem)] sm:flex-1 before:absolute before:inset-y-0 before:left-0 before:w-2.5 before:rounded-l-lg before:border-y-2 before:border-l-2 before:border-line"
        >
          <p id="flow-kera" className="text-xs font-semibold uppercase tracking-wide text-muted">
            Kera
          </p>
          <ul className="grid grid-cols-[repeat(auto-fill,minmax(11rem,1fr))] gap-2">
            {kera.map((s) => (
              <li key={s.seq}>{node(s)}</li>
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
