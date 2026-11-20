import { describe, expect, it } from 'vitest'
import { filterModels, filterPrompts, statusChips, type ModelCardData } from './lib'

const m = (id: string, o: Partial<ModelCardData> = {}): ModelCardData => ({ id, name: id, description: '', preset: false, local: false, hidden: false, ...o })
const items = [m('sales', { preset: true, description: 'Sales assistant' }), m('qwen', { local: true }), m('gpt', { hidden: true }), m('hr', { preset: true, local: true, hidden: true })]
const ids = (x: ModelCardData[]) => x.map((i) => i.id)

describe('filterModels', () => {
  it('filters by chip', () => {
    expect(ids(filterModels(items, '', 'all'))).toEqual(['sales', 'qwen', 'gpt', 'hr'])
    expect(ids(filterModels(items, '', 'assistants'))).toEqual(['sales', 'hr'])
    expect(ids(filterModels(items, '', 'base'))).toEqual(['qwen', 'gpt'])
    expect(ids(filterModels(items, '', 'local'))).toEqual(['qwen', 'hr'])
    expect(ids(filterModels(items, '', 'cloud'))).toEqual(['sales', 'gpt'])
    expect(ids(filterModels(items, '', 'hidden'))).toEqual(['gpt', 'hr'])
  })
  it('searches every word in id, name and description', () => {
    expect(ids(filterModels(items, 'SALES assistant', 'all'))).toEqual(['sales'])
    expect(ids(filterModels(items, 'payments', 'base'))).toEqual([])
    expect(ids(filterModels(items, '  ', 'all'))).toHaveLength(4)
  })
})

describe('statusChips', () => {
  it('summarises ingestion', () => {
    expect(statusChips(undefined)).toEqual([{ label: 'No files', tone: 'grey' }])
    expect(statusChips({ files: 85, completed: 82, pending: 1, processing: 0, failed: 2 })).toEqual([
      { label: '82 indexed', tone: 'green' },
      { label: '1 indexing', tone: 'amber' },
      { label: '2 failed', tone: 'red' },
    ])
  })
})

describe('filterPrompts', () => {
  it('matches command, title and content', () => {
    const ps = [{ command: 'sum', title: 'Summarise', content: 'Summarise this' }, { command: 'tr', title: 'Translate', content: 'To French' }]
    expect(filterPrompts(ps, 'french').map((p) => p.command)).toEqual(['tr'])
    expect(filterPrompts(ps, '').length).toBe(2)
  })
})
