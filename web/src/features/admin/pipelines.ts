// Admin > Pipelines: API shapes (internal/pipeline/http.go), queries, and the
// pure helpers behind the list and the flow run view (tested in pipelines.test.ts).
import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/api/client'
import { enc } from './api'
import { humanize } from './lib'
import type { Errors } from './settings/validate'

export type RunStatus = 'running' | 'ok' | 'warning' | 'failed'
export type StepStatus = 'pending' | 'running' | 'ok' | 'warning' | 'failed' | 'skipped'
/** What the list says about a pipeline: its last run, or why that is not the whole story. */
export type State = RunStatus | 'late' | 'never' | 'off'
export type Tone = 'grey' | 'accent' | 'red' | 'amber' | 'green'
export type Counts = Record<string, number>

export interface Step {
  seq: number
  /** a collection step (key = the collection id); the others are the script's */
  kera: boolean
  key: string
  name: string
  status: StepStatus
  started_at: string | null
  finished_at: string | null
  error: string
  counts: Counts
  log_dropped: boolean
  /** the step route only */
  log?: string
}

export interface Run {
  id: number
  pipeline: string
  trigger: string
  dry_run: boolean
  status: RunStatus
  client_done: boolean
  error: string
  started_at: string
  active_at: string
  finished_at: string | null
  /** the collection steps' counts added up */
  counts: Counts
  steps?: Step[]
}

export interface Target {
  id: string
  name: string
  /** the collection was deleted */
  missing: boolean
}

export interface Pipeline {
  id: string
  name: string
  title: string
  owner: { id: string; name: string; email: string }
  targets: Target[]
  every_minutes: number
  token_prefix: string
  adopt: boolean
  allow_mass_delete: boolean
  enabled: boolean
  managed: boolean
  created_at: string
  updated_at: string
  late: boolean
  running: boolean
  last_ok_at: string | null
  last_run: Run | null
}

// Pure helpers ------------------------------------------------------------------------

/** Off, Running, a failed last run, Late, then the last run's status; Never run when there is none. A failure outranks Late: a pipeline that fails every day is late too, and red must not turn amber. */
export function stateOf(p: Pick<Pipeline, 'enabled' | 'running' | 'late'> & { last_run: Pick<Run, 'status'> | null }): State {
  if (!p.enabled) return 'off'
  if (p.running) return 'running'
  if (p.last_run?.status === 'failed') return 'failed'
  if (p.late) return 'late'
  return p.last_run ? p.last_run.status : 'never'
}

export const statusLabel: Record<State | StepStatus, string> = {
  running: 'Running',
  ok: 'OK',
  warning: 'Warning',
  failed: 'Failed',
  late: 'Late',
  never: 'Never run',
  off: 'Off',
  pending: 'Pending',
  skipped: 'Skipped',
}

const tones: Record<State | StepStatus, Tone> = { running: 'accent', ok: 'green', warning: 'amber', late: 'amber', failed: 'red', pending: 'grey', skipped: 'grey', never: 'grey', off: 'grey' }
/** The Badge tone of a state or step status (the word and icon carry it too, never colour alone). */
export const toneOf = (s: State | StepStatus): Tone => tones[s]

const attention: State[] = ['failed', 'late', 'warning', 'running', 'never', 'ok', 'off']
/** Sort order for the list: what needs a look first, then by name. */
export const byAttention = (a: Pipeline, b: Pipeline) => attention.indexOf(stateOf(a)) - attention.indexOf(stateOf(b)) || a.name.localeCompare(b.name)

