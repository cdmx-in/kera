import { describe, expect, it } from 'vitest'
import { columnLetter, tableModel } from './table'

describe('tableModel', () => {
  it('pads ragged rows and names blank headers', () => {
    expect(tableModel([['Name', '', 'Total'], ['a', '1'], ['b', '2', '3', '']])).toEqual({
      header: ['Name', 'B', 'Total'],
      rows: [
        ['a', '1', ''],
        ['b', '2', '3'],
      ],
      columns: 3,
    })
  })
  it('keeps dates and numbers as given strings', () => {
    expect(tableModel([['Date'], ['2026-11-04'], ['45965']]).rows).toEqual([['2026-11-04'], ['45965']])
  })
  it('handles empty and junk input', () => {
    expect(tableModel([])).toEqual({ header: [], rows: [], columns: 0 })
    expect(tableModel(null)).toEqual({ header: [], rows: [], columns: 0 })
    expect(tableModel([['a', null, 3]])).toEqual({ header: ['a', 'B', '3'], rows: [], columns: 3 })
  })
  it('letters columns like a spreadsheet', () => {
    expect([0, 25, 26, 27, 701, 702].map(columnLetter)).toEqual(['A', 'Z', 'AA', 'AB', 'ZZ', 'AAA'])
  })
})
