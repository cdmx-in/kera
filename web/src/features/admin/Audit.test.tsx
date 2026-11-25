// The audit page shows names and outcomes, never a value, even if a payload carried one.
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AuditDetails, AuditEmpty, AuditRow } from './Audit'
import type { AuditEvent } from './audit'

const event = (o: Partial<AuditEvent> = {}): AuditEvent => ({
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
  user_agent: 'Mozilla/5.0 Test',
  meta: { changed: ['value'], value: 'leaked-value-123', password: 'hunter2-pass', api_key: 'sk-leakedkey' },
  ...o,
})

const row = (e: AuditEvent, open = false) =>
  renderToStaticMarkup(
    <ol>
      <AuditRow e={e} open={open} onToggle={() => {}} />
    </ol>,
  )

describe('AuditRow', () => {
  it('reads as one sentence with the actor and target, and a time with the exact moment on hover', () => {
    const h = row(event())
    expect(h).toContain('<strong class="font-semibold">priya@example.com</strong>')
    expect(h).toContain('replaced the value of the stored secret ')
    expect(h).toContain('<strong class="font-semibold">read-token</strong>')
    expect(h).toMatch(/<time datetime="2026-03-10T10:32:00Z" title="[^"]*2026[^"]*"/i)
    expect(h).toContain('aria-expanded="false"')
    expect(h).toContain('Secrets: ')
  })

  it('never renders a value, open or closed', () => {
    for (const open of [false, true]) {
      const h = row(event(), open)
      for (const leak of ['leaked-value', 'hunter2', 'sk-leaked']) expect(h).not.toContain(leak)
    }
  })

  it('marks refused and failed events in words, not only colour', () => {
    expect(row(event({ outcome: 'denied' }))).toContain('Refused')
    expect(row(event({ outcome: 'denied' }))).toContain('tried to replace the value')
    expect(row(event({ outcome: 'failed' }))).toContain('Failed')
    expect(row(event())).not.toContain('data-outcome')
  })

  it('says when an API key, an integration or the command line acted', () => {
    expect(row(event({ actor_kind: 'api_key' }))).toContain('API key')
    expect(row(event({ actor_kind: 'service', actor_label: 'Directory sync', action: 'user.disable', target_label: 'ann@example.com', meta: {} }))).toContain('Integration')
  })
})

describe('AuditDetails', () => {
  const h = renderToStaticMarkup(<AuditDetails e={event({ actor_kind: 'api_key' })} />)
  it('lists the structured record: action, who, target, address, client and meta names', () => {
    for (const want of ['secret.update', 'priya@example.com · API key · u1', 'secret · read-token', '203.0.113.5', 'Mozilla/5.0 Test', 'Changed', 'value']) expect(h).toContain(want)
  })
})

describe('AuditEmpty', () => {
  it('tells a filtered empty list from an empty log', () => {
    expect(renderToStaticMarkup(<AuditEmpty filtered />)).toContain('No events match these filters')
    expect(renderToStaticMarkup(<AuditEmpty filtered={false} />)).toContain('Nothing recorded yet')
  })
})
