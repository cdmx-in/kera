// Pure helpers behind the run view: the shared time axis of the step waterfall, the health strip, typical durations,
// progress segments and the one sentence a run is summed up in (tested in runview.test.ts).
import { duration, isNotebookRun, stageTitle, type Counts, type Run, type Stage, type Step, type StepStatus } from './pipelines'

const ms = (t: string | null | undefined): number | null => (t ? new Date(t).getTime() : null)

/** Wall-clock length of a run (an open one runs to `now`); 0 when the times are missing. */
export function runMs(r: Pick<Run, 'started_at' | 'finished_at'>, now: number): number {
  const a = ms(r.started_at)
  return a === null ? 0 : Math.max(0, (ms(r.finished_at) ?? now) - a)
}

/** Waits shorter than this are not worth a line in the run header. */
const startDelayMin = 5_000

/** "Waited 42 s to start": how long a Kera-run run sat queued before a runner took it, when that was 5 s or more; null otherwise (or while it is still queued). */
export function waitedText(r: Pick<Run, 'started_at' | 'claimed_at'>): string | null {
  const a = ms(r.started_at)
  const b = ms(r.claimed_at)
  return a === null || b === null || b - a < startDelayMin ? null : `Waited ${duration(r.started_at, r.claimed_at)} to start`
}

/** Length of a step: 0 for one that never started; an open one runs to `now`. */
export function stepMs(s: Pick<Step, 'started_at' | 'finished_at'>, now: number): number {
  const a = ms(s.started_at)
  return a === null ? 0 : Math.max(0, (ms(s.finished_at) ?? now) - a)
}

export function median(xs: number[]): number | null {
  if (!xs.length) return null
  const s = [...xs].sort((a, b) => a - b)
  const m = s.length >> 1
  return s.length % 2 ? s[m] : Math.round((s[m - 1] + s[m]) / 2)
}

/** A run a pipeline's own history is made of: not a dry run and not a notebook session. */
const real = (r: Run): boolean => !r.dry_run && !isNotebookRun(r)

/** The usual length of a good run: the median of the finished OK runs among `runs` (newest first); null with fewer than 3 to go by. */
export function typicalMs(runs: Run[], take = 20): number | null {
  const good = runs
    .filter((r) => r.status === 'ok' && r.finished_at && real(r))
    .slice(0, take)
    .map((r) => runMs(r, 0))
  return good.length >= 3 ? median(good) : null
}

/** The newest finished OK run other than `except`: the one whose steps tell what each step usually takes. */
export const baselineRun = (runs: Run[], except: number | undefined): Run | undefined => runs.find((r) => r.id !== except && r.status === 'ok' && r.finished_at && real(r))

/** What each step name took in the baseline run, in ms. A name that ran twice keeps the longer one. */
export function usualSteps(steps: Step[] | undefined): Record<string, number> {
  const out: Record<string, number> = {}
  for (const s of steps ?? []) if (s.status === 'ok' && s.started_at && s.finished_at) out[s.name] = Math.max(out[s.name] ?? 0, stepMs(s, 0))
  return out
}

/** Whether a step took clearly longer than usual: at least 1.5 times and 10 s more. */
export const slowerThanUsual = (took: number, usual: number | undefined): boolean => usual !== undefined && usual > 0 && took >= usual * 1.5 && took - usual >= 10_000

// Health strip --------------------------------------------------------------------------------

export type TickStatus = 'ok' | 'warning' | 'failed' | 'running' | 'queued' | 'cancelled'

export interface Tick {
  id: number
  status: TickStatus
  dry: boolean
  notebook: boolean
  ms: number
  /** 0.18 to 1: the tick's height, the longest run = 1 */
  h: number
  startedAt: string
  trigger: string
}

export const isCancelled = (r: Pick<Run, 'status' | 'error'>): boolean => r.status === 'failed' && /^Cancelled by an admin/i.test(r.error)
export const isClosedByAdmin = (r: Pick<Run, 'status' | 'error'>): boolean => r.status === 'failed' && /^Closed by an admin/i.test(r.error)

/** The last `n` runs (given newest first) as ticks, oldest first so the newest sits at the right. */
export function healthTicks(runs: Run[], now: number, n = 50): Tick[] {
  const list = runs.slice(0, n).reverse()
  const lens = list.map((r) => runMs(r, now))
  const top = Math.max(1, ...lens)
  return list.map((r, i) => ({
    id: r.id,
    status: r.status === 'running' ? (r.queued ? 'queued' : 'running') : isCancelled(r) ? 'cancelled' : r.status,
    dry: r.dry_run,
    notebook: isNotebookRun(r),
    ms: lens[i],
    // a failed run is often a short one: it keeps at least 45% of the height so it is not lost between its tall neighbours
    h: r.status === 'running' && r.queued ? 0.3 : Math.max(r.status === 'failed' ? 0.45 : 0.18, Math.min(1, lens[i] / top)),
    startedAt: r.started_at,
    trigger: r.trigger,
  }))
}

