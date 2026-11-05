import { describe, expect, it } from 'vitest'
import { coerce } from './form'

describe('coerce', () => {
  it('parses json fields and rejects bad json', () => {
    expect(coerce('json', '{"php":{"command":["php","{file}"]}}', 'k')).toEqual({ php: { command: ['php', '{file}'] } })
    expect(coerce('json', { a: 1 }, 'k')).toEqual({ a: 1 })
    expect(() => coerce('json', '{', 'k')).toThrow('k must be valid JSON')
    expect(coerce('int', '5', 'k')).toBe(5)
  })
  it('splits list fields into trimmed non-empty lines', () => {
    expect(coerce('lines', ' project\\s+nova \n\n  secret-x\n', 'k')).toEqual(['project\\s+nova', 'secret-x'])
    expect(coerce('lines', ['a'], 'k')).toEqual(['a'])
  })
})
