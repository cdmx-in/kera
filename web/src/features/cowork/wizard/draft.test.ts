import { describe, expect, it } from 'vitest'
import type { FileRef } from '@/api/types'
import type { Schedule } from '../lib'
import { initialDraft, parseSaved, reducer, taskBody, whenParam, type Draft } from './draft'

const zone = 'Asia/Kolkata'
const blank = () => initialDraft({ zone, schedulable: true })
const file = (id: string): FileRef => ({ id, name: `${id}.csv` })
const kb = (id: string): FileRef => ({ id, name: id, type: 'collection' })
const job = (over: Partial<Schedule> = {}): Schedule => ({
  id: 'j1', title: 'Nightly', prompt: 'update the sheet', model_id: 'm1', tool_ids: ['sheets'], features: { web_search: true },
  files: [file('f1'), kb('k1')], frequency: 'weekly', at_minute: 540, weekday: 3, timezone: 'Europe/Berlin', enabled: true, notes: 'row 12',
  next_run_at: 0, last_run_at: null, last_chat_id: null, last_status: '', last_error: '', ...over,
})

describe('initialDraft', () => {
  it('starts a new task as a run now, with the old defaults (02:00, Monday, minute 0)', () => {
    expect(blank()).toEqual({ title: '', goal: '', collections: [], kept: [], when: 'now', time: '02:00', minute: 0, weekday: 1, timezone: zone, notes: '' })
  })
  it('preselects the schedule of ?when= only for people who may schedule', () => {
    expect(initialDraft({ zone, schedulable: true, when: 'daily' }).when).toBe('daily')
    expect(initialDraft({ zone, schedulable: false, when: 'daily' }).when).toBe('now')
  })
  it('prefills the goal from ?q=', () => {
    expect(initialDraft({ zone, schedulable: true, goal: 'check it' }).goal).toBe('check it')
  })
  it('opens a saved task as it was saved: files split into kept files and collections, its own zone and notes', () => {
    const d = initialDraft({ zone, schedulable: true, job: job() })
    expect(d).toMatchObject({ title: 'Nightly', goal: 'update the sheet', when: 'weekly', time: '09:00', weekday: 3, timezone: 'Europe/Berlin', notes: 'row 12', minute: 0 })
    expect(d.kept.map((f) => f.id)).toEqual(['f1'])
    expect(d.collections.map((f) => f.id)).toEqual(['k1'])
  })
  it('reads an hourly task\'s at_minute as the minute of the hour, not a time of day', () => {
    const d = initialDraft({ zone, schedulable: true, job: job({ frequency: 'hourly', at_minute: 45 }) })
    expect(d).toMatchObject({ when: 'hourly', minute: 45, time: '02:00' })
  })
})

describe('whenParam', () => {
  it('accepts the frequencies and now, nothing else', () => {
    expect(whenParam('daily')).toBe('daily')
    expect(whenParam('now')).toBe('now')
    expect(whenParam('monthly')).toBeUndefined()
    expect(whenParam(null)).toBeUndefined()
  })
})

describe('reducer', () => {
  it('sets fields, adds a collection once, removes collections and kept files, restores a draft', () => {
    let d: Draft = blank()
    d = reducer(d, { type: 'set', patch: { goal: 'x', when: 'daily' } })
    expect(d).toMatchObject({ goal: 'x', when: 'daily' })
    d = reducer(d, { type: 'addCollection', collection: kb('k1') })
    d = reducer(d, { type: 'addCollection', collection: kb('k1') })
    d = reducer(d, { type: 'addCollection', collection: kb('k2') })
    expect(d.collections.map((c) => c.id)).toEqual(['k1', 'k2'])
    d = reducer(d, { type: 'removeCollection', id: 'k1' })
    expect(d.collections.map((c) => c.id)).toEqual(['k2'])
    d = reducer({ ...d, kept: [file('a'), file('b')] }, { type: 'removeKept', id: 'a' })
    expect(d.kept.map((f) => f.id)).toEqual(['b'])
    expect(reducer(d, { type: 'restore', draft: blank() })).toEqual(blank())
  })
  it('does not change the draft it was given', () => {
    const d = blank()
    reducer(d, { type: 'addCollection', collection: kb('k1') })
    expect(d.collections).toEqual([])
  })
})

describe('taskBody', () => {
  const picks = { model: 'm1', toolIds: ['sheets'], features: { web_search: true }, files: [file('up')] }
  it('is what the old form sent: trimmed goal, title from the goal, files in kept, knowledge, uploads order', () => {
    const d: Draft = { ...blank(), goal: '  first line\nsecond  ', kept: [file('old')], collections: [kb('k1')], when: 'daily', time: '06:30' }
    const b = taskBody(d, picks, false)
    expect(b).toEqual({
      title: 'first line', prompt: 'first line\nsecond', model_id: 'm1', tool_ids: ['sheets'], features: { web_search: true },
      files: [file('old'), kb('k1'), file('up')], frequency: 'daily', at_minute: 390, weekday: 1, timezone: zone,
    })
  })
  it('cuts a title taken from the goal at 60 characters, and keeps a given one', () => {
    expect(taskBody({ ...blank(), goal: 'x'.repeat(100) }, picks, false).title).toHaveLength(60)
    expect(taskBody({ ...blank(), goal: 'x', title: ' Mine ' }, picks, false).title).toBe('Mine')
  })
  it('sends the minute of the hour for an hourly task, and notes only when editing', () => {
    const d: Draft = { ...blank(), goal: 'x', when: 'hourly', minute: 15, time: '09:00', notes: 'n' }
    expect(taskBody(d, picks, false).at_minute).toBe(15)
    expect(taskBody(d, picks, false)).not.toHaveProperty('notes')
    expect(taskBody(d, picks, true)).toHaveProperty('notes', 'n')
  })
})

describe('parseSaved', () => {
  it('reads back what was saved and ignores anything else', () => {
    const raw = JSON.stringify({ draft: blank(), model: 'm1', toolIds: ['a'], features: { f: true } })
    expect(parseSaved(raw)).toEqual({ draft: blank(), model: 'm1', toolIds: ['a'], features: { f: true } })
    for (const bad of [null, '', 'not json', '{}', '{"draft":{"goal":1}}', JSON.stringify({ draft: { ...blank(), when: 'yearly' } })]) expect(parseSaved(bad)).toBeNull()
  })
  it('fills in what an older save lacks', () => {
    expect(parseSaved(JSON.stringify({ draft: blank() }))).toMatchObject({ model: '', toolIds: [], features: {} })
  })
})
