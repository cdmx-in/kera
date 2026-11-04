import { describe, expect, it } from 'vitest'
import type { Message, StreamEvent } from '@/api/types'
import { applyEvent, messageText } from './reduce'

const empty = (): Message => ({ id: 'a', parentId: 'u', childrenIds: [], role: 'assistant', createdAt: 0, status: 'streaming', parts: [], files: [], sources: [], pendingCalls: [] })
const run = (evs: StreamEvent[]) => evs.reduce((m, e) => applyEvent(m, e, 1000), empty())

describe('applyEvent', () => {
  it('builds reasoning, grouped steps and text', () => {
    const m = run([
      { type: 'reasoning', text: 'think', seq: 1 },
      { type: 'tool_call', callId: 'c1', name: 'search_web', args: { q: 'x' }, seq: 2 },
      { type: 'status', callId: 'c1', id: 's1', description: 'Searching "x"', seq: 3 },
      { type: 'tool_result', callId: 'c1', status: 'ok', preview: '3 results', seq: 4 },
      { type: 'tool_call', callId: 'c2', name: 'fetch_url', seq: 5 },
      { type: 'status', description: 'Reading example.com', id: 's2', seq: 6 },
      { type: 'delta', text: 'Hello ', seq: 7 },
      { type: 'delta', text: 'world', seq: 8 },
      { type: 'done', seq: 9 },
    ])
    expect(m.parts.map((p) => p.type)).toEqual(['reasoning', 'steps', 'text'])
    const steps = m.parts[1]
    if (steps.type !== 'steps') throw new Error('steps')
    expect(steps.steps.map((s) => [s.callId, s.status, s.statusLines.length])).toEqual([
      ['c1', 'done', 1],
      ['c2', 'done', 1],
    ])
    expect(m.parts[0]).toMatchObject({ done: true })
    expect(messageText(m)).toBe('Hello world')
    expect(m.status).toBe('done')
    expect(m.seq).toBe(9)
  })

  it('tracks pending calls, sources, files and errors', () => {
    let m = run([
      { type: 'ask_user', callId: 'q1', questions: [{ question: 'Which?', options: [{ label: 'A' }] }] },
      { type: 'confirm', callId: 'k1', message: 'Copy 5 rows?' },
      { type: 'sources', sources: [{ url: 'https://a.example/x', title: 'A' }] },
      { type: 'sources', sources: [{ url: 'https://a.example/x', title: 'A' }, { url: 'https://b.example', title: 'B' }] },
      { type: 'files', files: [{ id: 'f1', filename: 'out.csv', meta: { size: 10, content_type: 'text/csv' } }] },
      { type: 'block', block: { type: 'cards', items: [] } },
      { type: 'follow_ups', follow_ups: ['More?'] },
    ])
    expect(m.pendingCalls.map((c) => c.kind)).toEqual(['ask_user', 'confirm'])
    expect(m.sources).toHaveLength(2)
    expect(m.files[0]).toMatchObject({ id: 'f1', name: 'out.csv', size: 10, contentType: 'text/csv', type: 'file' })
    expect(m.followUps).toEqual(['More?'])
    m = applyEvent(m, { type: 'error', code: 'local_lock', message: 'locked' })
    expect(m).toMatchObject({ status: 'error', error: { code: 'local_lock' } })
    m = applyEvent(m, { type: 'done' })
    expect(m.status).toBe('error')
  })

  it('replaces state from a snapshot', () => {
    const m = applyEvent(run([{ type: 'delta', text: 'old' }]), { type: 'snapshot', parts: [{ type: 'text', text: 'new' }], pendingCalls: [], files: [], sources: [], status: 'streaming' })
    expect(messageText(m)).toBe('new')
  })

  it('shows a transient status line outside steps and clears it on text', () => {
    let m = run([{ type: 'status', id: 'p', action: 'preparing_files', description: 'Reading files' }])
    expect(m.statusLine?.description).toBe('Reading files')
    m = applyEvent(m, { type: 'delta', text: 'x' })
    expect(m.statusLine).toBeUndefined()
  })
})
