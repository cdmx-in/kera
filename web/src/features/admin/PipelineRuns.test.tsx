// Render checks for the flow run view: static markup only (no browser), so they pin what a reviewer reads in the code.
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { NavigateFunction } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { Flow, History, leaveStep, Notice, RunHeader, runWhere, StepBody } from './PipelineRuns'
import { CopyBlock, WhereItRuns } from './Pipelines'
import type { Run, Step } from './pipelines'

const at = (s: string) => `2026-03-10T06:${s}Z`
const now = new Date(at('10:00')).getTime()

const step = (o: Partial<Step>): Step => ({ seq: 1, kera: false, key: 'fetch', name: 'fetch', status: 'ok', started_at: at('00:00'), finished_at: at('00:12'), error: '', counts: {}, log_dropped: false, ...o })

const steps: Step[] = [
  step({ seq: 1, name: 'fetch', counts: { files: 25 } }),
  step({ seq: 2, name: 'convert', status: 'failed', error: 'exit code 1' }),
  step({ seq: 3, name: 'upload', status: 'skipped', started_at: null, finished_at: null }),
  step({ seq: 1000, kera: true, key: 'k1', name: 'Product docs', status: 'running', finished_at: null, counts: { listed: 25, unchanged: 22, uploaded: 3, waiting: 2 } }),
  step({ seq: 1001, kera: true, key: 'k2', name: 'Guides', status: 'pending', started_at: null, finished_at: null }),
]

describe('CopyBlock', () => {
  it('wraps a long command instead of scrolling it sideways under the Copy button', () => {
    const html = renderToStaticMarkup(<CopyBlock text="kera pipeline push --collection 'Product docs' --dir ./docs" label="command" />)
    expect(html).toContain('whitespace-pre-wrap')
    expect(html).not.toContain('overflow-x-auto')
  })
})

describe('Flow', () => {
  const html = renderToStaticMarkup(<Flow steps={steps} seq={2} now={now} onOpen={() => {}} />)
  const nodes = [...html.matchAll(/<button[^>]*data-testid="flow-node"[^>]*>.*?<\/button>/g)].map((m) => m[0])

  it('is one real button per step, script steps first, then the collections', () => {
    expect(nodes).toHaveLength(5)
    expect(nodes.map((n) => /data-status="(\w+)"/.exec(n)![1])).toEqual(['ok', 'failed', 'skipped', 'running', 'pending'])
    expect(html.indexOf('aria-label="Script steps"')).toBeLessThan(html.indexOf('>Kera<'))
    expect(html).toContain('<ol aria-label="Script steps"')
    expect(html).toContain('role="group"')
  })
  it('marks only the open step as pressed', () => {
    expect(nodes.filter((n) => n.includes('aria-pressed="true"'))).toHaveLength(1)
    expect(nodes[1]).toContain('aria-pressed="true"')
  })
  it('says the status in words, never by colour alone', () => {
    for (const [i, word] of ['OK', 'Failed', 'Skipped', 'Running', 'Pending'].entries()) expect(nodes[i]).toContain(word)
    expect(nodes[2]).toContain('border-dashed')
  })
  it('shows the duration and the main count', () => {
    expect(nodes[0]).toContain('12 s')
    expect(nodes[0]).toContain('25 files')
    expect(nodes[3]).toContain('2 waiting')
    expect(nodes[3]).toContain('10 min') // still running: counted to now
  })
  it('clamps a long name to two lines instead of cutting it to one, and lays many collections out as a wrapping grid', () => {
    const many = Array.from({ length: 20 }, (_, i) => step({ seq: 1000 + i, kera: true, key: `k${i}`, name: `Collection number ${i} of the product documentation` }))
    const h = renderToStaticMarkup(<Flow steps={many} seq={null} now={now} onOpen={() => {}} />)
    expect(h).toContain('line-clamp-2')
    expect(h).not.toContain('truncate font-medium')
    expect(h).toContain('grid-cols-[repeat(auto-fill,minmax(14rem,1fr))]')
    expect(h.match(/data-testid="flow-node"/g)).toHaveLength(20)
  })
  it('puts the implicit start step first, even when an older run recorded it last', () => {
    const late = [step({ seq: 1, name: 'secret-scan' }), step({ seq: 2, name: 'kera' }), step({ seq: 3, name: 'start' })]
    const h = renderToStaticMarkup(<Flow steps={late} seq={null} now={now} onOpen={() => {}} />)
    const order = [...h.matchAll(/data-testid="flow-node"[^>]*>.*?title="([^"]+)"/g)].map((m) => m[1])
    expect(order).toEqual(['start', 'secret-scan', 'kera'])
  })
  it('shows a count chip per number that matters, most telling first', () => {
    expect(nodes[3]).toMatch(/2 waiting.*3 changed/)
  })
  it('draws no script row when a run only has collections', () => {
    const only = renderToStaticMarkup(<Flow steps={steps.filter((s) => s.kera)} seq={null} now={now} onOpen={() => {}} />)
    expect(only).not.toContain('Script steps')
    expect(only).toContain('Product docs')
  })
})

