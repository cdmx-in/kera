// The new-task wizard's own state: everything except the assistant, its switches and the chosen apps,
// which stay in the composer store (the chat engine's request reads them from there).
import type { FileRef } from '@/api/types'
import { clock, minutesOf, type Frequency, type Schedule } from '../lib'

export type When = 'now' | Frequency

export const WHEN_VALUES: When[] = ['now', 'hourly', 'daily', 'weekdays', 'weekly']

export interface Draft {
  title: string
  goal: string
  /** knowledge collections, sent as {type:'collection'} files */
  collections: FileRef[]
  /** files already uploaded: those a saved task has, those of a restored draft */
  kept: FileRef[]
  when: When
  /** "HH:MM" for daily, weekdays and weekly */
  time: string
  /** minute of the hour, hourly only */
  minute: number
  /** weekly only, 0 = Sunday */
  weekday: number
  timezone: string
  /** a saved task's notes for its next run (edit only) */
  notes: string
}

export type Action =
  | { type: 'set'; patch: Partial<Draft> }
  | { type: 'addCollection'; collection: FileRef }
  | { type: 'removeCollection'; id: string }
  | { type: 'removeKept'; id: string }
  | { type: 'restore'; draft: Draft }

export function reducer(d: Draft, a: Action): Draft {
  switch (a.type) {
    case 'set':
      return { ...d, ...a.patch }
    case 'addCollection':
      return d.collections.some((c) => c.id === a.collection.id) ? d : { ...d, collections: [...d.collections, a.collection] }
    case 'removeCollection':
      return { ...d, collections: d.collections.filter((c) => c.id !== a.id) }
    case 'removeKept':
      return { ...d, kept: d.kept.filter((f) => f.id !== a.id) }
    case 'restore':
      return a.draft
  }
}

/** The `?when=` link parameter: a schedule to preselect. */
export function whenParam(v: string | null): When | undefined {
  return WHEN_VALUES.find((w) => w === v)
}

/** A new draft, or the draft of a saved task being edited. `when` (?when=) preselects a schedule; people who may not schedule always get "now". */
export function initialDraft(o: { job?: Schedule; goal?: string; when?: When; zone: string; schedulable: boolean }): Draft {
  const { job } = o
  return {
    title: job?.title ?? '',
    goal: job?.prompt ?? o.goal ?? '',
    collections: job?.files?.filter((f) => f.type === 'collection') ?? [],
    kept: job?.files?.filter((f) => f.type !== 'collection') ?? [],
    when: job?.frequency ?? (o.schedulable ? (o.when ?? 'now') : 'now'),
    time: clock(job && job.frequency !== 'hourly' ? job.at_minute : 120),
    minute: job?.frequency === 'hourly' ? job.at_minute : 0,
    weekday: job?.weekday ?? 1,
    timezone: job?.timezone || o.zone,
    notes: job?.notes ?? '',
  }
}

/** What the page picks outside the draft: the composer store's assistant, switches and apps, and the files uploaded in this visit. */
export interface Picks {
  model: string
  toolIds: string[]
  features: Record<string, boolean>
  files: FileRef[]
}

/** The body of POST /api/v1/cowork/schedules (and PUT, with notes), and of POST /api/v1/cowork/test-run. */
export function taskBody(d: Draft, p: Picks, edit: boolean) {
  const goal = d.goal.trim()
  return {
    title: d.title.trim() || goal.split('\n')[0].slice(0, 60),
    prompt: goal,
    model_id: p.model,
    tool_ids: p.toolIds,
    features: p.features,
    files: [...d.kept, ...d.collections, ...p.files],
    frequency: d.when,
    at_minute: d.when === 'hourly' ? d.minute : minutesOf(d.time),
    weekday: d.weekday,
    timezone: d.timezone,
    ...(edit ? { notes: d.notes } : {}),
  }
}

// ---- keeping the draft across a reload and the OAuth round trip of Connect ---------------------

export interface Saved {
  draft: Draft
  model: string
  toolIds: string[]
  features: Record<string, boolean>
}

export const draftKey = (jobId?: string) => `wizard:cowork:${jobId ?? 'new'}`

export function parseSaved(raw: string | null): Saved | null {
  if (!raw) return null
  try {
    const s = JSON.parse(raw) as Partial<Saved>
    const d = s.draft
    if (!d || typeof d.goal !== 'string' || !Array.isArray(d.collections) || !Array.isArray(d.kept) || !WHEN_VALUES.includes(d.when)) return null
    return { draft: d, model: typeof s.model === 'string' ? s.model : '', toolIds: Array.isArray(s.toolIds) ? s.toolIds : [], features: s.features && typeof s.features === 'object' ? s.features : {} }
  } catch {
    return null
  }
}
