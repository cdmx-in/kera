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
export type State = RunStatus | 'late' | 'never' | 'off' | 'queued'
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
  /** schedule | manual (Kera-run), script | push (started from outside) */
  trigger: string
  dry_run: boolean
  status: RunStatus
  client_done: boolean
  error: string
  /** where the script runs, as it reported when it opened the run (host, user, dir, command, client); empty for older runs */
  source?: Partial<Record<'host' | 'user' | 'dir' | 'command' | 'client', string>>
  /** when the run was opened; for a Kera-run run, when it was queued */
  started_at: string
  /** Kera-run: when a runner took the run; null while it is queued and for a run started from outside */
  claimed_at?: string | null
  active_at: string
  finished_at: string | null
  /** Kera-run: opened by the timetable or Run now, waiting for a runner to claim it (status is still running) */
  queued: boolean
  /** an admin asked to stop it; the runner kills the script at its next check */
  cancel_requested: boolean
  /** the script version a Kera-run run used */
  script_version?: number
  /** the id of the runner that claimed it */
  runner?: string
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

export type RunMode = 'external' | 'kera'
export type Frequency = 'hourly' | 'daily' | 'weekdays' | 'weekly'

/** The timetable of a Kera-run pipeline. at_minute: minutes after local midnight; for hourly the minute of the hour. weekday: 0 = Sunday. */
export interface Schedule {
  scheduled: boolean
  frequency: Frequency
  at_minute: number
  weekday: number
  timezone: string
  next_run_at: string | null
}

/** What the admin says about a step the script reports: a title and a line of description. Both may be empty. */
export interface Stage {
  /** the step name the script reports */
  name: string
  title: string
  about: string
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
  /** the pipeline token may read the variables, for a script that runs outside Kera */
  env_by_token: boolean
  created_at: string
  updated_at: string
  late: boolean
  running: boolean
  /** external: a cron job, CI or script starts the runs; kera: the script and timetable live in Kera */
  run_mode: RunMode
  schedule: Schedule
  last_ok_at: string | null
  last_run: Run | null
  /** the catalog of step titles and descriptions; always an array */
  stages: Stage[]
}

// Pure helpers ------------------------------------------------------------------------

/** Off, Queued or Running, a failed last run, Late, then the last run's status; Never run when there is none. A failure outranks Late: a pipeline that fails every day is late too, and red must not turn amber. */
export function stateOf(p: Pick<Pipeline, 'enabled' | 'running' | 'late'> & { last_run: (Pick<Run, 'status'> & { queued?: boolean }) | null }): State {
  if (!p.enabled) return 'off'
  if (p.running) return p.last_run?.queued ? 'queued' : 'running'
  if (p.last_run?.status === 'failed') return 'failed'
  if (p.late) return 'late'
  return p.last_run ? p.last_run.status : 'never'
}

export const statusLabel: Record<State | StepStatus, string> = {
  running: 'Running',
  queued: 'Queued',
  ok: 'OK',
  warning: 'Warning',
  failed: 'Failed',
  late: 'Late',
  never: 'Never run',
  off: 'Off',
  pending: 'Pending',
  skipped: 'Skipped',
}

const tones: Record<State | StepStatus, Tone> = { running: 'accent', queued: 'grey', ok: 'green', warning: 'amber', late: 'amber', failed: 'red', pending: 'grey', skipped: 'grey', never: 'grey', off: 'grey' }
/** The Badge tone of a state or step status (the word and icon carry it too, never colour alone). */
export const toneOf = (s: State | StepStatus): Tone => tones[s]

const attention: State[] = ['failed', 'late', 'warning', 'running', 'queued', 'never', 'ok', 'off']
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

export const queuedText = 'Queued: waiting for a runner'

/** What a run shows as its status: an open run that no runner has claimed yet is Queued, not Running. */
export const runStatus = (r: Pick<Run, 'status'> & { queued?: boolean }): State | StepStatus => (r.status === 'running' && r.queued ? 'queued' : r.status)

/** One line for a run in the list and the history: its error when it failed, else what moved. */
export const runSummary = (r: Pick<Run, 'status' | 'error' | 'counts'> & { queued?: boolean; cancel_requested?: boolean }): string =>
  r.status === 'running' ? (r.queued ? queuedText : r.cancel_requested ? 'Stopping…' : 'In progress') : r.status === 'failed' && r.error ? r.error : changeSummary(r.counts)

/** Cancel is offered for a Kera-run run that is still open (queued, or claimed by a runner, which records the script version); a run a script reports itself cannot be stopped from here. */
export const cancellable = (r: Pick<Run, 'status'> & { queued?: boolean; script_version?: number }): boolean => r.status === 'running' && (!!r.queued || (r.script_version ?? 0) > 0)

/** "script v3", or nothing for a run that did not come from a stored script. */
export const scriptLabel = (v: number | undefined): string => (v ? `script v${v}` : '')

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
export function isStuck(r: Pick<Run, 'status' | 'active_at' | 'client_done'> & { queued?: boolean }, now = Date.now()): boolean {
  // a queued run has reported nothing because no runner has it yet: that is the queue's business, not a stuck script
  return r.status === 'running' && !r.queued && !r.client_done && now - new Date(r.active_at).getTime() >= stuckAfterMin * 60_000
}

