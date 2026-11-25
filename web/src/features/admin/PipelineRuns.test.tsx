// Render checks for the run view: static markup only (no browser), so they pin what a reviewer reads in the code.
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { HealthStrip } from './HealthStrip'
import { KeraSyncs } from './KeraSyncs'
import { History, Notice } from './PipelineRuns'
import { runWhere, RunSummary } from './RunSummary'
import { StagesEditor } from './StagesEditor'
import { StatusChip, statusWord } from './status'
import { StepDetail, type StepCtx } from './StepLog'
import { StepWaterfall } from './StepWaterfall'
import { CopyBlock, StatusBadge, WhereItRuns } from './Pipelines'
import type { Run, Step } from './pipelines'
import { axisSpan, scriptSteps, keraSteps } from './runview'

const at = (s: string) => `2026-03-10T06:${s}Z`
const now = new Date(at('10:00')).getTime()

const step = (o: Partial<Step>): Step => ({ seq: 1, kera: false, key: 'fetch', name: 'fetch', status: 'ok', started_at: at('00:00'), finished_at: at('00:12'), error: '', counts: {}, log_dropped: false, ...o })
const mkRun = (o: Partial<Run> = {}): Run => ({ id: 7, pipeline: 'docs-sync', trigger: 'manual', dry_run: false, status: 'running', client_done: false, error: '', queued: false, cancel_requested: false, started_at: at('00:00'), active_at: at('00:00'), finished_at: null, counts: {}, ...o })

const steps: Step[] = [
  step({ seq: 1, name: 'fetch-docs', counts: { files: 25 } }),
  step({ seq: 2, name: 'convert', status: 'failed', started_at: at('00:12'), finished_at: at('01:12'), error: 'exit code 1' }),
  step({ seq: 3, name: 'upload', status: 'skipped', started_at: null, finished_at: null }),
  step({ seq: 1000, kera: true, key: 'k1', name: 'Product docs', status: 'running', started_at: at('01:12'), finished_at: null, counts: { listed: 25, unchanged: 22, uploaded: 3, waiting: 2 } }),
  step({ seq: 1001, kera: true, key: 'k2', name: 'Guides', status: 'pending', started_at: null, finished_at: null }),
]
const stages = [{ name: 'fetch-docs', title: 'Fetch documents', about: 'Pulls the latest documents.' }]
const ctx: StepCtx = { pipeline: 'docs-sync', runId: 7, stages, now, editHref: '/admin/pipelines/docs-sync/edit#stages' }
// StepDetail holds a query for the log, so it needs a client; the markup is the pending state
const withClient = (ui: React.ReactElement) =>
  renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
const inRouter = (ui: React.ReactElement) => renderToStaticMarkup(<MemoryRouter>{ui}</MemoryRouter>)

describe('CopyBlock', () => {
  it('wraps a long command instead of scrolling it sideways under the Copy button', () => {
    const html = renderToStaticMarkup(<CopyBlock text="kera pipeline push --collection 'Product docs' --dir ./docs" label="command" />)
    expect(html).toContain('whitespace-pre-wrap')
    expect(html).not.toContain('overflow-x-auto')
  })
})

