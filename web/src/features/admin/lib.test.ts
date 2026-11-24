import { describe, expect, it } from 'vitest'
import { ago, collapse, compact, humanize, lastDays, monthRange, move, parseList, permRows, setPerm, sortPeople, spendLevel, spendView } from './lib'
import { clampPage, errorField, groupIcon, pageCount, pageForSize, pageRange, pageSlice, permChips, validateGroup, validateUser } from './lib'

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
      { day: '2026-03-01', spend: 5, requests: 1 },
      { day: '2026-03-02', spend: 6.01, requests: 3 },
      { day: '2026-03-03', spend: 0, requests: 0 },
    ])
    expect(v.models[0]).toEqual({ model: 'gpt', spend: 6, requests: 2, input: 100, output: 10 })
    expect(sortPeople(v.people, 'spend', true).map((p) => p.name)[0]).toBe('Ada')
    expect(sortPeople(v.people, 'name', false).map((p) => p.name)).toEqual(['Ada', 'Bob', 'Unattributed (embeddings)'])
    expect([spendLevel(11), spendLevel(6), spendLevel(0.5), spendLevel(0)]).toEqual(['high', 'medium', 'light', 'none'])
    expect(spendLevel(0, 4)).toBe('light') // used free models only
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

describe('pagination', () => {
  it('counts pages and clamps the page when the total shrinks', () => {
    expect([pageCount(0, 25), pageCount(25, 25), pageCount(26, 25), pageCount(59, 25)]).toEqual([1, 1, 2, 3])
    expect(clampPage(3, 59, 25)).toBe(3)
    expect(clampPage(3, 50, 25)).toBe(2) // last row of page 3 deleted
    expect(clampPage(9, 0, 25)).toBe(1)
    expect([clampPage(0, 59, 25), clampPage(-2, 59, 25), clampPage(NaN, 59, 25)]).toEqual([1, 1, 1])
  })
  it('slices one page of rows', () => {
    const rows = Array.from({ length: 59 }, (_, i) => i + 1)
    expect(pageSlice(rows, 1, 25)).toEqual(rows.slice(0, 25))
    expect(pageSlice(rows, 3, 25)).toEqual([51, 52, 53, 54, 55, 56, 57, 58, 59])
    expect(pageSlice(rows, 7, 25)).toEqual(pageSlice(rows, 3, 25)) // clamped
    expect(pageSlice([], 2, 25)).toEqual([])
  })
  it('keeps the first visible row when the page size changes', () => {
    expect(pageForSize(3, 25, 50)).toBe(2) // row 51 is on page 2 of 50
    expect(pageForSize(2, 50, 25)).toBe(3) // row 51 is on page 3 of 25
    expect(pageForSize(1, 100, 25)).toBe(1)
  })
  it('lists page buttons with ellipses', () => {
    expect(pageRange(1, 1)).toEqual([1])
    expect(pageRange(4, 7)).toEqual([1, 2, 3, 4, 5, 6, 7])
    expect(pageRange(1, 10)).toEqual([1, 2, 3, 4, 5, null, 10])
    expect(pageRange(3, 10)).toEqual([1, 2, 3, 4, 5, null, 10])
    expect(pageRange(5, 10)).toEqual([1, null, 4, 5, 6, null, 10])
    expect(pageRange(8, 10)).toEqual([1, null, 6, 7, 8, 9, 10])
    expect(pageRange(10, 10)).toEqual([1, null, 6, 7, 8, 9, 10])
    for (let p = 1; p <= 40; p++) expect(pageRange(p, 40)).toHaveLength(7)
  })
})

describe('dialog validation', () => {
  it('validates users', () => {
    expect(validateUser({ name: 'Ada', role: 'user' }, { role: 'user', self: false })).toEqual({})
    expect(validateUser({ name: '  ', role: 'admin' }, { role: 'user', self: false })).toEqual({ name: 'Name is required' })
    expect(validateUser({ name: 'x'.repeat(101), role: 'user' }, { role: 'user', self: false }).name).toMatch(/too long \(101\/100/)
    expect(validateUser({ name: 'Me', role: 'user' }, { role: 'admin', self: true })).toEqual({ role: 'You cannot change your own role' })
    expect(validateUser({ name: 'Me', role: 'admin' }, { role: 'admin', self: true })).toEqual({})
  })
  it('validates groups: required, unique ignoring case and itself, short description', () => {
    const groups = [
      { id: 'g1', name: 'Admins' },
      { id: 'g2', name: 'Sales' },
    ]
    expect(validateGroup({ name: 'Ops', description: '' }, groups)).toEqual({})
    expect(validateGroup({ name: '', description: '' }, groups)).toEqual({ name: 'Name is required' })
    expect(validateGroup({ name: ' sales ', description: '' }, groups)).toEqual({ name: 'A group with that name already exists' })
    expect(validateGroup({ name: 'Sales', description: '' }, groups, 'g2')).toEqual({})
    expect(validateGroup({ name: 'Ops', description: 'd'.repeat(501) }, groups).description).toMatch(/too long/)
  })
  it('maps API error codes to fields', () => {
    expect([errorField('duplicate'), errorField('bad_name'), errorField('last_admin'), errorField('own_role'), errorField('boom'), errorField()]).toEqual(['name', 'name', 'role', 'role', undefined, undefined])
  })
  it('derives group icons and permission chips', () => {
    expect([groupIcon('Admin'), groupIcon('Backend developers'), groupIcon('QA team'), groupIcon('Everyone')]).toEqual(['shield', 'code', 'flask', 'users'])
    expect(permChips({ workspace: { models: true, tools: false }, features: { api_keys: true } })).toEqual(['Workspace models', 'Features API keys'])
    expect(permChips(null)).toEqual([])
  })
})

describe('spend with free models only', () => {
  it('counts people who made requests as active although nothing was charged', () => {
    const v = spendView({
      start_date: '2026-03-01',
      end_date: '2026-03-01',
      rows: [{ end_user: 'ada@x.io', model: 'local', day: '2026-03-01', spend: 0, prompt_tokens: 100, completion_tokens: 10, requests: 4 }],
      users: [
        { id: 'u1', name: 'Ada', email: 'ada@x.io', last_active_at: 1 },
        { id: 'u2', name: 'Bob', email: 'bob@x.io', last_active_at: 0 },
      ],
    })
    expect([v.total, v.active, v.idle]).toEqual([0, 1, 1])
    expect(v.days).toEqual([{ day: '2026-03-01', spend: 0, requests: 4 }])
  })
})
