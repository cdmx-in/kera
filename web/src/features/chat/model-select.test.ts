import { describe, expect, it } from 'vitest'
import { nextModel, pickSuggestions, resolveModel } from './model-select'
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
  it('never picks an embedding or rerank model', () => {
    const list = [{ id: 'all-minilm', name: 'all-minilm', kind: 'embedding' }, { id: 'rr', name: 'rr', kind: 'rerank' }, ...models]
    expect(resolveModel(list, {})).toBe('a')
    expect(resolveModel(list, { requested: ['rr'], chatModel: 'all-minilm', defaults: ['all-minilm'] })).toBe('a')
  })
})

describe('nextModel', () => {
  const base = { current: '', auto: false, requested: [] as string[], inChat: false, defaults: ['b'] }
  it('redoes an automatic pick when the default shows up later', () => {
    const partial = [{ id: 'all-minilm', name: 'x', kind: 'embedding' }, { id: 'a', name: 'A' }]
    const first = nextModel(partial, base)
    expect(first).toEqual({ id: 'a', auto: true })
    expect(nextModel(models, { ...base, current: 'a', auto: true })).toEqual({ id: 'b', auto: true })
  })
  it("keeps the user's and the chat's choice", () => {
    expect(nextModel(models, { ...base, current: 'a' })).toBeNull()
    expect(nextModel(models, { ...base, current: 'a', inChat: true, auto: true })).toEqual({ id: 'a', auto: false })
  })
  it('follows ?models= and replaces a model that disappeared', () => {
    expect(nextModel(models, { ...base, current: 'a', requested: ['h'] })).toEqual({ id: 'h', auto: false })
    expect(nextModel(models, { ...base, current: 'gone' })).toEqual({ id: 'b', auto: true })
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

describe('pickSuggestions', () => {
  const global = [{ content: 'g' }]
  it('prefers the model prompts, then info.meta, then the global list', () => {
    expect(pickSuggestions({ id: 'a', name: 'A', suggestionPrompts: [{ content: 'm' }] }, global)).toEqual([{ content: 'm' }])
    expect(pickSuggestions({ id: 'a', name: 'A', info: { meta: { suggestion_prompts: [{ content: 'i' }] } } }, global)).toEqual([{ content: 'i' }])
    expect(pickSuggestions({ id: 'a', name: 'A', suggestionPrompts: [] }, global)).toEqual(global)
    expect(pickSuggestions(undefined, global)).toEqual(global)
  })
  it('caps at six', () => {
    expect(pickSuggestions(undefined, Array.from({ length: 9 }, (_, i) => ({ content: String(i) })))).toHaveLength(6)
  })
})