const triggers: Record<string, string> = { schedule: 'Schedule', manual: 'Manual', script: 'Script', push: 'Push', test: 'Notebook' }
/** A run behind a notebook session (Test, Run all): it is dry, and is not the pipeline's last run. */
export const isNotebookRun = (r: Pick<Run, 'trigger'>): boolean => r.trigger === 'test'
export const triggerLabel = (t: string) => triggers[t] ?? t.charAt(0).toUpperCase() + t.slice(1)

// Stages: titles and descriptions of the steps ----------------------------------------------

/** Whole words kept in their own spelling when a step name is turned into a title. Tiny on purpose; the catalog is the place for the rest. */
const spelled: Record<string, string> = { id: 'ID', url: 'URL', api: 'API', qa: 'QA', itop: 'iTop', sharepoint: 'SharePoint', kb: 'KB', ai: 'AI', ci: 'CI', hr: 'HR', db: 'DB', sql: 'SQL', csv: 'CSV', pdf: 'PDF', json: 'JSON' }

/** "fetch-itop" -> "Fetch iTop", "secret_scan" -> "Secret scan"; a name that already has spaces or capitals only gets its first letter raised. */
export function sentenceCase(name: string): string {
  const raw = name.trim()
  if (!raw) return ''
  const parts = raw.split(/[-_\s]+/).filter(Boolean)
  if (/[A-Z\s]/.test(raw)) return parts.join(' ').replace(/^./u, (c) => c.toUpperCase())
  return parts.map((w, i) => spelled[w] ?? (i === 0 ? w.replace(/^./u, (c) => c.toUpperCase()) : w)).join(' ')
}

const stageOf = (name: string, stages: Stage[] | undefined) => stages?.find((s) => s.name === name)

/** The catalog title of a step, else its name in sentence case. A collection step keeps the collection's own name. */
export function stageTitle(step: Pick<Step, 'name' | 'kera'>, stages?: Stage[]): string {
  if (step.kera) return step.name
  return stageOf(step.name, stages)?.title.trim() || sentenceCase(step.name)
}

export const keraAbout = 'Kera uploads the changed files to this collection and indexes them'
export const startAbout = 'Everything the script printed before its first step'

/** The catalog description of a step; a collection and the implicit start step have a generic one; empty when there is none. */
export function stageAbout(step: Pick<Step, 'name' | 'kera'>, stages?: Stage[]): string {
  const own = (!step.kera && stageOf(step.name, stages)?.about.trim()) || ''
  if (own) return own
  if (step.kera) return keraAbout
  return step.name === 'start' ? startAbout : ''
}

/** Whether the step has no description of its own to show (the generic ones count as having one). */
export const lacksAbout = (step: Pick<Step, 'name' | 'kera'>, stages?: Stage[]): boolean => stageAbout(step, stages) === ''

export const stageLimits = { max: 200, name: 100, title: 80, about: 400 }

/** Rules for the stage list, keyed `stages` like the server's errors. */
export function validateStages(list: Stage[]): Errors {
  const e: Errors = {}
  if (list.length > stageLimits.max) e.stages = `At most ${stageLimits.max} stages`
  else if (list.some((s) => s.title.length > stageLimits.title || s.about.length > stageLimits.about)) e.stages = `A title is at most ${stageLimits.title} characters and a description at most ${stageLimits.about}`
  else if (list.some((s) => !s.name.trim() || s.name.length > stageLimits.name)) e.stages = 'A stage name is 1 to 100 characters: the step name the script reports'
  return e
}

/** One script step as the newest run that got to it reported it; pending or skipped means declared, but no saved run reached it. */
export interface ReportedStep {
  name: string
  run: number
  /** its place in that run, for the link to its log */
  seq: number
  status: StepStatus
  started_at: string | null
  finished_at: string | null
  counts: Counts
  /** where the script says it starts the step, e.g. "worker.py:42 main()"; may be empty */
  code_at: string
}

export const stepRan = (s: Pick<ReportedStep, 'status'>) => s.status !== 'pending' && s.status !== 'skipped'

/**
 * The labels as the editor lists them: those of reported steps first, in run order, then those no saved run knows, in
 * saved order. `place` numbers the listed reported ones 1, 2, 3: a reported step without a label (say the `start` of
 * an old run) leaves no gap. With nothing loaded they stay as saved.
 */
export function stageRows(stages: Stage[], reported: ReportedStep[] | undefined): { stage: Stage; step?: ReportedStep; place?: number }[] {
  const at = (name: string) => {
    const i = reported?.findIndex((r) => r.name === name) ?? -1
    return i < 0 ? Number.MAX_SAFE_INTEGER : i
  }
  return [...stages]
    .sort((a, b) => at(a.name) - at(b.name))
    .map((stage, i) => {
      const step = reported?.[at(stage.name)]
      return step ? { stage, step, place: i + 1 } : { stage }
    })
}

/** Whether two stage lists differ (an untouched list is not sent again). */
export const stagesChanged = (a: Stage[], b: Stage[]): boolean => a.length !== b.length || a.some((s, i) => s.name !== b[i].name || s.title !== b[i].title || s.about !== b[i].about)

