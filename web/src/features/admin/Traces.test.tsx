import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { TraceBody } from './Traces'
import type { TraceDetail } from './trace'

const detail: TraceDetail = {
  run_id: 'r1',
  message_id: 'a',
  user_name: 'Pat',
  user_email: 'pat@example.com',
  model_id: 'assistant',
  started_at: '2026-01-01T00:00:00Z',
  duration_ms: 2000,
  steps: [
    { seq: 0, round: 0, kind: 'retrieval', name: 'knowledge', started_at: '2026-01-01T00:00:00Z', duration_ms: 100, prompt_tokens: null, cached_tokens: null, completion_tokens: null, finish: '', error: '', detail: { hits: 4, top_score: 0.8 } },
    { seq: 1, round: 0, kind: 'model', name: 'm', started_at: '2026-01-01T00:00:00.1Z', duration_ms: 900, prompt_tokens: 1000, cached_tokens: 800, completion_tokens: 20, finish: 'tool_calls', error: '', detail: { tool_calls: 1 } },
    { seq: 2, round: 0, kind: 'tool', name: 'lookup', started_at: '2026-01-01T00:00:01Z', duration_ms: 400, prompt_tokens: null, cached_tokens: null, completion_tokens: null, finish: '', error: '', detail: { result_chars: 5000, is_error: true } },
    { seq: 3, round: 1, kind: 'model', name: 'm', started_at: '2026-01-01T00:00:01.5Z', duration_ms: 500, prompt_tokens: 1200, cached_tokens: 1000, completion_tokens: 60, finish: 'stop', error: '', detail: {} },
  ],
}

describe('TraceBody', () => {
  const html = renderToStaticMarkup(<TraceBody d={detail} />)
  it('draws one row per step, in order, with the failed tool marked', () => {
    expect(html.match(/data-testid="trace-step"/g)).toHaveLength(4)
    expect(html.indexOf('knowledge')).toBeLessThan(html.indexOf('Round 1'))
    expect(html.indexOf('Round 1')).toBeLessThan(html.indexOf('lookup'))
    expect(html.indexOf('lookup')).toBeLessThan(html.indexOf('Round 2'))
    expect(html).toMatch(/data-kind="tool" data-status="failed"/)
  })
  it('totals the rounds and the cached share', () => {
    expect(html).toContain('Model rounds')
    expect(html).toContain('2.2k / 80') // tokens in / out
    expect(html).toContain('82%') // 1800 of 2200
    expect(html).toContain('4 passages')
  })
})
