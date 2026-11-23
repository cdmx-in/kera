import { describe, expect, it } from 'vitest'
import type { FileRef, Message } from '@/api/types'
import { canCowork, canSchedule, clock, collectOutputs, minutesOf, scheduleText, taskStatus } from './lib'

describe('scheduleText', () => {
  const base = { at_minute: 150, weekday: 1, timezone: 'Asia/Kolkata' }
  it('says each frequency in words', () => {
    expect(scheduleText({ ...base, frequency: 'daily' })).toBe('Every day at 02:30 (Asia/Kolkata)')
    expect(scheduleText({ ...base, frequency: 'weekdays', at_minute: 540 })).toBe('Weekdays at 09:00 (Asia/Kolkata)')
    expect(scheduleText({ ...base, frequency: 'weekly', at_minute: 540 })).toBe('Every Monday at 09:00 (Asia/Kolkata)')
    expect(scheduleText({ ...base, frequency: 'hourly', at_minute: 5 })).toBe('Every hour at :05 (Asia/Kolkata)')
  })
  it('converts between a time input and minutes', () => {
    expect(clock(1439)).toBe('23:59')
    expect(minutesOf('02:30')).toBe(150)
    expect(minutesOf('')).toBe(0)
  })
})

describe('canSchedule', () => {
  const user = (features: Record<string, boolean>) => ({ id: 'u', name: 'U', email: 'u@example.com', role: 'user' as const, permissions: { features } })
  it('needs cowork and the schedule permission; admins always', () => {
    expect(canSchedule(user({ cowork: true }))).toBe(false)
    expect(canSchedule(user({ cowork: true, cowork_schedule: true }))).toBe(true)
    expect(canSchedule(user({ cowork_schedule: true }))).toBe(false)
    expect(canSchedule({ ...user({}), role: 'admin' })).toBe(true)
    expect(canSchedule(undefined)).toBe(false)
  })
})

const msg = (id: string, over: Partial<Message> = {}): Message => ({ id, parentId: null, childrenIds: [], role: 'assistant', createdAt: 0, status: 'done', parts: [], files: [], sources: [], pendingCalls: [], ...over })

describe('taskStatus', () => {
  it('derives the status from the leaf', () => {
    expect(taskStatus({}, null)).toBe('stopped')
    expect(taskStatus({ u: msg('u', { role: 'user' }) }, 'u')).toBe('stopped')
    expect(taskStatus({ a: msg('a', { status: 'streaming' }) }, 'a')).toBe('running')
    expect(taskStatus({ a: msg('a', { status: 'streaming', pendingCalls: [{ kind: 'confirm', callId: 'c', message: 'ok?' }] }) }, 'a')).toBe('needs_input')
    expect(taskStatus({ a: msg('a') }, 'a')).toBe('done')
    expect(taskStatus({ a: msg('a', { status: 'stopped' }) }, 'a')).toBe('stopped')
    expect(taskStatus({ a: msg('a', { status: 'error' }) }, 'a')).toBe('error')
  })
})

describe('collectOutputs', () => {
  it('keeps assistant step and message files, deduped in first-seen order, without collections', () => {
    const notes: FileRef = { id: 'f1', name: 'notes.md' }
    const csv: FileRef = { id: 'f2', name: 'out.csv' }
    const messages = {
      u: msg('u', { role: 'user', files: [{ id: 'up', name: 'input.csv' }, { id: 'kb', name: 'KB', type: 'collection' }] }),
      a: msg('a', { parts: [{ type: 'steps', steps: [{ callId: 'c', name: 'run', status: 'done', statusLines: [], files: [notes] }] }], files: [csv, notes, { id: 'kb2', name: 'KB', type: 'collection' }] }),
      b: msg('b', { files: [csv] }),
      other: msg('other', { files: [{ id: 'f9', name: 'other branch' }] }),
    }
    const out = collectOutputs(messages, ['u', 'a', 'b'])
    expect(out.map((f) => f.id)).toEqual(['f1', 'f2'])
    expect(out[0]).toBe(notes) // original refs, stable for useShallow
  })
})

describe('canCowork', () => {
  it('allows admins and users with the permission', () => {
    expect(canCowork(undefined)).toBe(false)
    expect(canCowork({ id: '1', name: '', email: '', role: 'admin' })).toBe(true)
    expect(canCowork({ id: '1', name: '', email: '', role: 'user' })).toBe(false)
    expect(canCowork({ id: '1', name: '', email: '', role: 'user', permissions: { features: { cowork: true } } })).toBe(true)
  })
})