/** Single-quotes a word for a shell unless it is already plain. */
export const shq = (s: string) => (/^[\w./:@%+=,-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`)

/** The line that pushes a folder into the pipeline's first collection (token = the real one, or a placeholder). */
export function runCommand(p: Pick<Pipeline, 'name' | 'targets'>, origin: string, token = '<token>'): string {
  const t = p.targets.find((x) => !x.missing)
  return `KERA_URL=${shq(origin)} KERA_PIPELINE=${p.name} KERA_PIPELINE_TOKEN=${token} \\\nkera pipeline push${t ? ` --collection ${shq(t.name)}` : ''} --dir ./docs`
}

/** The docker command that pushes ./docs. A container cannot reach the host's localhost, so for a loopback address it is pointed at the host instead. */
export function dockerPush(p: Pick<Pipeline, 'targets'>, origin: string): string {
  const u = new URL(origin)
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(u.hostname)
  const host = loopback ? ` \\\n  --add-host host.docker.internal:host-gateway -e KERA_URL=${u.protocol}//host.docker.internal${u.port ? `:${u.port}` : ''}` : ''
  return `docker run --rm --env-file kera.env${host} -v "$PWD/docs:/docs:ro" \\\n  ghcr.io/cdmx-in/kera pipeline push${p.targets[0] ? ` --collection ${shq(p.targets[0].name)}` : ''} --dir /docs`
}

/** The env file a script or cron job reads (kera.env). */
export const envFile = (name: string, origin: string, token: string) => `KERA_URL=${origin}\nKERA_PIPELINE=${name}\nKERA_PIPELINE_TOKEN=${token}`

export const slugRe = /^[a-z0-9][a-z0-9-]{0,39}$/

/** Lines of a textarea: trimmed, empty ones dropped. */
export const lines = (s: string) => s.split('\n').map((l) => l.trim()).filter(Boolean)

/** Form rules, keyed by the field names the server's errors use. A Kera-run pipeline's expected interval follows its timetable, so it is not checked. */
export function validatePipeline(f: { name: string; every: string; targets: string[]; fresh: string[]; mode?: RunMode }, isNew: boolean): Errors {
  const e: Errors = {}
  if (isNew && !slugRe.test(f.name)) e.name = 'Use 1 to 40 lowercase letters, digits and dashes, starting with a letter or digit'
  const n = Number(f.every)
  if (f.mode !== 'kera' && (f.every.trim() === '' || !Number.isInteger(n) || n < 0 || n > 525600)) e.every_minutes = 'A whole number of minutes, 0 (no expectation) to 525600'
  // no collection is fine: a pipeline that only runs a script or reports runs (alert-ai) writes to none
  return e
}

// Timetable -------------------------------------------------------------------------------

export const weekdays = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
export const frequencies: [Frequency, string][] = [
  ['hourly', 'Every hour'],
  ['daily', 'Every day'],
  ['weekdays', 'Every weekday (Mon–Fri)'],
  ['weekly', 'Every week'],
]

/** "09:30" for 570 minutes after midnight. */
export const hhmm = (at: number): string => `${String(Math.floor(at / 60)).padStart(2, '0')}:${String(at % 60).padStart(2, '0')}`

/** Minutes after midnight for "9:30" or "09:30"; null when it is not a time of day. */
export function parseTime(s: string): number | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(s.trim())
  if (!m || Number(m[1]) > 23 || Number(m[2]) > 59) return null
  return Number(m[1]) * 60 + Number(m[2])
}

/** The browser's own time zone, the default for a new timetable. */
export const browserZone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** Whether the runtime knows this IANA name (the server checks again; Go also refuses "Local"). */
export function validZone(tz: string): boolean {
  const t = tz.trim()
  if (!t || t === 'Local') return false
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: t })
    return true
  } catch {
    return false
  }
}

/** "Every day at 09:30 (Asia/Kolkata)", "Every Monday at 06:15 (UTC)", "Every hour at minute 15". */
export function scheduleSummary(s: Pick<Schedule, 'frequency' | 'at_minute' | 'weekday' | 'timezone'>): string {
  const at = `at ${hhmm(s.at_minute)} (${s.timezone})`
  switch (s.frequency) {
    case 'hourly':
      return `Every hour at minute ${s.at_minute}`
    case 'weekdays':
      return `Every weekday ${at}`
    case 'weekly':
      return `Every ${weekdays[s.weekday] ?? 'week'} ${at}`
  }
  return `Every day ${at}`
}

/** The timetable part of the edit form, as typed (strings) before it is checked. */
export interface ScheduleForm {
  mode: RunMode
  scheduled: boolean
  frequency: Frequency
  /** HH:MM, for every frequency but hourly */
  time: string
  /** minute of the hour, for hourly */
  minute: string
  weekday: number
  timezone: string
}

/** The form's start: the saved timetable of a Kera-run pipeline; for any other the stored values with the browser's time zone (the stored one is only the server default). */
export function scheduleFormOf(p: Pick<Pipeline, 'run_mode' | 'schedule'> | null, zone: string): ScheduleForm {
  const s = p?.schedule
  const kera = p?.run_mode === 'kera'
  const at = s?.at_minute ?? 0
  return { mode: kera ? 'kera' : 'external', scheduled: kera && !!s?.scheduled, frequency: s?.frequency ?? 'daily', time: hhmm(at), minute: String(at % 60), weekday: s?.weekday ?? 1, timezone: kera && s ? s.timezone : zone }
}

