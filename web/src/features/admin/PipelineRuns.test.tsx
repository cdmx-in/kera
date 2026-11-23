// Render checks for the flow run view: static markup only (no browser), so they pin what a reviewer reads in the code.
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { NavigateFunction } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { Flow, History, leaveStep, Notice, StepBody } from './PipelineRuns'
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
    expect(h).toContain('grid-cols-[repeat(auto-fill,minmax(11rem,1fr))]')
    expect(h.match(/data-testid="flow-node"/g)).toHaveLength(20)
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
  const run = (o: Partial<Run>): Run => ({ id: 1, pipeline: 'docs-sync', trigger: 'script', dry_run: false, status: 'ok', client_done: true, error: '', started_at: at('00:00'), active_at: at('00:00'), finished_at: at('02:00'), counts: {}, ...o })
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
