// A secret variable is write-only: no markup of the environment tab may carry its value.
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AddEnv, EnvRow } from './PipelineEnv'
import type { EnvVar } from './pipelines'

const var_ = (o: Partial<EnvVar>): EnvVar => ({ name: 'API_TOKEN', secret: true, value: null, updated_at: '2026-03-10T06:00:00Z', ...o })
const row = (v: EnvVar) =>
  renderToStaticMarkup(
    <table>
      <tbody>
        <EnvRow v={v} onSave={async () => {}} onDelete={() => {}} />
      </tbody>
    </table>,
  )

describe('EnvRow', () => {
  it('says a secret is set and offers Update, with no input open and no value', () => {
    const h = row(var_({}))
    expect(h).toContain('set')
    expect(h).toContain('aria-label="Update API_TOKEN"')
    expect(h).toContain('Secret')
    expect(h).not.toContain('<input')
  })
  it('never renders a secret value, even if a payload carries one', () => {
    const h = row(var_({ value: 'hunter2-the-real-value' }))
    expect(h).not.toContain('hunter2')
    expect(h).toContain('data-secret="true"')
  })
  it('shows a plain value and lets it be edited', () => {
    const h = row(var_({ name: 'REGION', secret: false, value: 'eu-west-1' }))
    expect(h).toContain('eu-west-1')
    expect(h).toContain('aria-label="Edit REGION"')
    expect(h).toContain('Plain')
  })
  it('says so when a plain value is empty', () => {
    expect(row(var_({ name: 'FLAG', secret: false, value: '' }))).toContain('(empty)')
  })
  it('names the delete button for the variable', () => {
    expect(row(var_({}))).toContain('aria-label="Delete API_TOKEN"')
  })
})

describe('AddEnv', () => {
  const h = renderToStaticMarkup(<AddEnv existing={['REGION']} onAdd={async () => {}} />)
  it('starts with Secret ticked and the value hidden', () => {
    expect(h).toMatch(/type="checkbox"[^>]*checked=""[^>]*\/>Secret/)
    expect(h).toContain('type="password"')
  })
  it('labels its inputs and says what KERA_ is for', () => {
    for (const l of ['Name', 'Value']) expect(h).toContain(l)
    expect(h).toContain('KERA_URL, KERA_PIPELINE, KERA_PIPELINE_TOKEN, KERA_RUN, KERA_MARK, KERA_SCRIPT_DIR and KERA_TEST are set by the runner.')
    expect(h).toContain('aria-label="Add variable"')
  })
})