describe('StepBody', () => {
  const failed = step({ status: 'failed', error: 'kept the previous files', counts: { listed: 25, refused: 3 }, log: '06:00:01 start\n06:00:09 boom\n', log_dropped: true })
  const html = renderToStaticMarkup(<StepBody step={failed} now={now} />)

  it('reads origin, status and timing, then the error, then counts, then the log', () => {
    const order = ['Script step', 'Failed', 'Duration', 'role="alert"', 'Counts', 'Log'].map((s) => html.indexOf(s))
    expect(order.every((n) => n >= 0)).toBe(true)
    expect(order).toEqual([...order].sort((a, b) => a - b))
    expect(html).toContain('kept the previous files')
  })
  it('lists counts as a dl with words, and the log in a pre with the 256 KiB note and Copy', () => {
    expect(html).toContain('<dl')
    expect(html).toContain('Removals refused')
    expect(html).toContain('<pre')
    expect(html).toContain('06:00:09 boom')
    expect(html).toContain('Showing the last 256 KiB. Times are UTC. Earlier lines were dropped.')
    expect(html).toContain('aria-label="Copy log"')
  })
  it('says so when a step has no log, and labels a collection step', () => {
    const none = renderToStaticMarkup(<StepBody step={step({ kera: true, status: 'pending', started_at: null, finished_at: null, log: '' })} now={now} />)
    expect(none).toContain('Kera · collection')
    expect(none).toContain('No log lines yet.')
    expect(none).not.toContain('<pre')
    expect(none).not.toContain('role="alert"')
  })
})

describe('StepBody while the log is not here', () => {
  const noLog = step({ status: 'ok' }) // the run payload has no `log` key
  it('says it is loading, never that the step wrote nothing', () => {
    const h = renderToStaticMarkup(<StepBody step={noLog} now={now} />)
    expect(h).toContain('Loading log…')
    expect(h).not.toContain('This step wrote no log.')
  })
  it('says so when the request failed', () => {
    const h = renderToStaticMarkup(<StepBody step={noLog} now={now} loadFailed />)
    expect(h).toContain('Could not load the log.')
    expect(h).not.toContain('Loading log…')
  })
  it('still says a finished step wrote no log once the log has arrived empty', () => {
    expect(renderToStaticMarkup(<StepBody step={step({ log: '' })} now={now} />)).toContain('This step wrote no log.')
  })
})

describe('Notice', () => {
  it('gives its actions the theme foreground (a Btn sets only a background) and lets them wrap under the text', () => {
    const h = renderToStaticMarkup(
      <Notice tone="red" actions={<button type="button">Open step</button>}>
        Run failed
      </Notice>,
    )
    expect(h).toContain('<div class="text-fg"><button type="button">Open step</button></div>')
    expect(h).toContain('min-w-[min(100%,14rem)]')
    expect(renderToStaticMarkup(<Notice tone="grey">Off</Notice>)).not.toContain('text-fg')
  })
})

