import { describe, expect, it } from 'vitest'
import { resolveModel } from './model-select'

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
