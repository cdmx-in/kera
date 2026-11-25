import { describe, expect, it } from 'vitest'
import { auditQuery, catalogue, categoryOf, metaRows, names, past, secretKey, sentence, when, type AuditEvent } from './audit'

const ev = (o: Partial<AuditEvent> = {}): AuditEvent => ({
  id: 'e1',
  at: '2026-03-10T10:32:00Z',
  actor_id: 'u1',
  actor_label: 'priya@example.com',
  actor_kind: 'user',
  action: 'secret.update',
  target_type: 'secret',
  target_id: 'read-token',
  target_label: 'read-token',
  outcome: 'ok',
  ip: '203.0.113.5',
  user_agent: 'Firefox',
  meta: {},
  ...o,
})

describe('sentence', () => {
  it('reads as a plain sentence for every action in the catalogue, done or refused', () => {
    for (const action of Object.keys(catalogue)) {
      for (const outcome of ['ok', 'denied', 'failed'] as const) {
        const s = sentence(ev({ action, outcome, target_label: 'Thing-X', target_id: 'thing-x' }))
        expect(s, action).toMatch(/^priya@example\.com [a-z]/)
        expect(s, action).not.toMatch(/\{t\}|\{s\}|undefined|NaN|null|\s{2}|\s$/)
        if (outcome === 'ok') expect(s, action).not.toContain('tried to')
        else expect(s, action).toContain(' tried to ')
        if (catalogue[action].say.toString().includes('{t}')) expect(s, action).toContain('Thing-X')
      }
    }
  })

  it('says what changed, in words', () => {
    expect(sentence(ev({ meta: { changed: ['value'] } }))).toBe('priya@example.com replaced the value of the stored secret read-token')
    expect(sentence(ev({ meta: { changed: ['description'] } }))).toBe('priya@example.com edited the description of the stored secret read-token')
    expect(sentence(ev({ action: 'user.role_change', target_type: 'user', target_label: 'pat@example.com', meta: { from: 'user', to: 'admin' } }))).toBe(
      'priya@example.com changed the role of pat@example.com from user to admin',
    )
    expect(sentence(ev({ action: 'settings.update', target_label: '', target_id: '', meta: { settings: ['site.name', 'site.timezone'] } }))).toBe(
      'priya@example.com changed the settings site.name and site.timezone',
    )
    expect(sentence(ev({ action: 'group.member_add', target_label: 'Finance', meta: { users: ['a', 'b'] } }))).toBe('priya@example.com added 2 people to the group Finance')
    expect(sentence(ev({ action: 'pipeline.env_set', target_label: 'nightly', meta: { variable: 'API_TOKEN', secret_ref: 'read-token' } }))).toBe(
      'priya@example.com set the variable API_TOKEN of the pipeline nightly to the stored secret read-token',
    )
    expect(sentence(ev({ action: 'pipeline.run', target_label: 'nightly', outcome: 'denied', actor_label: 'pat@example.com' }))).toBe('pat@example.com tried to run the pipeline nightly')
  })

  it('names the anonymous and non-human actors plainly', () => {
    const failed = ev({ action: 'auth.sign_in_failed', actor_kind: 'anonymous', actor_id: '', actor_label: 'ann@example.com', outcome: 'denied', target_id: 'u9', target_label: 'ann@example.com', meta: { method: 'password', reason: 'wrong password' } })
    expect(sentence(failed)).toBe('Someone tried to sign in as ann@example.com (wrong password)')
    expect(sentence({ ...failed, target_id: '', target_label: 'email#1a2b3c4d', meta: { reason: 'no such user' } })).toBe('Someone tried to sign in with an unknown email (no such user)')
    expect(sentence({ ...failed, target_id: '', target_label: '', meta: { method: 'google', reason: 'invalid_state' } })).toBe('Someone tried to sign in (invalid state)')
    expect(sentence(ev({ action: 'config.apply', actor_kind: 'system', actor_id: '', actor_label: 'Kera command line', target_label: '', meta: { created: 2, updated: 1, deleted: 0 } }))).toBe(
      'Kera command line applied the config file (2 created, 1 updated, 0 deleted)',
    )
    expect(sentence(ev({ action: 'auth.sign_in', target_label: '', target_id: '', meta: { method: 'google' } }))).toBe('priya@example.com signed in through google')
  })

  it('falls back for an action it does not know, and for an unnamed admin route', () => {
    expect(sentence(ev({ action: 'widget.frobnicate', target_label: 'W1' }))).toBe('priya@example.com did widget.frobnicate on W1')
    expect(sentence(ev({ action: 'widget.frobnicate', target_label: '', target_id: '' }))).toBe('priya@example.com did widget.frobnicate')
    expect(sentence(ev({ action: 'admin.request', target_label: '', target_id: 'x', meta: { method: 'POST', route: '/api/v1/things/{id}' } }))).toBe(
      'priya@example.com made an admin change (POST /api/v1/things/{id})',
    )
    expect(categoryOf('widget.frobnicate')).toBe('other')
  })
})

