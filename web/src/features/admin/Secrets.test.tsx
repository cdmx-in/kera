// A stored secret is write-only: no markup of the Secrets page may carry its value.
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AddSecret, randomSecret, SecretRow, UsedBy } from './Secrets'
import { validateSecret, type Secret } from './pipelines'

const secret = (o: Partial<Secret> = {}): Secret => ({
  name: 'shared-key',
  description: 'read access',
  used_by: [{ pipeline: 'nightly', variable: 'API_KEY' }],
  updated_by: 'admin@example.com',
  updated_at: '2026-03-10T06:00:00Z',
  last_used_at: null,
  ...o,
})
const row = (s: Secret) =>
  renderToStaticMarkup(
    <MemoryRouter>
      <table>
        <tbody>
          <SecretRow s={s} onSave={async () => {}} onDelete={() => {}} />
        </tbody>
      </table>
    </MemoryRouter>,
  )

describe('SecretRow', () => {
  it('lists the name, description, who uses it and when it was last used, with no input open', () => {
    const h = row(secret())
    expect(h).toContain('shared-key')
    expect(h).toContain('read access')
    expect(h).toContain('nightly / API_KEY')
    expect(h).toContain('href="/admin/pipelines/nightly?tab=env"')
    expect(h).toContain('Never')
    expect(h).not.toContain('<input')
  })
  it('never renders a value, even if a payload carries one', () => {
    const h = row({ ...secret(), value: 'leaked-value-123', value_enc: 'abc' } as Secret)
    expect(h).not.toContain('leaked-value')
    expect(h).not.toContain('value_enc')
  })
  it('names its buttons for the secret', () => {
    const h = row(secret())
    for (const l of ['Update value of shared-key', 'Edit description of shared-key', 'Delete shared-key']) expect(h).toContain(`aria-label="${l}"`)
  })
})

describe('UsedBy', () => {
  it('says so when nothing uses the secret', () => {
    expect(renderToStaticMarkup(<UsedBy list={[]} />)).toContain('Not used')
  })
})

describe('AddSecret', () => {
  const h = renderToStaticMarkup(<AddSecret onAdd={async () => {}} />)
  it('hides the value field and offers Generate random', () => {
    expect(h).toContain('type="password"')
    expect(h).toContain('Generate random')
    expect(h).toContain('aria-label="Add secret"')
    expect(h).toContain('never shown again')
  })
})

describe('randomSecret', () => {
  it('is 32 random bytes in hex, new every time', () => {
    const a = randomSecret()
    expect(a).toMatch(/^[0-9a-f]{64}$/)
    expect(randomSecret()).not.toBe(a)
  })
})

describe('validateSecret', () => {
  it('accepts names with dashes, dots and underscores and refuses the rest', () => {
    expect(validateSecret({ name: 'svc.read-token_1', value: 'x' })).toEqual({})
    for (const bad of ['', 'Upper', 'has space', '-lead', 'a'.repeat(101)]) expect(validateSecret({ name: bad, value: 'x' }).name).toBeTruthy()
  })
  it('needs a value', () => {
    expect(validateSecret({ name: 'ok', value: '' }).value).toBeTruthy()
  })
})