/** at_minute as the server stores it, or null when the typed time is not valid. */
export const atMinuteOf = (f: Pick<ScheduleForm, 'frequency' | 'time' | 'minute'>): number | null => {
  if (f.frequency !== 'hourly') return parseTime(f.time)
  const n = Number(f.minute)
  return f.minute.trim() !== '' && Number.isInteger(n) && n >= 0 && n <= 59 ? n : null
}

/** Rules for the timetable, keyed by the server's field names. Only a Kera-run pipeline has one. */
export function validateSchedule(f: ScheduleForm): Errors {
  const e: Errors = {}
  if (f.mode !== 'kera') return e
  if (atMinuteOf(f) === null) e.at_minute = f.frequency === 'hourly' ? 'A minute from 0 to 59' : 'Choose a time of day'
  if (!validZone(f.timezone)) e.timezone = 'Use a time zone name such as Asia/Kolkata or UTC'
  return e
}

/** The body of PUT /schedule. A pipeline started from outside is never scheduled. */
export const scheduleBody = (f: ScheduleForm) => ({
  run_mode: f.mode,
  scheduled: f.mode === 'kera' && f.scheduled,
  frequency: f.frequency,
  at_minute: atMinuteOf(f) ?? 0,
  weekday: f.weekday,
  timezone: f.timezone.trim(),
})

/** Whether saving must send the timetable: every save recomputes the next run, so an untouched one is left alone. */
export function scheduleChanged(p: Pick<Pipeline, 'run_mode' | 'schedule'> | null, f: ScheduleForm): boolean {
  if (f.mode === 'external') return p?.run_mode === 'kera'
  if (!p || p.run_mode !== 'kera') return true
  const b = scheduleBody(f)
  const s = p.schedule
  return s.scheduled !== b.scheduled || s.frequency !== b.frequency || s.at_minute !== b.at_minute || s.weekday !== b.weekday || s.timezone !== b.timezone
}

// Script ----------------------------------------------------------------------------------

export interface Script {
  version: number
  files: Record<string, string>
  entry: string
  args: string
  note: string
  created_by: string
  created_at: string
}

export interface ScriptVersion {
  version: number
  note: string
  created_by: string
  created_at: string
}

/** GET .../script: the newest version in full (null before the first save) and every kept version, newest first (null, not [], when there are none). */
export interface ScriptList {
  current: Script | null
  versions: ScriptVersion[] | null
}

/** What the editor holds: the files, the entry and the arguments of one version, plus the note for the next save. */
export interface Draft {
  files: Record<string, string>
  entry: string
  args: string
  note: string
}

export const draftOf = (s: Script | null | undefined): Draft => ({ files: { ...(s?.files ?? {}) }, entry: s?.entry ?? '', args: s?.args ?? '', note: '' })

/** The runner starts .sh and .bash with bash and .py with python3; anything else fails the run at start. */
export const startable = (name: string): boolean => /\.(sh|bash|py)$/i.test(name)

/** Whether the draft differs from what is saved (the note does not count: it is only the label of the next save). */
export function draftChanged(saved: Script | null | undefined, d: Draft): boolean {
  const a = Object.keys(d.files)
  if (!saved) return a.length > 0 || d.entry !== '' || d.args.trim() !== ''
  return d.entry !== saved.entry || d.args.trim() !== saved.args || a.length !== Object.keys(saved.files).length || a.some((n) => saved.files[n] !== d.files[n])
}

/** Files that are new or different from the saved version (the editor marks them). */
export const changedFiles = (saved: Script | null | undefined, files: Record<string, string>): string[] => Object.keys(files).filter((n) => saved?.files[n] !== files[n])

/** An error text for a new file name, or '' when it is fine: a plain relative path, as the server requires. */
export function fileNameError(name: string, existing: string[]): string {
  if (!name) return 'Type a file name'
  if (name.length > 200 || !/^[A-Za-z0-9._][A-Za-z0-9._/ -]*$/.test(name) || name.endsWith('/') || name.split('/').some((p) => p === '' || p === '.' || p === '..'))
    return 'Use a relative path such as scripts/fetch.py: letters, digits, dot, dash, space and slash, no .. and no leading slash'
  if (existing.includes(name)) return 'A file with this name exists'
  return ''
}

/** Rules for saving a script, keyed by the server's field names. */
export function validateScript(d: Draft): Errors {
  const e: Errors = {}
  if (!Object.keys(d.files).length) e.files = 'Add at least one file'
  if (!d.entry || !(d.entry in d.files)) e.entry = 'Choose the file that is started'
  else if (!startable(d.entry)) e.entry = 'The entry file must end in .sh, .bash or .py'
  if (d.args.length > 500 || d.args.includes('\n')) e.args = 'One line, at most 500 characters'
  return e
}

export type DiffLine = { kind: 'same' | 'add' | 'del'; text: string }
export type DiffItem = DiffLine | { kind: 'skip'; count: number }

/** Two sides this large are not compared line by line (the table would be too big): the middle is shown as all removed, then all added. */
const lcsLimit = 4_000_000