export interface Health {
  /** finished runs among the ones counted */
  total: number
  ok: number
  warning: number
  failed: number
  median: number | null
}

/** "46 of 50 ok": over the last `n` runs that have finished (an open run has no verdict yet). */
export function healthSummary(runs: Run[], n = 50): Health {
  const done = runs.slice(0, n).filter((r) => r.status !== 'running')
  return {
    total: done.length,
    ok: done.filter((r) => r.status === 'ok').length,
    warning: done.filter((r) => r.status === 'warning').length,
    failed: done.filter((r) => r.status === 'failed').length,
    median: median(done.filter((r) => r.status === 'ok' && r.finished_at).map((r) => runMs(r, 0))),
  }
}

// Time axis ------------------------------------------------------------------------------------

/**
 * The length of the shared axis in ms. A finished run fills it exactly; an open one gets a little air, and at least the usual length,
 * so the bars do not rescale on every tick of the clock while the run is on schedule.
 */
export function axisSpan(run: Pick<Run, 'started_at' | 'finished_at'>, steps: Pick<Step, 'started_at' | 'finished_at'>[], now: number, typical: number | null = null): number {
  const t0 = ms(run.started_at) ?? now
  let end = ms(run.finished_at) ?? now
  for (const s of steps) if (ms(s.started_at) !== null) end = Math.max(end, ms(s.finished_at) ?? now)
  const base = Math.max(1000, end - t0)
  if (run.finished_at) return base
  return Math.max(base * 1.06, typical ? typical * 1.08 : 0)
}

export interface Bar {
  /** percent of the axis */
  left: number
  width: number
}

/** Where a step's bar sits on the axis; null for a step that never started. A step of 0 s still gets a sliver so it can be seen. */
export function barOf(step: Pick<Step, 'started_at' | 'finished_at'>, t0: number, span: number, now: number, minWidth = 0.4): Bar | null {
  const a = ms(step.started_at)
  if (a === null) return null
  const b = ms(step.finished_at) ?? now
  const left = Math.min(100 - minWidth, Math.max(0, ((a - t0) / span) * 100))
  const width = Math.min(100 - left, Math.max(minWidth, ((b - a) / span) * 100))
  return { left, width }
}

const NICE = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14400, 21600, 43200, 86400].map((s) => s * 1000)

/** Gridline positions: a round interval (5 s, 30 s, 2 min, 1 h...) giving at most `max` marks, as ms from the start and as a share of the axis. */
export function axisTicks(span: number, max = 6): { step: number; marks: { ms: number; pct: number }[] } {
  const step = NICE.find((n) => span / n <= max) ?? NICE[NICE.length - 1]
  const marks: { ms: number; pct: number }[] = []
  for (let t = 0; t <= span; t += step) marks.push({ ms: t, pct: (t / span) * 100 })
  return { step, marks }
}

/** "0", "30 s", "2 min", "1 h 30 min" for an axis label. */
export function axisLabel(t: number): string {
  return t === 0 ? '0' : duration('1970-01-01T00:00:00Z', new Date(t).toISOString())
}

