import { describe, expect, it } from 'vitest'
import { resolveModel } from './model-select'
import { normalizeModel } from '@/api/queries'

const models = [
  { id: 'a', name: 'A' },
  { id: 'h', name: 'Hidden', hidden: true },
  { id: 'b', name: 'B' },
]

describe('resolveModel', () => {
  it('prefers an explicit request, hidden allowed', () => {
    expect(resolveModel(models, { requested: ['nope', 'h'], defaults: ['b'] })).toBe('h')
  })
  it('then the chat model, then visible defaults, then the first visible', () => {
    expect(resolveModel(models, { chatModel: 'b' })).toBe('b')
    expect(resolveModel(models, { defaults: ['h', 'b'] })).toBe('b')
    expect(resolveModel(models, {})).toBe('a')
    expect(resolveModel([], {})).toBe('')
  })
})

describe('normalizeModel', () => {
  it('turns feature ids into pills with defaults', () => {
    const m = normalizeModel({ id: 'k', name: 'K', features: ['web_search', 'code_interpreter'] as never, defaultFeatures: ['web_search'] })
    expect(m.features).toEqual([
      { id: 'web_search', label: 'Web search', default: true },
      { id: 'code_interpreter', label: 'Code interpreter', default: false },
    ])
  })
  it('keeps feature objects as they are', () => {
    const f = [{ id: 'memory', label: 'Memory', icon: 'brain', default: true }]
    expect(normalizeModel({ id: 'k', name: 'K', features: f }).features?.[0]).toBe(f[0])
  })
})