/**
 * Line diff from `a` to `b`: the common head and tail are peeled off, the middle goes through a longest-common-subsequence table.
 * ponytail: O(n*m) on the changed middle only; a Myers diff if edits to huge files ever need to be told line by line.
 */
export function diffLines(a: string, b: string): DiffLine[] {
  const A = a === '' ? [] : a.split('\n')
  const B = b === '' ? [] : b.split('\n')
  let head = 0
  while (head < A.length && head < B.length && A[head] === B[head]) head++
  let ea = A.length
  let eb = B.length
  while (ea > head && eb > head && A[ea - 1] === B[eb - 1]) (ea--, eb--)
  const x = A.slice(head, ea)
  const y = B.slice(head, eb)
  const same = (t: string): DiffLine => ({ kind: 'same', text: t })
  const mid: DiffLine[] = []
  if (x.length * y.length > lcsLimit) {
    mid.push(...x.map((t): DiffLine => ({ kind: 'del', text: t })), ...y.map((t): DiffLine => ({ kind: 'add', text: t })))
  } else {
    const w = y.length + 1
    const t = new Uint32Array((x.length + 1) * w)
    for (let i = x.length - 1; i >= 0; i--) for (let j = y.length - 1; j >= 0; j--) t[i * w + j] = x[i] === y[j] ? t[(i + 1) * w + j + 1] + 1 : Math.max(t[(i + 1) * w + j], t[i * w + j + 1])
    let i = 0
    let j = 0
    while (i < x.length && j < y.length) {
      if (x[i] === y[j]) (mid.push(same(x[i])), i++, j++)
      else if (t[(i + 1) * w + j] >= t[i * w + j + 1]) mid.push({ kind: 'del', text: x[i++] })
      else mid.push({ kind: 'add', text: y[j++] })
    }
    while (i < x.length) mid.push({ kind: 'del', text: x[i++] })
    while (j < y.length) mid.push({ kind: 'add', text: y[j++] })
  }
  return [...A.slice(0, head).map(same), ...mid, ...A.slice(ea).map(same)]
}

/** Keeps `ctx` unchanged lines around every change and folds the rest into "skip" items (a fold of one line is kept as it is). */
export function collapseDiff(d: DiffLine[], ctx = 3): DiffItem[] {
  const keep = d.map((l, i) => l.kind !== 'same' || d.slice(Math.max(0, i - ctx), i + ctx + 1).some((n) => n.kind !== 'same'))
  const out: DiffItem[] = []
  for (let i = 0; i < d.length; ) {
    if (keep[i]) {
      out.push(d[i++])
      continue
    }
    let j = i
    while (j < d.length && !keep[j]) j++
    if (j - i === 1) out.push(d[i])
    else out.push({ kind: 'skip', count: j - i })
    i = j
  }
  return out
}

// Notebook ------------------------------------------------------------------------------------
// The script stays one file. A line "#%% name" starts a cell (a plain comment); the text before the first one is the
// unnamed first cell, "setup". The runner turns each header into a step marker, so a cell is a pipeline step.

/** One cell. header is the file's own header line (kept so odd spacing survives), null for the setup cell; bare: no lines at all after the header (not even an empty one). */
export interface Cell {
  title: string
  header: string | null
  code: string
  bare: boolean
}

/** The same rule as the runner's cellHeader: the line starts with #%% and the rest is the title. */
const cellHeaderRe = /^#%%\s*(.*?)\s*$/
export const headerTitle = (line: string): string | null => cellHeaderRe.exec(line)?.[1] ?? null
export const isCellHeader = (line: string): boolean => cellHeaderRe.test(line)

/** Splits a script into cells. joinCells(splitCells(t)) === t for every text, CRLF and odd spacing included. */
export function splitCells(text: string): Cell[] {
  const cells: Cell[] = []
  let cur: { title: string; header: string | null; body: string[] } | null = null
  const close = () => cur && cells.push({ title: cur.title, header: cur.header, code: cur.body.join('\n'), bare: cur.body.length === 0 })
  for (const l of text.split('\n')) {
    const t = headerTitle(l)
    if (t !== null) {
      close()
      cur = { title: t, header: l, body: [] }
    } else {
      cur ??= { title: '', header: null, body: [] }
      cur.body.push(l)
    }
  }
  close()
  return cells
}

/** The header line a cell is written with: its own when its name is unchanged, else rebuilt. Only the first cell may go without one (anything else would merge into the cell above). */
function headerLine(c: Cell, i: number): string | null {
  if (c.header !== null) return headerTitle(c.header) === c.title ? c.header : c.title ? `#%% ${c.title}` : '#%%'
  return i === 0 ? null : c.title ? `#%% ${c.title}` : '#%%'
}

export function joinCells(cells: Cell[]): string {
  const out: string[] = []
  cells.forEach((c, i) => {
    const h = headerLine(c, i)
    if (h !== null) out.push(h)
    if (!c.bare) for (const l of c.code.split('\n')) out.push(l)
  })
  return out.join('\n')
}

/** The step name the runner gives a header: one word, as in the runner's cellName (n counts header cells from 1). */
export function cellStep(title: string, n: number): string {
  const s = title.trim().replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60)
  return s || `cell-${n}`
}

