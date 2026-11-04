import { describe, expect, it } from 'vitest'
import { ago, collapse, compact, humanize, lastDays, monthRange, move, parseList, permRows, setPerm, sortPeople, spendLevel, spendView } from './lib'

describe('admin lib', () => {
  it('humanizes keys', () => {
    expect(humanize('api_keys')).toBe('API keys')
    expect(humanize('max_tool_iterations')).toBe('Max tool iterations')
    expect(humanize('model_ids')).toBe('Model IDs')
  })

  it('lists the known permission tree with values', () => {
    const rows = permRows({ chat: { share: true, edit: true }, features: { api_keys: false } }, { chat: { edit: true }, other: { x: true } })
    expect(rows).toEqual([
      { cat: 'chat', leaves: [{ key: 'edit', on: true }, { key: 'share', on: false }] },
      { cat: 'features', leaves: [{ key: 'api_keys', on: false }] },
    ])
    expect(setPerm({ chat: { edit: true } }, 'chat', 'share', true)).toEqual({ chat: { edit: true, share: true } })
  })

  it('computes UTC ranges', () => {
    expect(monthRange(new Date('2026-02-14T23:30:00Z'))).toEqual(['2026-02-01', '2026-02-28'])
    expect(monthRange(new Date('2024-12-31T12:00:00Z'))).toEqual(['2024-12-01', '2024-12-31'])
    expect(lastDays(30, new Date('2026-03-01T05:00:00Z'))).toEqual(['2026-01-31', '2026-03-01'])
  })

  it('formats', () => {
    const now = 1_000_000_000_000
    expect(ago(0, now)).toBe('–')
    expect(ago(now / 1000 - 90, now)).toBe('1 min ago')
    expect(ago(now / 1000 - 3 * 86400, now)).toBe('3 d ago')
    expect(compact(950)).toBe('950')
    expect(compact(1234)).toBe('1.2k')
    expect(compact(3_400_000)).toBe('3.4M')
    expect(parseList('a, b\n\n c')).toEqual(['a', 'b', 'c'])
    expect(move(['a', 'b', 'c'], 0, 2)).toEqual(['b', 'c', 'a'])
    expect(move(['a', 'b'], 0, 5)).toEqual(['a', 'b'])
  })

  it('folds spend rows like the old page', () => {
    const v = spendView({
      start_date: '2026-03-01',
      end_date: '2026-03-03',
      rows: [
        { end_user: 'ada@x.io', model: 'gpt', day: '2026-03-02', spend: 6, prompt_tokens: 100, completion_tokens: 10, requests: 2 },
        { end_user: 'ada@x.io', model: 'mini', day: '2026-03-01', spend: 5, prompt_tokens: 50, completion_tokens: 5, requests: 1 },
        { end_user: '', model: 'embed', day: '2026-03-02', spend: 0.01, prompt_tokens: 30, completion_tokens: 0, requests: 1 },
      ],
      users: [
        { id: 'u1', name: 'Ada', email: 'ada@x.io', last_active_at: 1 },
        { id: 'u2', name: 'Bob', email: 'bob@x.io', last_active_at: 0 },
      ],
    })
    expect(v.total).toBeCloseTo(11.01)
    expect([v.users, v.active, v.idle]).toEqual([2, 2, 1])
    expect(v.people.map((p) => [p.name, p.spend, p.tokens])).toEqual([
      ['Ada', 11, 165],
      ['Bob', 0, 0],
      ['Unattributed (embeddings)', 0.01, 30],
    ])
    expect(v.days).toEqual([
      { day: '2026-03-01', spend: 5 },
      { day: '2026-03-02', spend: 6.01 },
      { day: '2026-03-03', spend: 0 },
    ])
    expect(v.models[0]).toEqual({ model: 'gpt', spend: 6, requests: 2, input: 100, output: 10 })
    expect(sortPeople(v.people, 'spend', true).map((p) => p.name)[0]).toBe('Ada')
    expect(sortPeople(v.people, 'name', false).map((p) => p.name)).toEqual(['Ada', 'Bob', 'Unattributed (embeddings)'])
    expect([spendLevel(11), spendLevel(6), spendLevel(0.5), spendLevel(0)]).toEqual(['high', 'medium', 'light', 'none'])
  })
})

describe('access', () => {
  it('collapses grants: write wins, everyone first, then groups, then users', () => {
        expect(
      collapse([
        { principal: 'user:u1', permission: 'read' },
        { principal: 'group:g1', permission: 'read' },
        { principal: 'group:g1', permission: 'write' },
        { principal: 'user:*', permission: 'read' },
      ]),
    ).toEqual([
      { principal: 'user:*', permission: 'read' },
      { principal: 'group:g1', permission: 'write' },
      { principal: 'user:u1', permission: 'read' },
    ])
  })
})
