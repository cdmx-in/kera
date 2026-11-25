import { describe, expect, it } from 'vitest'
import { bar, cachedShare, ms, stepChips, type TraceStep } from './trace'

const step = (over: Partial<TraceStep>): TraceStep => ({
  seq: 0,
  round: 0,
  kind: 'model',
  name: '',
  started_at: '2026-01-01T00:00:00Z',
  duration_ms: 0,
  prompt_tokens: null,
  cached_tokens: null,
  completion_tokens: null,
  finish: '',
  error: '',
  detail: {},
  ...over,
})

describe('trace helpers', () => {
  it('shows the cached share only when a server reported one', () => {
    expect(cachedShare(200, 150)).toBe('75%')
    expect(cachedShare(200, 0)).toBe('-')
    expect(cachedShare(0, 0)).toBe('-')
  })
  it('writes durations in the unit that reads best', () => {
    expect(ms(850)).toBe('850 ms')
    expect(ms(4200)).toBe('4.2 s')
    expect(ms(65000)).toBe('1 min 5 s')
  })
  it('keeps a bar inside the axis and gives a zero-length step a sliver', () => {
    const t0 = new Date('2026-01-01T00:00:00Z').getTime()
    expect(bar(step({ started_at: '2026-01-01T00:00:01Z', duration_ms: 2000 }), t0, 10000)).toEqual({ left: 10, width: 20 })
    const b = bar(step({ started_at: '2026-01-01T00:00:10Z', duration_ms: 5000 }), t0, 10000)
    expect(b.left + b.width).toBeLessThanOrEqual(100)
    expect(bar(step({ started_at: '2026-01-01T00:00:00Z', duration_ms: 0 }), t0, 10000).width).toBeGreaterThan(0)
  })
  it('lists a model round by tokens, cache and tools', () => {
    const chips = stepChips(step({ prompt_tokens: 12000, cached_tokens: 9000, completion_tokens: 80, detail: { tools_offered: 4, tool_def_chars: 3000, tool_calls: 2, truncated: true } })).map((c) => c.text)
    expect(chips).toEqual(['12k in', '75% cached', '80 out', '4 tools offered (3.0k chars)', '2 tool calls', 'cut off at the length limit'])
  })
  it('flags a failed tool, a truncated result and an empty retrieval', () => {
    expect(stepChips(step({ kind: 'tool', detail: { result_chars: 1200, truncated: true, is_error: true } })).map((c) => c.text)).toEqual(['1,200 chars back', 'result truncated', 'error'])
    expect(stepChips(step({ kind: 'retrieval', detail: { hits: 0 } })).map((c) => c.text)).toEqual(['0 passages'])
    expect(stepChips(step({ kind: 'retrieval', detail: { hits: 3, top_score: 0.91, reranked: true } })).map((c) => c.text)).toEqual(['3 passages', 'top score 0.91', 'reranked'])
    expect(stepChips(step({ error: 'provider_error', detail: { http_status: 502 } })).map((c) => c.text)).toEqual(['provider error', 'HTTP 502'])
  })
})