/** Step names by cell: null for the setup cell, which has no marker. */
export function stepNames(cells: Cell[]): (string | null)[] {
  let n = 0
  return cells.map((c, i) => (headerLine(c, i) === null ? null : cellStep(c.title, ++n)))
}

/** Where each cell sits in the file, 1-based: its header line (null for setup) and the first line of its code, and how many code lines it has. */
export function cellLines(cells: Cell[]): { header: number | null; first: number; count: number }[] {
  let line = 1
  return cells.map((c, i) => {
    const header = headerLine(c, i) === null ? null : line++
    const first = line
    const count = c.bare ? 0 : c.code.split('\n').length
    line += count
    return { header, first, count }
  })
}

/** The cell a file line belongs to and the line within its code (0 = its header line). A line past the end belongs to the last cell. */
export function locateLine(cells: Cell[], line: number): { cell: number; line: number } | null {
  if (!cells.length || line < 1) return null
  const at = cellLines(cells)
  for (let i = 0; i < at.length; i++) {
    const { header, first, count } = at[i]
    if (line === header) return { cell: i, line: 0 }
    if (line >= first && line < first + count) return { cell: i, line: line - first + 1 }
  }
  const last = at.length - 1
  return { cell: last, line: Math.max(1, at[last].count) }
}

export type Severity = 'error' | 'warning' | 'info'

export interface Problem {
  file: string
  line: number
  col: number
  severity: Severity
  message: string
  tool: string
  code?: string
}

/** POST .../check: what the runner found. */
export interface CheckResult {
  problems: Problem[]
  tools: string[]
  error?: string
}

const rank: Record<Severity, number> = { error: 3, warning: 2, info: 1 }
const worse = (a: Severity | undefined, b: Severity): Severity => (a && rank[a] >= rank[b] ? a : b)

/** The worst severity on each line of a file (the gutter marks these). */
export function lineSeverities(problems: Problem[], file: string): Record<number, Severity> {
  const out: Record<number, Severity> = {}
  for (const p of problems) if (p.file === file && p.line >= 1) out[p.line] = worse(out[p.line], p.severity)
  return out
}

export interface CellProblems {
  errors: number
  warnings: number
  /** worst severity by line within the cell's code; 0 is the header line */
  lines: Record<number, Severity>
}

/** Maps the entry file's problems (file lines) onto cells (lines within a cell). */
export function problemsByCell(cells: Cell[], problems: Problem[], entry: string): CellProblems[] {
  const out: CellProblems[] = cells.map(() => ({ errors: 0, warnings: 0, lines: {} }))
  for (const p of problems) {
    if (p.file !== entry) continue
    const at = locateLine(cells, p.line)
    if (!at) continue
    const c = out[at.cell]
    if (p.severity === 'error') c.errors++
    else if (p.severity === 'warning') c.warnings++
    c.lines[at.line] = worse(c.lines[at.line], p.severity)
  }
  return out
}

export type CellStatus = 'queued' | 'running' | 'ok' | 'failed' | 'stopped'
export type SessionStatus = 'starting' | 'ready' | 'closing' | 'closed'

export const cellStatusLabel: Record<CellStatus, string> = { queued: 'Queued', running: 'Running', ok: 'OK', failed: 'Failed', stopped: 'Stopped' }
export const cellBusy = (s: CellStatus | undefined): boolean => s === 'queued' || s === 'running'

export type RunAllStep = { action: 'run'; index: number } | { action: 'wait' } | { action: 'done' } | { action: 'stop'; index: number; reason: 'failed' | 'stopped' | 'lost' }

/**
 * Run all, one cell at a time: `ids` are the cells to run in order, `cursor` the index of the one submitted last (-1 before the first).
 * The next cell is only sent once the one before it is OK, so a failure stops the run (cells that were already queued would run anyway).
 */
export function runAllNext(ids: string[], status: Record<string, CellStatus | undefined>, cursor: number): RunAllStep {
  if (cursor >= 0) {
    const s = status[ids[cursor]]
    if (cellBusy(s)) return { action: 'wait' }
    if (s === 'failed' || s === 'stopped') return { action: 'stop', index: cursor, reason: s }
    if (s !== 'ok') return { action: 'stop', index: cursor, reason: 'lost' }
  }
  return cursor + 1 >= ids.length ? { action: 'done' } : { action: 'run', index: cursor + 1 }
}

/** How long after the last edit a check is due, in ms; null when none is wanted: no runner known to be online, nothing startable, one already going, or nothing edited since the last. */
export function autoCheckIn(s: { online: boolean | undefined; checking: boolean; editedAt: number; checkedAt: number; now: number; entry: string }, delay = 1500): number | null {
  if (!s.online || !startable(s.entry) || s.checking || s.editedAt <= s.checkedAt) return null
  return Math.max(0, s.editedAt + delay - s.now)
}

/** Fractions of a second padded to nanoseconds, so two timestamps compare as text. */
const stamp = (t: string) => t.replace(/(\.\d+)?Z$/, (_, f: string | undefined) => `${(f ?? '.').padEnd(10, '0').slice(0, 10)}Z`)
/** The later of two RFC 3339 timestamps; the poll sends the latest back as `since`, so it must stay exactly as the server wrote it. */
export const laterStamp = (a: string, b: string): string => (!a || stamp(b) > stamp(a) ? b : a)