describe('leaveStep', () => {
  it('goes Back when this page pushed the step, so one Back press is enough', () => {
    const nav = vi.fn()
    leaveStep(nav as unknown as NavigateFunction, { drawer: true }, '/admin/pipelines/docs-sync/12')
    expect(nav.mock.calls).toEqual([[-1]])
  })
  it('drops ?step= in place for a deep link or a reload without state', () => {
    for (const state of [null, undefined, {}]) {
      const nav = vi.fn()
      leaveStep(nav as unknown as NavigateFunction, state, '/admin/pipelines/docs-sync/12')
      expect(nav.mock.calls).toEqual([['/admin/pipelines/docs-sync/12', { replace: true }]])
    }
  })
})

describe('History', () => {
  const run = (o: Partial<Run>): Run => ({ id: 1, pipeline: 'docs-sync', trigger: 'script', dry_run: false, status: 'ok', client_done: true, error: '', queued: false, cancel_requested: false, started_at: at('00:00'), active_at: at('00:00'), finished_at: at('02:00'), counts: {}, ...o })
  const runs = [run({ id: 12, status: 'running', finished_at: null }), run({ id: 11, counts: { uploaded: 4, removed: 1 } }), run({ id: 10, status: 'failed', trigger: 'push', dry_run: true })]
  const html = (more: boolean) => renderToStaticMarkup(<MemoryRouter><History runs={runs} loading={false} more={more} selected={11} name="docs-sync" now={now} onMore={() => {}} /></MemoryRouter>)

  it('links every run with its status word, summary, trigger and dry-run badge', () => {
    const h = html(false)
    expect(h.match(/data-testid="run-item"/g)).toHaveLength(3)
    expect(h).toContain('href="/admin/pipelines/docs-sync/11"')
    expect(h).toContain('4 changed, 1 removed')
    expect(h).toContain('In progress')
    expect(h).toContain('Dry run')
    expect(h).toContain('Push')
    for (const word of ['Running', 'OK', 'Failed']) expect(h).toContain(word)
  })
  it('shows why a failed run failed, not No changes', () => {
    const failed = [run({ id: 9, status: 'failed', error: 'convert exited with code 3', counts: {} })]
    const h = renderToStaticMarkup(<MemoryRouter><History runs={failed} loading={false} more={false} selected={9} name="docs-sync" now={now} onMore={() => {}} /></MemoryRouter>)
    expect(h).toContain('convert exited with code 3')
    expect(h).not.toContain('No changes')
  })
  it('marks the selected run and offers Load older only when there are more', () => {
    expect(html(false).match(/aria-current="page"/g)).toHaveLength(1)
    expect(html(false)).not.toContain('Load older')
    expect(html(true)).toContain('Load older')
  })
})

describe('WhereItRuns', () => {
  const run = { started_at: new Date(Date.now() - 3600_000).toISOString() }
  it('shows machine, folder and command, and says Kera does not start it', () => {
    const html = renderToStaticMarkup(<WhereItRuns run={{ ...run, source: { host: 'build-01.example.com', user: 'root', dir: '/srv/kb/docs', command: 'bash refresh.sh', client: 'kera v1.2.1' } }} />)
    for (const s of ['root@build-01.example.com', '/srv/kb/docs', 'bash refresh.sh', 'kera v1.2.1', 'Kera does not start this']) expect(html).toContain(s)
  })
  it('says so when nothing was reported (a pipeline that never ran, or an older run)', () => {
    expect(renderToStaticMarkup(<WhereItRuns run={null} />)).toContain('Not reported yet')
    expect(renderToStaticMarkup(<WhereItRuns run={{ ...run, source: {} }} />)).toContain('Not reported yet')
  })
})

describe('runWhere', () => {
  it('joins who and the command, and is empty without a source', () => {
    expect(runWhere({ source: { user: 'root', host: 'rad', command: 'bash x.sh' } })).toBe('root@rad: bash x.sh')
    expect(runWhere({ source: { command: 'bash x.sh' } })).toBe('bash x.sh')
    expect(runWhere({})).toBe('')
  })
})