describe('StatusChip', () => {
  it('is an icon and a word for every status, upper-cased by the CSS, never colour alone', () => {
    for (const s of ['queued', 'running', 'stopping', 'ok', 'warning', 'failed', 'skipped', 'pending', 'cancelled', 'dry', 'notebook', 'late', 'never', 'off'] as const) {
      const h = renderToStaticMarkup(<StatusChip status={s} />)
      expect(h).toContain('uppercase')
      expect(h).toContain('<svg')
      expect(h).toContain(statusWord(s))
    }
  })
  it('turns the running icon only while it runs, and uses the same chip on the list page', () => {
    expect(renderToStaticMarkup(<StatusChip status="running" />)).toContain('animate-spin')
    expect(renderToStaticMarkup(<StatusChip status="ok" />)).not.toContain('animate-spin')
    const h = renderToStaticMarkup(<StatusBadge status="failed" />)
    expect(h).toContain('Failed')
    expect(h).toContain('uppercase')
  })
  it('stands still for a reader who asked for less motion', () => {
    expect(renderToStaticMarkup(<StatusChip status="running" />)).not.toMatch(/(^|[ "])animate-spin/)
  })
})

describe('StepWaterfall', () => {
  const run = mkRun({ status: 'failed', finished_at: at('01:12') })
  const script = scriptSteps(steps)
  const html = (open: (s: Step) => boolean = () => false) =>
    withClient(<StepWaterfall run={run} steps={script} ctx={ctx} span={axisSpan(run, script, now)} isOpen={open} onToggle={() => {}} flash={null} />)
  const rows = (h: string) => [...h.matchAll(/<li id="step-\d+"[^>]*data-status="(\w+)"/g)].map((m) => m[1])

  it('is a real list with one button per step, and only the script steps', () => {
    const h = html()
    expect(h).toContain('<ol aria-label="Script steps"')
    expect(rows(h)).toEqual(['ok', 'failed', 'skipped'])
    expect(h.match(/<button[^>]*aria-expanded/g)).toHaveLength(3)
  })
  it('says the status in an icon and an upper-case word on every row', () => {
    const h = html()
    for (const w of ['OK', 'Failed', 'Skipped']) expect(h).toContain(w)
    expect(h.match(/data-status-chip/g)).toHaveLength(3)
  })
  it('shows a title in sentence case, the catalog title when there is one, and what each step is for', () => {
    const h = html()
    expect(h).toContain('Fetch documents')
    expect(h).toContain('Pulls the latest documents.')
    expect(h).toContain('>Convert<')
    expect(h).not.toContain('>convert<')
  })
  it('says so quietly when a step has no description', () => {
    expect(html()).toContain('No description yet')
  })
  it('shows duration and the main counts, and no bar for a step that never started', () => {
    const h = html()
    expect(h).toContain('12 s')
    expect(h).toContain('25 files')
    expect(h.match(/rounded-\[3px\] motion-safe:transition-\[left,width\]/g)).toHaveLength(2)
  })
  it('opens the panels it is told to, with the error and the log in them', () => {
    const closed = html()
    expect(closed).not.toContain('role="region"')
    const h = html((s) => s.status === 'failed')
    expect(h.match(/role="region"/g)).toHaveLength(1)
    expect(h).toContain('aria-expanded="true"')
    expect(h).toContain('exit code 1')
    expect(h).toContain('Loading log…')
  })
  it('lets a long list be narrowed to the steps that need a look', () => {
    const many = Array.from({ length: 20 }, (_, i) => step({ seq: i + 1, name: `batch-${i}`, status: i === 4 ? 'warning' : 'ok' }))
    const r = mkRun({ status: 'ok', finished_at: at('01:00') })
    const h = withClient(<StepWaterfall run={r} steps={many} ctx={ctx} span={60_000} isOpen={() => false} onToggle={() => {}} flash={null} />)
    expect(h).toContain('Only failed or warning')
    expect(h).not.toContain('content-visibility')
    const huge = Array.from({ length: 35 }, (_, i) => step({ seq: i + 1, name: `batch-${i}` }))
    expect(withClient(<StepWaterfall run={r} steps={huge} ctx={ctx} span={60_000} isOpen={() => false} onToggle={() => {}} flash={null} />)).toContain('content-visibility') // rows off screen are not laid out
    expect(withClient(<StepWaterfall run={r} steps={many.slice(0, 5)} ctx={ctx} span={60_000} isOpen={() => false} onToggle={() => {}} flash={null} />)).not.toContain('Only failed or warning')
  })
})

describe('StepDetail', () => {
  const failed = step({ status: 'failed', error: 'kept the previous files', counts: { listed: 25, refused: 3 }, log_dropped: true })
  const h = withClient(<StepDetail step={failed} ctx={ctx} />)
  it('reads description, status and timing, then the error, then the log', () => {
    const order = ['No description yet', 'Failed', 'Took', 'kept the previous files', 'Log'].map((s) => h.indexOf(s))
    expect(order.every((n) => n >= 0)).toBe(true)
    expect(order).toEqual([...order].sort((a, b) => a - b))
  })
  it('lists the counts with words and links Add to the stages editor', () => {
    expect(h).toContain('Removals refused')
    expect(h).toContain('href="/admin/pipelines/docs-sync/edit#stages"')
  })
  it('has the log tools by name and the 256 KiB note', () => {
    expect(h).toContain('aria-label="Copy log"')
    expect(h).toContain('aria-pressed')
    expect(h).toContain('Wrap')
    expect(h).toContain('Follow')
    expect(h).toContain('Showing the last 256 KiB. Times are UTC. Earlier lines were dropped.')
  })
  it('shows the catalog description instead of the Add link, and a generic one for a collection', () => {
    const d = withClient(<StepDetail step={step({ name: 'fetch-docs' })} ctx={ctx} />)
    expect(d).toContain('Pulls the latest documents.')
    expect(d).not.toContain('No description yet')
    const k = withClient(<StepDetail step={step({ kera: true, name: 'Docs' })} ctx={ctx} />)
    expect(k).toContain('Kera uploads the changed files to this collection and indexes them')
  })
  it('says it is loading, never that the step wrote nothing, while the log has not arrived', () => {
    expect(h).toContain('Loading log…')
    expect(h).not.toContain('This step wrote no log.')
  })
})

describe('KeraSyncs', () => {
  const colls = keraSteps(steps)
  const html = withClient(<KeraSyncs steps={colls} counts={{ listed: 25, unchanged: 22, uploaded: 3 }} ctx={ctx} isOpen={() => false} onToggle={() => {}} flash={null} />)
  it('is a group of one row per collection with its own count in the title', () => {
    expect(html).toContain('role="group"')
    expect(html).toContain('2 collections kept in sync')
    expect(html.match(/data-kind="kera"/g)).toHaveLength(2)
  })
  it('draws a segmented bar with numbers, and an indexing note', () => {
    expect(html).toContain('22')
    expect(html).toContain('unchanged')
    expect(html).toContain('3</b> changed')
    expect(html).toContain('Indexing · 2 waiting')
    expect(html).toContain('25 files · 3 changed')
  })
  it('stays quiet when everything is unchanged', () => {
    const quiet = withClient(<KeraSyncs steps={[step({ seq: 1000, kera: true, name: 'Docs', counts: { listed: 120, unchanged: 120, uploaded: 0 } })]} counts={{ listed: 120, unchanged: 120 }} ctx={ctx} isOpen={() => false} onToggle={() => {}} flash={null} />)
    expect(quiet).toContain('120 files, all unchanged')
    expect(quiet).toContain('nothing changed')
    expect(quiet).not.toContain('role="img"')
  })
  it('says why a collection has no numbers yet', () => {
    expect(html).toContain('Waiting for the script to finish')
  })
})

describe('HealthStrip', () => {
  const runs: Run[] = [mkRun({ id: 12, status: 'running' }), mkRun({ id: 11, status: 'ok', finished_at: at('02:00') }), mkRun({ id: 10, status: 'failed', error: 'x', finished_at: at('00:30') }), mkRun({ id: 9, status: 'ok', finished_at: at('03:00') })]
  const html = inRouter(<HealthStrip runs={runs} selected={11} urlOf={(id) => `/admin/pipelines/docs-sync/${id}`} now={now} />)
  it('links every run, oldest first, with its status in words', () => {
    expect(html.match(/<a /g)).toHaveLength(4)
    expect(html.indexOf('/docs-sync/9')).toBeLessThan(html.indexOf('/docs-sync/12'))
    expect(html).toContain('aria-label="#10, Failed')
    expect(html).toContain('aria-label="#12, Running')
  })
  it('marks the selected run and sums up the finished ones', () => {
    expect(html.match(/aria-current="page"/g)).toHaveLength(1)
    expect(html).toContain('2 of 3')
    expect(html).toContain('1 failed')
    expect(html).toContain('median')
  })
  it('draws nothing without runs', () => {
    expect(inRouter(<HealthStrip runs={[]} selected={undefined} urlOf={() => ''} now={now} />)).toBe('')
  })
})

describe('StagesEditor', () => {
  const list = [{ name: 'fetch-itop', title: '', about: '' }, { name: 'convert', title: 'Convert it', about: 'Runs pandoc.' }]
  it('lists each stage with its name and a title field whose placeholder is the sentence-case fallback', () => {
    const h = renderToStaticMarkup(<StagesEditor value={list} onChange={() => {}} seen={['fetch-itop']} />)
    expect(h.match(/data-testid="stage-row"/g)).toHaveLength(2)
    expect(h).toContain('placeholder="Fetch iTop"')
    expect(h).toContain('value="Convert it"')
    expect(h).toContain('aria-label="Remove the stage convert"')
  })
  it('offers to detect the step names of the latest run that are not listed yet', () => {
    const h = renderToStaticMarkup(<StagesEditor value={list} onChange={() => {}} seen={['fetch-itop', 'dedupe', 'upload']} />)
    expect(h).toContain('Detect from the latest run (2)')
    const none = renderToStaticMarkup(<StagesEditor value={list} onChange={() => {}} seen={['convert']} />)
    expect(none).toMatch(/<button[^>]*disabled=""[^>]*data-testid="detect-stages"|<button[^>]*data-testid="detect-stages"[^>]*disabled=""/)
  })
  it('says what an empty list means, and shows a server error', () => {
    const h = renderToStaticMarkup(<StagesEditor value={[]} onChange={() => {}} seen={[]} error="At most 200 stages" />)
    expect(h).toContain('sentence case')
    expect(h).toContain('At most 200 stages')
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


describe('RunSummary', () => {
  const run = (o: Partial<Run>): Run => mkRun({ id: 7, ...o })
  const header = (r: Run, o: { runner?: string; cancel?: boolean; steps?: Step[]; online?: boolean; typical?: number | null } = {}) =>
    inRouter(<RunSummary run={r} steps={o.steps} stages={stages} now={now} runner={o.runner} typical={o.typical} online={o.online} onCancel={o.cancel === false ? undefined : () => {}} onJump={() => {}} />)

  it('has the trigger, script version and runner name among its facts', () => {
    const h = header(run({ script_version: 3, runner: 'kr1' }), { runner: 'build-server' })
    expect(h).toContain('Manual')
    expect(h).toContain('script v3')
    expect(h).toContain('Runner build-server')
  })
  it('sums the state up in one sentence that is announced, next to a big clock', () => {
    const h = header(run({}), { steps: [step({ seq: 1, name: 'fetch-docs', status: 'running', finished_at: null })] })
    expect(h).toContain('aria-live="polite"')
    expect(h).toContain('Running')
    expect(h).toContain('Fetch documents')
    expect(h).toContain('10:00') // started 10 minutes before now
    expect(h).toContain('Elapsed')
  })
  it('draws one segment per step as a button, the running one animated', () => {
    const h = header(run({}), { steps })
    expect(h.match(/data-status="(ok|failed|skipped|running|pending)"[^>]*class="block h-2\.5/g)).toHaveLength(5)
    expect(h).toContain('aria-label="Fetch documents: OK"')
    expect(h).toContain('animate-sweep')
    expect(h).toContain('steps done')
  })
  it('compares with the usual length', () => {
    expect(header(run({}), { steps, typical: 90_000 })).toContain('Typically 1 min 30 s')
    expect(header(run({}), { steps, typical: 90_000 })).toContain('running longer than usual')
    expect(header(run({}), { steps })).not.toContain('Typically')
  })
  it('reads Queued with a Cancel run button before a runner claims it', () => {
    const h = header(run({ queued: true }), { online: true })
    expect(h).toContain('Queued')
    expect(h).toContain('Cancel run')
    expect(h).toContain('A runner picks it up within a few seconds')
    expect(h).not.toContain('animate-spin')
    expect(h).not.toContain('Runners')
  })
  it('says so, with a way to the runners, when none is online', () => {
    const h = header(run({ queued: true }), { online: false })
    expect(h).toContain('No runner is online')
    expect(h).toContain('href="/admin/pipelines?runners"')
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
  it('puts a failed run in a calm block with the error and a way to the failed step', () => {
    const h = header(run({ status: 'failed', finished_at: at('01:12'), error: 'convert failed: exit code 1' }), { steps })
    expect(h).toContain('What went wrong')
    expect(h).toContain('convert failed: exit code 1')
    expect(h).toContain('Show the failed step')
    expect(h).toContain('Failed at Convert')
    expect(h).toContain('Total time')
  })
  it('says dry run and notebook with their own chips', () => {
    expect(header(run({ dry_run: true }))).toContain('Dry run')
    const nb = header(run({ trigger: 'test', dry_run: true }))
    expect(nb).toContain('Notebook')
    expect(nb).not.toContain('Dry run')
  })
  it('says closed and cancelled in their own words, without an error block', () => {
    const closed = header(run({ status: 'failed', finished_at: at('01:12'), error: 'Closed by an admin at 08:12 UTC.' }))
    expect(closed).toContain('Closed by an admin at 08:12 UTC')
    expect(closed).not.toContain('What went wrong')
    const cancelled = header(run({ status: 'failed', finished_at: at('01:12'), error: 'Cancelled by an admin.' }))
    expect(cancelled).toContain('Cancelled')
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