/** One cell's latest run in a session. The server never sends the code back; `sent` is what was sent. */
export interface CellRun {
  /** the server's id for this run; 0 until the request has been answered */
  server: number
  status: CellStatus
  output: string
  exit_code: number | null
  sent: string
}

export interface SessionCell {
  id: number
  name: string
  status: CellStatus
  output: string
  exit_code: number | null
  started_at: string | null
  finished_at: string | null
  updated_at: string
}

/** GET/POST .../sessions: `cells` holds only those changed after `since`, without their code. */
export interface SessionOut {
  id: string
  status: SessionStatus
  lang: string
  entry: string
  real_workspace: boolean
  error: string
  run: number
  created_at: string
  cells: SessionCell[]
}

/** Folds the cells of a poll into the runs by the server id each editor cell last got. A run that was replaced meanwhile is ignored. */
export function applyCells(runs: Record<string, CellRun>, cells: SessionCell[]): Record<string, CellRun> {
  let next = runs
  for (const c of cells) {
    const key = Object.keys(next).find((k) => next[k].server === c.id)
    if (!key) continue
    const r = next[key]
    if (r.status === c.status && r.output === c.output && r.exit_code === c.exit_code) continue
    next = { ...next, [key]: { ...r, status: c.status, output: c.output, exit_code: c.exit_code } }
  }
  return next
}

/** One line for the toolbar: where the session is, and why it ended. */
export function sessionLine(s: { status: SessionStatus; error: string } | null, noRunner = false): string {
  if (noRunner) return 'No runner is online'
  if (!s) return 'No session yet'
  const word = { starting: 'Starting…', ready: 'Ready', closing: 'Closing…', closed: 'Closed' }[s.status]
  return s.error ? `${word}: ${s.error}` : word
}

export const sessionLive = (s: { status: SessionStatus } | null): boolean => !!s && (s.status === 'starting' || s.status === 'ready')

// Environment ------------------------------------------------------------------------------

/** A stored variable. A secret has no value here: the server never sends it back. */
export interface EnvVar {
  name: string
  secret: boolean
  value: string | null
  /** set when the variable points at a stored secret (Admin > Pipelines > Secrets); the value stays null */
  secret_ref?: string
  updated_at: string
}

/** What a variable is saved with: its own value, or the name of a stored secret (never both). */
export type EnvBody = { name: string; value: string; secret: boolean } | { name: string; secret_ref: string }

/** The body a variable is saved with: a stored secret by name, or its own value. */
export const envBody = (f: { name: string; kind: 'value' | 'stored'; value: string; secret: boolean; ref: string }): EnvBody =>
  f.kind === 'stored' ? { name: f.name.trim(), secret_ref: f.ref } : { name: f.name.trim(), value: f.value, secret: f.secret }

// Stored secrets ---------------------------------------------------------------------------

/** A stored secret as the list shows it: never its value. */
export interface Secret {
  name: string
  description: string
  used_by: { pipeline: string; variable: string }[]
  updated_by: string
  updated_at: string
  last_used_at: string | null
}

export const secretsUrl = '/admin/pipelines?secrets'

/** Rules for a secret's name: lower-case letters, digits, dashes, dots and underscores; 1 to 100 characters. */
export function validateSecret(f: { name: string; value?: string }): Errors {
  const e: Errors = {}
  if (!/^[a-z0-9_][a-z0-9._-]{0,99}$/.test(f.name.trim())) e.name = 'Use 1 to 100 lowercase letters, digits, dashes, dots and underscores'
  if (f.value !== undefined && f.value === '') e.value = 'A secret needs a value'
  return e
}

// Runners ------------------------------------------------------------------------------------

export interface Runner {
  id: string
  name: string
  token_prefix: string
  version: string
  last_seen_at: string | null
  online: boolean
  created_at: string
}

export const noRunnerText = 'No runner is connected, so scheduled runs will fail after 10 minutes. Start the runner service (docker compose up -d runner) or create one under Runners.'

/** The runner settings for a container's env file. */
export const runnerEnvFile = (origin: string, token: string) => `KERA_URL=${origin}\nKERA_RUNNER_TOKEN=${token}`