describe('Kera-run runs', () => {
  const run = (o: Partial<Run>): Run => ({ id: 1, pipeline: 'docs-sync', trigger: 'schedule', dry_run: false, status: 'running', client_done: false, error: '', queued: false, cancel_requested: false, started_at: at('00:00'), active_at: at('00:00'), finished_at: null, counts: {}, ...o })
  const history = (runs: Run[]) => renderToStaticMarkup(<MemoryRouter><History runs={runs} loading={false} more={false} selected={undefined} name="docs-sync" now={now} onMore={() => {}} /></MemoryRouter>)

  it('shows a queued run as Queued, waiting for a runner, not as Running', () => {
    const h = history([run({ id: 5, queued: true })])
    expect(h).toContain('Queued')
    expect(h).toContain('Queued: waiting for a runner')
    expect(h).not.toContain('Running')
    expect(h).not.toContain('In progress')
    expect(h).toContain('data-queued="true"')
  })
  it('shows who started a run and which script version it used', () => {
    const h = history([run({ id: 4, status: 'ok', finished_at: at('01:00'), script_version: 3 }), run({ id: 3, trigger: 'manual', status: 'ok', finished_at: at('01:00') }), run({ id: 2, trigger: 'script', status: 'ok', finished_at: at('01:00') })])
    expect(h).toContain('Schedule')
    expect(h).toContain('script v3')
    expect(h).toContain('Manual')
    expect(h).toContain('Script')
    expect(h.match(/script v/g)).toHaveLength(1) // only the run that used a stored script
  })
  it('says Stopping when a stop was asked for', () => {
    expect(history([run({ id: 6, script_version: 2, cancel_requested: true })])).toContain('Stopping…')
  })
})

describe('RunHeader', () => {
  const run = (o: Partial<Run>): Run => ({ id: 7, pipeline: 'docs-sync', trigger: 'manual', dry_run: false, status: 'running', client_done: false, error: '', queued: false, cancel_requested: false, started_at: at('00:00'), active_at: at('00:00'), finished_at: null, counts: {}, ...o })
  const header = (r: Run, o: { runner?: string; cancel?: boolean } = {}) => renderToStaticMarkup(<RunHeader run={r} now={now} runner={o.runner} onCancel={o.cancel === false ? undefined : () => {}} />)

  it('has the trigger, script version and runner name in the header line', () => {
    const h = header(run({ script_version: 3, runner: 'kr1' }), { runner: 'build-server' })
    expect(h).toContain('Manual')
    expect(h).toContain('script v3')
    expect(h).toContain('Runner build-server')
  })
  it('reads Queued with a Cancel run button before a runner claims it', () => {
    const h = header(run({ queued: true }))
    expect(h).toContain('Queued')
    expect(h).toContain('Cancel run')
    expect(h).not.toContain('animate-spin')
    expect(h).not.toContain('Running')
  })
  it('turns the button into a disabled Stopping once a stop is asked for', () => {
    const h = header(run({ script_version: 3, cancel_requested: true }))
    expect(h).toMatch(/<button[^>]*disabled=""[^>]*data-testid="cancel-run"|<button[^>]*data-testid="cancel-run"[^>]*disabled=""/)
    expect(h).toContain('Stopping…')
    expect(h).not.toContain('Cancel run')
  })
  it('offers no Cancel for a run a script reports itself, or one that is over', () => {
    expect(header(run({ trigger: 'script' }))).not.toContain('Cancel run')
    expect(header(run({ script_version: 3, status: 'ok', finished_at: at('01:00') }))).not.toContain('Cancel run')
  })
})

describe('WhereItRuns for a Kera-run pipeline', () => {
  it('says the script version and runner come from Kera, not that cron starts it', () => {
    const h = renderToStaticMarkup(<WhereItRuns kera run={{ started_at: at('00:00'), source: { user: 'build-server', host: 'runner-1', dir: '/work/docs-sync', command: 'run.sh --full', client: 'kera runner v1.3.0' } }} />)
    expect(h).toContain('Run by Kera: the script version and the runner come from Kera')
    expect(h).toContain('build-server@runner-1')
    expect(h).not.toContain('Kera does not start this')
  })
})