/** "06:57" or "1:02:15": the big clock of a run. */
export function clock(t: number): string {
  const s = Math.max(0, Math.floor(t / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const p = (n: number) => String(n).padStart(2, '0')
  return h ? `${h}:${p(m)}:${p(s % 60)}` : `${p(m)}:${p(s % 60)}`
}

// Steps in order, progress, collections ------------------------------------------------------------

/** The script's steps in order, with the implicit "start" first whatever order an older run recorded it in. */
export function scriptSteps(steps: Step[]): Step[] {
  const plain = steps.filter((s) => !s.kera).sort((a, b) => a.seq - b.seq)
  return [...plain.filter((s) => s.name === 'start'), ...plain.filter((s) => s.name !== 'start')]
}

export const keraSteps = (steps: Step[]): Step[] => steps.filter((s) => s.kera).sort((a, b) => a.seq - b.seq)

/** Script steps, then collections: the order of the progress rail. */
export const orderedSteps = (steps: Step[]): Step[] => [...scriptSteps(steps), ...keraSteps(steps)]

/** One segment per step for the progress rail. */
export const segments = (steps: Step[]): { seq: number; status: StepStatus; kera: boolean; name: string }[] => orderedSteps(steps).map((s) => ({ seq: s.seq, status: s.status, kera: s.kera, name: s.name }))

/** How many steps are over (ok, warning, failed or skipped) out of how many there are, and how many of those never ran. */
export function stepsDone(steps: Step[]): { done: number; skipped: number; total: number } {
  const skipped = steps.filter((s) => s.status === 'skipped').length
  return { done: steps.filter((s) => s.status === 'ok' || s.status === 'warning' || s.status === 'failed').length + skipped, skipped, total: steps.length }
}

/** Whether a step's panel is open until the reader decides: the running one (it follows its log), a failed one and the one a ?step= link points at. */
export const opensByDefault = (s: Pick<Step, 'status' | 'seq'>, focus: number | null): boolean => s.status === 'running' || s.status === 'failed' || s.seq === focus

/** The step the run is busy with: the first one running, else undefined. */
export const runningStep = (steps: Step[]): Step | undefined => orderedSteps(steps).find((s) => s.status === 'running')

/** The segments of a collection's bar, in the order they are drawn; zero segments are left out. `listed` is the folder's size, so it is not a segment. */
export const syncParts: { key: string; label: string; tone: 'idle' | 'ok' | 'accent' | 'warn' | 'bad' }[] = [
  { key: 'unchanged', label: 'unchanged', tone: 'idle' },
  { key: 'uploaded', label: 'changed', tone: 'ok' },
  { key: 'adopted', label: 'adopted', tone: 'accent' },
  { key: 'removed', label: 'removed', tone: 'warn' },
  { key: 'failed', label: 'failed', tone: 'bad' },
]

export function syncSegments(c: Counts | null | undefined): { key: string; label: string; tone: (typeof syncParts)[number]['tone']; n: number; pct: number }[] {
  const parts = syncParts.map((p) => ({ ...p, n: c?.[p.key] ?? 0 })).filter((p) => p.n > 0)
  const sum = parts.reduce((a, p) => a + p.n, 0)
  return parts.map((p) => ({ ...p, pct: sum ? (p.n / sum) * 100 : 0 }))
}

/** Nothing moved in this collection: no change of any kind and nothing waiting to be indexed. */
export const syncQuiet = (c: Counts | null | undefined): boolean => !((c?.uploaded ?? 0) + (c?.adopted ?? 0) + (c?.removed ?? 0) + (c?.failed ?? 0) + (c?.waiting ?? 0) + (c?.refused ?? 0))

// The sentence ---------------------------------------------------------------------------------

/** The first line of a text, cut at `max` characters with an ellipsis. */
export function firstLine(text: string, max = 90): string {
  const l = text.split('\n')[0].trim()
  if (l.length <= max) return l
  const cut = l.slice(0, max - 1)
  const space = cut.lastIndexOf(' ')
  // end on a word when there is one near the end
  return `${(space > max * 0.6 ? cut.slice(0, space) : cut).trimEnd()}…`
}

/**
 * The one sentence a run is summed up in: "Running · step 4 · Fetch iTop", "Failed at Fetch iTop · exit code 1",
 * "Queued · waiting for a runner", "Finished OK in 9 min 12 s".
 */
export function runSentence(run: Run, steps: Step[], stages: Stage[] | undefined, now: number): string {
  const t = (s: Step) => stageTitle(s, stages)
  const took = duration(run.started_at, run.finished_at, now)
  if (run.status === 'running') {
    if (run.queued) return 'Queued · waiting for a runner'
    if (run.cancel_requested) return 'Stopping · the runner is stopping the script'
    const cur = runningStep(steps)
    const plain = scriptSteps(steps)
    const coll = keraSteps(steps)
    if (cur?.kera) return `Running · syncing collection ${coll.indexOf(cur) + 1} of ${coll.length} · ${cur.name}`
    if (cur) {
      const n = plain.indexOf(cur) + 1
      const more = plain.some((s) => s.status === 'pending')
      return `Running · step ${n}${more ? ` of ${plain.length}` : ''} · ${t(cur)}`
    }
    const waiting = run.counts?.waiting ?? 0
    if (run.client_done && waiting > 0) return `Running · Kera is indexing · ${waiting} ${waiting === 1 ? 'file' : 'files'} waiting`
    if (!steps.some((s) => s.status !== 'pending')) return 'Running · waiting for the first step'
    return `Running · ${stepsDone(steps).done} of ${steps.length} steps done`
  }
  if (isCancelled(run)) return `Cancelled by an admin after ${took}`
  if (run.status === 'failed') {
    if (isClosedByAdmin(run)) return run.error.replace(/\.$/, '')
    const bad = steps.find((s) => !s.kera && s.status === 'failed') ?? steps.find((s) => s.status === 'failed')
    const why = firstLine(bad?.error || run.error, 60)
    if (bad) return `Failed at ${t(bad)}${why ? ` · ${why}` : ''}`
    return why ? `Failed · ${why}` : 'Failed'
  }
  if (run.status === 'warning') return `Finished with a warning in ${took}`
  return `${run.dry_run ? 'Dry run finished' : 'Finished OK'} in ${took}`
}

/** The run's status as its chip shows it: Queued and Stopping are states of an open run, Cancelled of a failed one. */
export type RunChip = 'queued' | 'running' | 'stopping' | 'ok' | 'warning' | 'failed' | 'cancelled'
export function runChip(run: Pick<Run, 'status' | 'queued' | 'cancel_requested' | 'error'>): RunChip {
  if (run.status === 'running') return run.queued ? 'queued' : run.cancel_requested ? 'stopping' : 'running'
  return isCancelled(run) ? 'cancelled' : run.status
}