/** "12 s", "3 min 4 s", "1 h 5 min"; an open end runs to now. Empty without a start. */
export function duration(start: string | null | undefined, end: string | null | undefined, now = Date.now()): string {
  if (!start) return ''
  const s = Math.max(0, Math.round(((end ? new Date(end).getTime() : now) - new Date(start).getTime()) / 1000))
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  if (m < 60) return s % 60 ? `${m} min ${s % 60} s` : `${m} min`
  return m % 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m / 60} h`
}

/** "day", "7 days", "6 hours", "90 min": the tail of "expected every ...". */
export function everyText(min: number): string {
  if (min >= 1440 && min % 1440 === 0) return min === 1440 ? 'day' : `${min / 1440} days`
  if (min >= 60 && min % 60 === 0) return min === 60 ? 'hour' : `${min / 60} hours`
  return `${min} min`
}

const changeParts: [string, string][] = [
  ['uploaded', 'changed'],
  ['adopted', 'adopted'],
  ['removed', 'removed'],
  ['failed', 'failed'],
  ['refused', 'not removed'],
]

/** "4 changed, 1 removed"; "No changes" when nothing moved. */
export function changeSummary(c: Counts | null | undefined): string {
  const parts = changeParts.filter(([k]) => (c?.[k] ?? 0) > 0).map(([k, label]) => `${c![k]} ${label}`)
  return parts.join(', ') || 'No changes'
}

/** One line for a run in the list and the history: its error when it failed, else what moved. */
export const runSummary = (r: Pick<Run, 'status' | 'error' | 'counts'>): string => (r.status === 'running' ? 'In progress' : r.status === 'failed' && r.error ? r.error : changeSummary(r.counts))

/** How many files were already up to date when the run changed nothing; 0 otherwise. */
export const upToDate = (c: Counts | null | undefined): number => (changeSummary(c) === 'No changes' ? (c?.listed ?? 0) : 0)

const keraMain: [string, string][] = [
  ['failed', 'failed'],
  ['refused', 'not removed'],
  ['waiting', 'waiting'],
  ['uploaded', 'changed'],
  ['removed', 'removed'],
  ['adopted', 'adopted'],
  ['unchanged', 'unchanged'],
]

/** The one count a flow node shows: a collection's most telling number, or a script step's first count. */
export function mainCount(s: Pick<Step, 'kera' | 'counts'>): string {
  const c = s.counts ?? {}
  if (!s.kera) {
    const [k, v] = Object.entries(c)[0] ?? []
    return k ? `${v} ${k}` : ''
  }
  const hit = keraMain.find(([k]) => (c[k] ?? 0) > 0)
  return hit ? `${c[hit[0]]} ${hit[1]}` : ''
}

const chipDefs: [string, string, 'red' | 'amber' | 'green' | 'accent' | 'grey'][] = [
  ['failed', 'failed', 'red'],
  ['refused', 'not removed', 'amber'],
  ['waiting', 'waiting', 'accent'],
  ['uploaded', 'changed', 'green'],
  ['removed', 'removed', 'amber'],
  ['adopted', 'adopted', 'grey'],
  ['unchanged', 'unchanged', 'grey'],
]

/** The few counts a flow node shows as chips, most telling first (the first one is {@link mainCount}). */
export function countChips(s: Pick<Step, 'kera' | 'counts'>, max = 3): { text: string; tone: 'red' | 'amber' | 'green' | 'accent' | 'grey' }[] {
  const c = s.counts ?? {}
  if (!s.kera) return Object.entries(c).slice(0, max).map(([k, v]) => ({ text: `${v} ${k}`, tone: 'grey' as const }))
  return chipDefs.filter(([k]) => (c[k] ?? 0) > 0).slice(0, max).map(([k, label, tone]) => ({ text: `${c[k]} ${label}`, tone }))
}

export const countLabel = (k: string) => (k === 'refused' ? 'Removals refused' : humanize(k))

/** The step to blame: the first failed script step, else the first failed collection. */
export function failedStep(steps: Step[] | undefined): Step | undefined {
  return steps?.find((s) => !s.kera && s.status === 'failed') ?? steps?.find((s) => s.status === 'failed')
}

/** The last n non-empty lines of a log. */
export const logTail = (log: string | undefined, n = 10): string =>
  (log ?? '')
    .split('\n')
    .filter((l) => l.trim())
    .slice(-n)
    .join('\n')

/** Whether a scroll box is at (or within slack px of) its end: the log keeps following only then. */
export const atBottom = (el: { scrollHeight: number; scrollTop: number; clientHeight: number }, slack = 24) => el.scrollHeight - el.scrollTop - el.clientHeight <= slack

/** An open run whose script is silent for this long gets "Close run" (the server closes it itself after 30 minutes). */
export const stuckAfterMin = 5

// ponytail: no Close run once the script is done (client_done): Kera is only indexing behind a shared worker and closes the run itself after 30 minutes without progress. Offer it after 30 minutes if that wait proves too long.
export function isStuck(r: Pick<Run, 'status' | 'active_at' | 'client_done'>, now = Date.now()): boolean {
  return r.status === 'running' && !r.client_done && now - new Date(r.active_at).getTime() >= stuckAfterMin * 60_000
}

const triggers: Record<string, string> = { script: 'Script', push: 'Push' }
export const triggerLabel = (t: string) => triggers[t] ?? t.charAt(0).toUpperCase() + t.slice(1)

/** Single-quotes a word for a shell unless it is already plain. */
export const shq = (s: string) => (/^[\w./:@%+=,-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`)

/** The line that pushes a folder into the pipeline's first collection (token = the real one, or a placeholder). */
export function runCommand(p: Pick<Pipeline, 'name' | 'targets'>, origin: string, token = '<token>'): string {
  const t = p.targets.find((x) => !x.missing)
  return `KERA_URL=${shq(origin)} KERA_PIPELINE=${p.name} KERA_PIPELINE_TOKEN=${token} \\\nkera pipeline push${t ? ` --collection ${shq(t.name)}` : ''} --dir ./docs`
}

/** The env file a script or cron job reads (kera.env). */
export const envFile = (name: string, origin: string, token: string) => `KERA_URL=${origin}\nKERA_PIPELINE=${name}\nKERA_PIPELINE_TOKEN=${token}`

export const slugRe = /^[a-z0-9][a-z0-9-]{0,39}$/