/** Rules for a runner's name (the same slug as a pipeline's). */
export function validateRunner(f: { name: string }): Errors {
  return slugRe.test(f.name.trim()) ? {} : { name: 'Use 1 to 40 lowercase letters, digits and dashes, starting with a letter or digit' }
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
export const useRuns = (name: string, limit: number, enabled = true) =>
  useQuery({
    ...live,
    enabled,
    queryKey: key(name, 'runs', limit),
    queryFn: () => api<{ runs: Run[]; more: boolean }>(`${base}/${enc(name)}/runs?limit=${limit}`),
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (q.state.data?.runs.some((r) => r.status === 'running') ? 10_000 : 60_000),
  })

/** The script steps the saved runs reported, in run order. */
export const useReportedSteps = (name: string, enabled = true) =>
  useQuery({ ...live, enabled, queryKey: key(name, 'reported-steps'), queryFn: () => api<{ runs: number; steps: ReportedStep[] }>(`${base}/${enc(name)}/reported-steps`) })

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

/** The stored script: the newest version in full plus the version list. */
export const useScript = (name: string, enabled = true) => useQuery({ ...live, queryKey: key(name, 'script'), queryFn: () => api<ScriptList>(`${base}/${enc(name)}/script`), enabled })

/** One stored version in full. A version never changes, so it is fetched once. */
export const useScriptVersion = (name: string, version: number | undefined) =>
  useQuery({ queryKey: key(name, 'script', version ?? 0), queryFn: () => api<Script>(`${base}/${enc(name)}/script/${version}`), enabled: version != null, staleTime: Infinity })

/** The variables of a pipeline: secrets without values. */
export const useEnv = (name: string, enabled = true) => useQuery({ ...live, queryKey: key(name, 'env'), queryFn: () => api<{ env: EnvVar[] }>(`${base}/${enc(name)}/env`), enabled })

const runnersKey = ['admin', 'pipeline-runners']
const secretsKey = ['admin', 'secrets']

/** The stored secrets, without values. */
export const useSecrets = (enabled = true) => useQuery({ ...live, queryKey: secretsKey, queryFn: () => api<{ secrets: Secret[] }>('/api/v1/admin/secrets'), enabled })

/** Every runner; polled, because "Online" is a matter of the last two minutes. */
export const useRunners = (enabled = true) => useQuery({ ...live, queryKey: runnersKey, queryFn: () => api<{ runners: Runner[] }>('/api/v1/admin/pipeline-runners'), refetchInterval: 15_000, enabled })

/** Every write, each refreshing the pipeline queries (and the runners). */
export function usePipelineApi() {
  const qc = useQueryClient()
  const call = async <T>(path: string, method: string, json?: unknown) => {
    const r = await api<T>(path.startsWith('/api/') ? path : base + path, { method, json })
    await Promise.all([qc.invalidateQueries({ queryKey: key() }), qc.invalidateQueries({ queryKey: runnersKey }), qc.invalidateQueries({ queryKey: secretsKey })])
    return r
  }
  return {
    create: (body: Record<string, unknown>) => call<{ pipeline: Pipeline; token: string }>('', 'POST', body),
    update: (name: string, body: Record<string, unknown>) => call<Pipeline>(`/${enc(name)}`, 'PUT', body),
    remove: (name: string) => call<{ ok: boolean }>(`/${enc(name)}`, 'DELETE'),
    rotate: (name: string) => call<{ token: string; token_prefix: string }>(`/${enc(name)}/token`, 'POST'),
    once: (name: string, flags: { adopt?: boolean; mass_delete?: boolean }) => call<Pipeline>(`/${enc(name)}/once`, 'POST', flags),
    close: (name: string, run: number) => call<Run>(`/${enc(name)}/runs/${run}/close`, 'POST'),
    /** How the pipeline is started and its timetable; answers the pipeline */
    schedule: (name: string, body: ReturnType<typeof scheduleBody> | { run_mode?: RunMode; scheduled?: boolean }) => call<Pipeline>(`/${enc(name)}/schedule`, 'PUT', body),
    /** Queues a run now (409 run_active while one is open); answers the new run */
    runNow: (name: string) => call<Run>(`/${enc(name)}/run`, 'POST'),
    /** Replaces the whole catalog of stage titles and descriptions; answers the pipeline */
    setStages: (name: string, stages: Stage[]) => call<Pipeline>(`/${enc(name)}/stages`, 'PUT', { stages }),
    /** A queued run closes at once; a running one is asked to stop */
    cancel: (name: string, run: number) => call<Run>(`/${enc(name)}/runs/${run}/cancel`, 'POST'),
    /** Stores a new version; answers it */
    saveScript: (name: string, body: { files: Record<string, string>; entry: string; args: string; note: string }) => call<Script>(`/${enc(name)}/script`, 'PUT', body),
    setEnv: (name: string, body: EnvBody) => call<{ env: EnvVar[] }>(`/${enc(name)}/env`, 'PUT', body),
    deleteEnv: (name: string, variable: string) => call<{ env: EnvVar[] }>(`/${enc(name)}/env/${enc(variable)}`, 'DELETE'),
    /** The value goes up and is never sent back */
    createSecret: (body: { name: string; description: string; value: string }) => call<{ secrets: Secret[] }>('/api/v1/admin/secrets', 'POST', body),
    /** A new value (rotation) and/or a description; the value is write-only */
    updateSecret: (name: string, body: { value?: string; description?: string }) => call<{ secrets: Secret[] }>(`/api/v1/admin/secrets/${enc(name)}`, 'PUT', body),
    /** 409 while a variable still uses it */
    deleteSecret: (name: string) => call<undefined>(`/api/v1/admin/secrets/${enc(name)}`, 'DELETE'),
    /** The token is in the answer once; Kera keeps a hash */
    createRunner: (runnerName: string) => call<{ runner: Runner; token: string }>('/api/v1/admin/pipeline-runners', 'POST', { name: runnerName }),
    rotateRunner: (id: string) => call<{ token: string; token_prefix: string }>(`/api/v1/admin/pipeline-runners/${enc(id)}/token`, 'POST'),
    deleteRunner: (id: string) => call<undefined>(`/api/v1/admin/pipeline-runners/${enc(id)}`, 'DELETE'),
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
