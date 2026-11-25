// The runner token is shown once, in the dialog; the list only ever has its prefix.
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NoRunnerHint, OnlineBadge, RunnerTable, RunnerTokenDialog } from './PipelineRunners'
import type { Runner } from './pipelines'

const runner = (o: Partial<Runner> = {}): Runner => ({ id: 'kr1', name: 'build-server', token_prefix: 'kr-1a2b3c4', version: 'v1.4.0', last_seen_at: new Date(Date.now() - 30_000).toISOString(), online: true, created_at: '2026-03-01T00:00:00Z', ...o })
const TOKEN = 'kr-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d'

afterEach(() => vi.unstubAllGlobals())

describe('RunnerTokenDialog', () => {
  it('shows the whole token and an env snippet with this address', () => {
    vi.stubGlobal('location', { origin: 'https://kera.example.com' })
    const h = renderToStaticMarkup(<RunnerTokenDialog name="build-server" token={TOKEN} onClose={() => {}} />)
    expect(h).toContain(TOKEN)
    expect(h).toContain('KERA_URL=https://kera.example.com')
    expect(h).toContain(`KERA_RUNNER_TOKEN=${TOKEN}`)
    expect(h).toContain('shown once')
    expect(h).toContain('aria-label="Copy token"')
  })
})

describe('RunnerTable', () => {
  const table = (rs: Runner[]) => renderToStaticMarkup(<RunnerTable runners={rs} onRotate={() => {}} onDelete={() => {}} />)
  it('lists name, status, version and the token prefix only', () => {
    const h = table([runner(), runner({ id: 'kr2', name: 'spare', online: false, last_seen_at: null, version: '' })])
    expect(h).toContain('build-server')
    expect(h).toContain('Online')
    expect(h).toContain('v1.4.0')
    expect(h).toContain('kr-1a2b3c4…')
    expect(h).toContain('Never connected')
    expect(h.match(/data-testid="runner-/g)).toHaveLength(2)
  })
  it('never prints a full token, even if a payload carries one', () => {
    const h = table([{ ...runner(), token: TOKEN } as Runner])
    expect(h).not.toContain(TOKEN)
    expect(h).not.toContain('d5e6f708')
  })
  it('names the rotate and delete buttons for their runner', () => {
    const h = table([runner()])
    expect(h).toContain('aria-label="Rotate token of build-server"')
    expect(h).toContain('aria-label="Delete build-server"')
  })
})

describe('OnlineBadge', () => {
  it('says Online or Offline in words', () => {
    expect(renderToStaticMarkup(<OnlineBadge runner={runner()} />)).toContain('Online')
    expect(renderToStaticMarkup(<OnlineBadge runner={runner({ online: false })} />)).toContain('Offline')
  })
})

describe('NoRunnerHint', () => {
  const hint = (rs: Runner[] | undefined) => renderToStaticMarkup(<MemoryRouter><NoRunnerHint runners={rs} /></MemoryRouter>)
  it('warns when no runner is online and links to the Runners page', () => {
    const h = hint([runner({ online: false })])
    expect(h).toContain('No runner is connected, so scheduled runs will fail after 10 minutes. Start the runner service (docker compose up -d runner) or create one under Runners.')
    expect(h).toContain('href="/admin/pipelines?runners"')
    expect(hint([])).toContain('No runner is connected')
  })
  it('says nothing while the runners are not known, or when one is online', () => {
    expect(hint(undefined)).toBe('')
    expect(hint([runner(), runner({ id: 'x', name: 'other', online: false })])).toBe('')
  })
})