/** Lines of a textarea: trimmed, empty ones dropped. */
export const lines = (s: string) => s.split('\n').map((l) => l.trim()).filter(Boolean)

/** Form rules, keyed by the field names the server's errors use. */
export function validatePipeline(f: { name: string; every: string; targets: string[]; fresh: string[] }, isNew: boolean): Errors {
  const e: Errors = {}
  if (isNew && !slugRe.test(f.name)) e.name = 'Use 1 to 40 lowercase letters, digits and dashes, starting with a letter or digit'
  const n = Number(f.every)
  if (f.every.trim() === '' || !Number.isInteger(n) || n < 0 || n > 525600) e.every_minutes = 'A whole number of minutes, 0 (no expectation) to 525600'
  if (!f.targets.length && !f.fresh.length) e.targets = 'Pick at least one collection, or name a new one'
  return e
}

// Hooks -------------------------------------------------------------------------------

const base = '/api/v1/admin/pipelines'
const key = (...k: (string | number)[]) => ['admin', 'pipelines', ...k]
/** Always refetch when a page mounts (the app default keeps data fresh for 30 s). */
const live = { staleTime: 0 }

export const usePipelines = () =>
  useQuery({ ...live, queryKey: key(), queryFn: () => api<Pipeline[]>(base), refetchInterval: (q) => (q.state.data?.some((p) => p.running) ? 10_000 : 60_000) })

export const usePipeline = (name: string) =>
  useQuery({ ...live, queryKey: key(name), queryFn: () => api<Pipeline>(`${base}/${enc(name)}`), refetchInterval: (q) => (q.state.data?.running ? 10_000 : 60_000) })

/** The newest `limit` runs; "Load older" raises the limit (no cursor, so a run that starts meanwhile cannot leave a gap). */
export const useRuns = (name: string, limit: number) =>
  useQuery({
    ...live,
    queryKey: key(name, 'runs', limit),
    queryFn: () => api<{ runs: Run[]; more: boolean }>(`${base}/${enc(name)}/runs?limit=${limit}`),
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (q.state.data?.runs.some((r) => r.status === 'running') ? 10_000 : 60_000),
  })

/** One run with its steps; 3 s while it is open. */
export const useRun = (name: string, id: number | undefined) =>
  useQuery({
    ...live,
    queryKey: key(name, 'run', id ?? 0),
    queryFn: () => api<Run>(`${base}/${enc(name)}/runs/${id}`),
    enabled: id != null && Number.isFinite(id),
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (q.state.data?.status === 'running' ? 3000 : false),
  })

/** One step with its log; 3 s while it can still change (the last fetch then shows its final state). */
export const useStep = (name: string, run: number | undefined, seq: number | undefined, enabled = true) =>
  useQuery({
    ...live,
    queryKey: key(name, 'run', run ?? 0, 'step', seq ?? 0),
    queryFn: () => api<Step>(`${base}/${enc(name)}/runs/${run}/steps/${seq}`),
    enabled: enabled && run != null && seq != null,
    refetchInterval: (q) => (q.state.data && (q.state.data.status === 'running' || q.state.data.status === 'pending') ? 3000 : false),
  })

/** Every write, each refreshing the pipeline queries. */
export function usePipelineApi() {
  const qc = useQueryClient()
  const call = async <T>(path: string, method: string, json?: unknown) => {
    const r = await api<T>(base + path, { method, json })
    await qc.invalidateQueries({ queryKey: key() })
    return r
  }
  return {
    create: (body: Record<string, unknown>) => call<{ pipeline: Pipeline; token: string }>('', 'POST', body),
    update: (name: string, body: Record<string, unknown>) => call<Pipeline>(`/${enc(name)}`, 'PUT', body),
    remove: (name: string) => call<{ ok: boolean }>(`/${enc(name)}`, 'DELETE'),
    rotate: (name: string) => call<{ token: string; token_prefix: string }>(`/${enc(name)}/token`, 'POST'),
    once: (name: string, flags: { adopt?: boolean; mass_delete?: boolean }) => call<Pipeline>(`/${enc(name)}/once`, 'POST', flags),
    close: (name: string, run: number) => call<Run>(`/${enc(name)}/runs/${run}/close`, 'POST'),
  }
}

/** Date.now(), refreshed every `ms` (0 = never) so running durations tick. */
export function useNow(ms: number): number {
  const [now, set] = useState(Date.now())
  useEffect(() => {
    if (!ms) return
    const t = setInterval(() => set(Date.now()), ms)
    return () => clearInterval(t)
  }, [ms])
  return now
}

/** Whether the viewport is at least `min` px wide (the lg breakpoint by default); true where there is no matchMedia. */
export function useWide(min = 1024): boolean {
  const q = `(min-width: ${min}px)`
  const [wide, set] = useState(() => typeof matchMedia !== 'function' || matchMedia(q).matches)
  useEffect(() => {
    if (typeof matchMedia !== 'function') return
    const m = matchMedia(q)
    const on = () => set(m.matches)
    on()
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [q])
  return wide
}
