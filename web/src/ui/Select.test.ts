import { describe, expect, it } from 'vitest'
import { EMPTY, fromRadix, toRadix } from './Select'

describe('Select empty-value mapping', () => {
  const withNone = [{ value: '', label: '(none)' }, { value: 'a', label: 'A' }]
  const plain = [{ value: 'a', label: 'A' }]
  it('maps "" to the sentinel only when an option has value ""', () => {
    expect(toRadix('', withNone)).toBe(EMPTY)
    expect(toRadix('', plain)).toBe('')
    expect(toRadix('a', withNone)).toBe('a')
  })
  it('maps the sentinel back to ""', () => {
    expect(fromRadix(EMPTY)).toBe('')
    expect(fromRadix('a')).toBe('a')
  })
})
