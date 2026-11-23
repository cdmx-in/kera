// Pure helpers for the Cowork page: task status and output files from the chat store.
import type { FileRef, Me, Message } from '@/api/types'

export type TaskStatus = 'running' | 'needs_input' | 'done' | 'stopped' | 'error'

export const STATUS_LABEL: Record<TaskStatus, string> = { running: 'Running', needs_input: 'Needs input', done: 'Done', stopped: 'Stopped', error: 'Failed' }

export const STATUS_TONE: Record<TaskStatus, string> = {
  running: 'bg-accent-soft text-accent dark:text-accent-bright',
  needs_input: 'bg-amber-500/15 text-amber-700 dark:text-amber-400',
  done: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400',
  stopped: 'bg-surface-2 text-muted',
  error: 'bg-red-500/15 text-red-700 dark:text-red-400',
}

/** Admins, or users granted features.cowork (the server checks the same permission). */
export const canCowork = (me?: Me) => !!me && (me.role === 'admin' || !!me.permissions?.features?.cowork)

/** Admins, or users granted features.cowork_schedule (a pilot permission, off by default). */
export const canSchedule = (me?: Me) => canCowork(me) && (me?.role === 'admin' || !!me?.permissions?.features?.cowork_schedule)

export type Frequency = 'hourly' | 'daily' | 'weekdays' | 'weekly'

/** A scheduled task as GET /api/v1/cowork/schedules returns it (times in epoch seconds). */
export interface Schedule {
  id: string
  title: string
  prompt?: string
  model_id: string
  tool_ids: string[]
  /** null = the assistant's default pills */
  features: Record<string, boolean> | null
  files?: FileRef[]
  frequency: Frequency
  /** minutes after local midnight; for hourly the minute of the hour */
  at_minute: number
  /** weekly only, 0 = Sunday */
  weekday: number
  timezone: string
  enabled: boolean
  notes?: string
  next_run_at: number
  last_run_at: number | null
  last_chat_id: string | null
  last_status: TaskStatus | ''
  last_error: string
  /** only in the admin list (?all=1) */
  owner?: { id: string; name: string; email: string }
}

export const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

const pad = (n: number) => String(n).padStart(2, '0')

/** "HH:MM" for minutes after midnight. */
export const clock = (minutes: number) => `${pad(Math.floor(minutes / 60))}:${pad(minutes % 60)}`

/** "HH:MM" from a time input to minutes after midnight (0 when blank). */
export const minutesOf = (hhmm: string) => {
  const [h, m] = hhmm.split(':').map(Number)
  return Number.isFinite(h) && Number.isFinite(m) ? h * 60 + m : 0
}

/** The schedule in words: "Every day at 02:30 (Asia/Kolkata)". */
export function scheduleText(s: Pick<Schedule, 'frequency' | 'at_minute' | 'weekday' | 'timezone'>): string {
  const when = {
    hourly: `Every hour at :${pad(s.at_minute % 60)}`,
    daily: `Every day at ${clock(s.at_minute)}`,
    weekdays: `Weekdays at ${clock(s.at_minute)}`,
    weekly: `Every ${WEEKDAYS[s.weekday] ?? 'week'} at ${clock(s.at_minute)}`,
  }[s.frequency]
  return `${when} (${s.timezone})`
}

/**
 * Status of the open task from its active leaf. A sent turn puts its reply in the store at once,
 * so a missing or user leaf means the run never started (rejected request): stopped, as the server lists it.
 */
export function taskStatus(messages: Record<string, Message>, currentId: string | null): TaskStatus {
  const m = currentId ? messages[currentId] : undefined
  if (m?.role !== 'assistant') return 'stopped'
  if (m.pendingCalls.length) return 'needs_input'
  return m.status === 'streaming' ? 'running' : m.status
}

/** Files the assistant produced on the branch (step files and message files), first seen first, without knowledge collections. */
export function collectOutputs(messages: Record<string, Message>, ids: string[]): FileRef[] {
  const seen = new Set<string>()
  const out: FileRef[] = []
  const add = (f: FileRef) => {
    if (f.type === 'collection' || seen.has(f.id)) return
    seen.add(f.id)
    out.push(f)
  }
  for (const id of ids) {
    const m = messages[id]
    if (m?.role !== 'assistant') continue
    for (const p of m.parts) if (p.type === 'steps') for (const s of p.steps) s.files?.forEach(add)
    m.files.forEach(add)
  }
  return out
}
