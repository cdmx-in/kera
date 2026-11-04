import { describe, expect, it } from 'vitest'
import type { StreamEvent } from '@/api/types'
import { decodeEvent, streamLoop } from './stream'

const sse = (...events: [string, object][]) => events.map(([e, d]) => `event: ${e}\ndata: ${JSON.stringify(d)}\n\n`).join('')

function body(text: string, opts: { failAfter?: boolean } = {}) {
  const bytes = new TextEncoder().encode(text)
  return new Response(
    new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(bytes)
        if (!opts.failAfter) c.close()
      },
      // errors only after the first chunk was read (error() would drop queued chunks)
      pull(c) {
        if (opts.failAfter && c.desiredSize !== null && c.desiredSize > 0) c.error(new Error('network drop'))
      },
    }),
  )
}

describe('decodeEvent', () => {
  it('takes the type from the event field or the data', () => {
    expect(decodeEvent('delta', '{"seq":1,"text":"a"}')).toEqual({ type: 'delta', seq: 1, text: 'a' })
    expect(decodeEvent('message', '{"type":"done"}')).toEqual({ type: 'done' })
    expect(decodeEvent('message', 'not json')).toBeNull()
    expect(decodeEvent('message', '{"x":1}')).toBeNull()
  })
})

describe('streamLoop', () => {
  it('resumes after a drop and skips replayed events', async () => {
    const got: StreamEvent[] = []
    const resumedAfter: number[] = []
    const outcome = await streamLoop({
      start: async () => body(sse(['delta', { seq: 1, text: 'a' }], ['delta', { seq: 2, text: 'b' }]), { failAfter: true }),
      resume: async (after) => {
        resumedAfter.push(after)
        // server replays from a snapshot point: seq 2 again, then new events
        return body(sse(['delta', { seq: 2, text: 'b' }], ['delta', { seq: 3, text: 'c' }], ['done', { seq: 4 }]))
      },
      onEvent: (e) => got.push(e),
      signal: new AbortController().signal,
      delayMs: () => 0,
    })
    expect(outcome).toBe('finished')
    expect(resumedAfter).toEqual([2])
    expect(got.map((e) => e.seq)).toEqual([1, 2, 3, 4])
  })

  it('resumes when the stream ends cleanly without done', async () => {
    let calls = 0
    const outcome = await streamLoop({
      start: async () => body(sse(['delta', { seq: 1, text: 'a' }])),
      resume: async () => {
        calls++
        return body(sse(['done', { seq: 2 }]))
      },
      onEvent: () => {},
      signal: new AbortController().signal,
      delayMs: () => 0,
    })
    expect(outcome).toBe('finished')
    expect(calls).toBe(1)
  })

  it('gives up after the retry budget', async () => {
    const outcome = await streamLoop({
      start: async () => body(''),
      resume: async () => {
        throw new Error('down')
      },
      onEvent: () => {},
      signal: new AbortController().signal,
      retries: 2,
      delayMs: () => 0,
    })
    expect(outcome).toBe('lost')
  })

  it('reattaches with after= when resuming an existing run', async () => {
    const after: number[] = []
    await streamLoop({
      start: async () => {
        throw new Error('must not start')
      },
      resume: async (n) => {
        after.push(n)
        return body(sse(['done', { seq: 9 }]))
      },
      after: 5,
      onEvent: () => {},
      signal: new AbortController().signal,
    })
    expect(after).toEqual([5])
  })

  it('surfaces a 4xx on the first request', async () => {
    await expect(
      streamLoop({
        start: async () => {
          throw Object.assign(new Error('locked'), { status: 409, code: 'local_lock' })
        },
        resume: async () => body(''),
        onEvent: () => {},
        signal: new AbortController().signal,
      }),
    ).rejects.toThrow('locked')
  })
})
