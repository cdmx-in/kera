import { describe, expect, it } from 'vitest'
import { importSummary } from './Skills'

describe('importSummary', () => {
  it('counts outcomes and skipped files', () => {
    const r = { results: [{ name: 'a', outcome: 'created' as const }, { name: 'b', outcome: 'created' as const }, { name: 'c', outcome: 'unchanged' as const }], warnings: ['x.png: not a text file'] }
    expect(importSummary(r)).toBe('2 created, 1 unchanged (1 files skipped)')
    expect(importSummary({ results: [], warnings: [] })).toBe('Nothing imported')
  })
})
