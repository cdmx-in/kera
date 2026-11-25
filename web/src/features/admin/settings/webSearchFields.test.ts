import { describe, expect, it } from 'vitest'
import { WEB_SEARCH_FIELDS } from './webSearchFields'

describe('web search settings fields', () => {
  it('exposes every web.search key the backend reads', () => {
    expect(WEB_SEARCH_FIELDS.map((f) => f.key).sort()).toEqual([
      'web.search.engine',
      'web.search.max_calls_per_turn',
      'web.search.result_count',
      'web.search.url',
    ])
  })

  it('has a text field for the SearXNG address', () => {
    const url = WEB_SEARCH_FIELDS.find((f) => f.key === 'web.search.url')
    expect(url?.kind).toBe('text')
    expect(url?.label).toBe('SearXNG address')
    expect(url?.hint).toBeTruthy()
  })
})