describe('helpers', () => {
  it('puts the verb in the past', () => {
    expect(['delete x', 'add x', 'apply x', 'run x', 'set x', 'stop x', 'sign in', 'cancel x'].map(past)).toEqual(['deleted x', 'added x', 'applied x', 'ran x', 'set x', 'stopped x', 'signed in', 'cancelled x'])
  })
  it('lists names briefly', () => {
    expect([names([]), names(['a']), names(['a', 'b']), names(['a', 'b', 'c']), names(['a', 'b', 'c', 'd', 'e'])]).toEqual(['', 'a', 'a and b', 'a, b and c', 'a, b and 3 more'])
  })
  it('groups actions by what they touch', () => {
    expect(['auth.sign_in', 'apikey.create', 'user.delete', 'group.member_add', 'secret.update', 'pipeline.env_set', 'runner.create', 'settings.update', 'config.apply', 'chat.share', 'config.export'].map(categoryOf)).toEqual([
      'signin',
      'signin',
      'users',
      'users',
      'secrets',
      'pipelines',
      'pipelines',
      'settings',
      'settings',
      'data',
      'data',
    ])
  })
  it('tells the time relative to now', () => {
    const now = new Date(2026, 2, 10, 15, 0)
    const at = (d: Date) => when(d.toISOString(), now)
    expect(at(new Date(2026, 2, 10, 14, 59, 30))).toBe('just now')
    expect(at(new Date(2026, 2, 10, 14, 48))).toBe('12 min ago')
    expect(at(new Date(2026, 2, 10, 10, 32))).toBe('10:32 today')
    expect(at(new Date(2026, 2, 9, 23, 5))).toBe('23:05 yesterday')
    expect(at(new Date(2026, 2, 6, 9, 0))).toBe('Fri 09:00')
    expect(at(new Date(2026, 0, 3, 9, 0))).toBe('3 Jan, 09:00')
    expect(at(new Date(2025, 10, 3, 9, 0))).toBe('3 Nov 2025, 09:00')
  })
  it('hides secret-looking meta like the server does', () => {
    expect(['password', 'api_key', 'tokens', 'value', 'client_secret', 'content'].every(secretKey)).toBe(true)
    expect(['secret_ref', 'changed', 'variable', 'settings'].some(secretKey)).toBe(false)
    expect(metaRows({ changed: ['value'], value: 'leak', secret_ref: 'read-token', write_only: true })).toEqual([
      ['changed', 'value'],
      ['secret ref', 'read-token'],
      ['write only', 'yes'],
    ])
  })
  it('turns the filters into the query the server reads', () => {
    const p = auditQuery({ actor: ' pat ', category: 'signin', outcome: 'denied,failed', from: '2026-03-01', to: '2026-03-02', q: '' })
    expect(p.get('actor')).toBe('pat')
    expect(p.get('action')).toBe('auth.,apikey.')
    expect(p.get('outcome')).toBe('denied,failed')
    expect(new Date(p.get('to')!).getTime() - new Date(p.get('from')!).getTime()).toBe(2 * 86400_000) // the to day is whole
    expect(p.has('q')).toBe(false)
  })
})
