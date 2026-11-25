// Render checks for the notebook view: static markup only.
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { CodeEditor } from './CodeEditor'
import { CellBlock, type CellActions, CellStatusChip, NotebookToolbar, ProblemsPanel, notebookHelp, realWarning } from './PipelineNotebook'
import { History } from './PipelineRuns'
import type { NbCell } from './notebook'
import type { CellProblems, CellRun, Problem, Run } from './pipelines'

const cell = (o: Partial<NbCell> = {}): NbCell => ({ id: 'c1', title: 'fetch data', header: '#%% fetch data', code: 'curl x\nls', bare: false, ...o })
const none: CellProblems = { errors: 0, warnings: 0, lines: {} }
const noop = () => {}
const actions: CellActions = { onTitle: noop, onCode: noop, onRun: noop, onRunUpTo: noop, onStop: noop, onDelete: noop, onAddBelow: noop, onMove: noop, onClear: noop, onRunNext: noop }
const run = (o: Partial<CellRun> = {}): CellRun => ({ server: 4, status: 'ok', output: 'hello\n', exit_code: 0, sent: 'curl x\nls', ...o })
const block = (o: { cell?: NbCell; run?: CellRun; problems?: CellProblems; count?: number; startLine?: number; step?: string | null; up?: boolean; down?: boolean } = {}) =>
  renderToStaticMarkup(
    <CellBlock
      cell={o.cell ?? cell()}
      index={1}
      count={o.count ?? 3}
      startLine={o.startLine ?? 7}
      step={o.step === undefined ? 'fetch-data' : o.step}
      problems={o.problems ?? none}
      run={o.run}
      canMoveUp={o.up ?? true}
      canMoveDown={o.down ?? true}
      editorRef={noop}
      titleRef={noop}
      actions={actions}
    />,
  )

describe('CellBlock', () => {
  it('has a labelled name, the step the runner will make of it and its code, numbered through the file', () => {
    const h = block()
    expect(h).toContain('aria-label="Name of cell 2"')
    expect(h).toContain('value="fetch data"')
    expect(h).toContain('step fetch-data')
    expect(h).toContain('aria-label="Code of cell 2"')
    expect(h).toContain('<pre>7\n8</pre>') // the second line of the cell is line 8 of the file
  })
  it('says setup for the text before the first header, and gives it no name box', () => {
    const h = block({ cell: cell({ header: null, title: '' }), step: null })
    expect(h).toContain('setup')
    expect(h).not.toContain('Name of cell')
    expect(h).not.toContain('step ')
  })
  it('names every button for its cell', () => {
    const h = block()
    for (const l of ['Run cell 2', 'Run up to cell 2', 'Move cell 2 up', 'Move cell 2 down', 'Add a cell below cell 2', 'Delete cell 2']) expect(h).toContain(`aria-label="${l}"`)
  })
  it('turns Run into Stop while the cell is queued or running', () => {
    expect(block({ run: run({ status: 'running' }) })).toContain('aria-label="Stop cell 2"')
    expect(block({ run: run({ status: 'queued', output: '' }) })).toContain('aria-label="Stop cell 2"')
    const idle = block({ run: run() })
    expect(idle).toContain('aria-label="Run cell 2"')
    expect(idle).not.toContain('Stop cell')
  })
  it('shows the status in words with an icon, and the output under the cell in a scroll box', () => {
    const h = block({ run: run({ status: 'failed', output: 'boom\n', exit_code: 2 }) })
    expect(h).toContain('data-status="failed"')
    expect(h).toContain('Failed')
    expect(h).toContain('overflow-auto')
    expect(h).toContain('boom')
    expect(h).toContain('Exit code 2')
    expect(h).toContain('Clear output')
  })
  it('offers Clear output only once the cell is done and has output', () => {
    expect(block({ run: run({ status: 'running', output: 'x' }) })).not.toContain('Clear output')
    expect(block({ run: run({ output: '' }) })).not.toContain('cell-output')
    expect(block()).not.toContain('cell-output')
  })
  it('says the cell was edited after it ran, and waits for the runner while queued', () => {
    expect(block({ run: run({ sent: 'old code' }) })).toContain('Edited since it ran')
    expect(block({ run: run() })).not.toContain('Edited since it ran')
    expect(block({ run: run({ status: 'queued', output: '', server: 0 }) })).toContain('Waiting for the runner')
  })
  it('counts errors and warnings in the header and marks the lines in the gutter', () => {
    const h = block({ problems: { errors: 2, warnings: 1, lines: { 2: 'error', 1: 'warning' } } })
    expect(h).toContain('2 errors')
    expect(h).toContain('1 warning')
    expect(h.match(/data-testid="gutter-mark"/g)).toHaveLength(2)
    expect(h).toContain('Error on line 8') // line 2 of a cell that starts at line 7 is line 8 of the file
    expect(h).toContain('Warning on line 7')
    expect(block({ problems: { errors: 1, warnings: 0, lines: {} } })).toContain('1 error<')
    expect(block()).not.toContain('error')
  })
  it('does not mark the header line in the cell gutter (it has no line there)', () => {
    expect(block({ problems: { errors: 1, warnings: 0, lines: { 0: 'error' } } }).match(/data-testid="gutter-mark"/g)).toBeNull()
  })
  it('cannot delete the only cell, and cannot move past the ends', () => {
    expect(block({ count: 1 })).toMatch(/aria-label="Delete cell 2"[^>]*disabled|disabled=""[^>]*aria-label="Delete cell 2"/)
    expect(block({ up: false })).toMatch(/aria-label="Move cell 2 up"[^>]*disabled|disabled=""[^>]*aria-label="Move cell 2 up"/)
  })
})

describe('CellStatusChip', () => {
  it('says each status in words', () => {
    for (const [s, w] of [['queued', 'Queued'], ['running', 'Running'], ['ok', 'OK'], ['failed', 'Failed'], ['stopped', 'Stopped']] as const) expect(renderToStaticMarkup(<CellStatusChip status={s} />)).toContain(w)
  })
})

describe('CodeEditor numbering', () => {
  it('starts the gutter at the line the editor has in the file', () => {
    expect(renderToStaticMarkup(<CodeEditor label="x" value={'a\nb\nc'} startLine={10} />)).toContain('<pre>10\n11\n12</pre>')
  })
  it('marks a line with an icon that says its severity, and nothing without problems', () => {
    const h = renderToStaticMarkup(<CodeEditor label="x" value={'a\nb'} marks={{ 2: 'error' }} />)
    expect(h).toContain('data-severity="error"')
    expect(h).toContain('top:28px') // 8px padding + one 20px line
    expect(renderToStaticMarkup(<CodeEditor label="x" value="a" />)).not.toContain('gutter-mark')
  })
})

describe('NotebookToolbar', () => {
  const base = { session: null, noRunner: false, starting: false, error: '', real: false, onReal: noop, runningAll: false, onStart: noop, onClose: noop, onRunAll: noop, onStopAll: noop, checking: false, onCheck: noop, raw: false, onRaw: noop }
  const bar = (o: Partial<Parameters<typeof NotebookToolbar>[0]> = {}) =>
    renderToStaticMarkup(
      <MemoryRouter>
        <NotebookToolbar {...base} {...o} />
      </MemoryRouter>,
    )
  it('starts a session, and restarts it once one is open', () => {
    expect(bar()).toContain('Start session')
    expect(bar()).toContain('No session yet')
    const h = bar({ session: { id: 's1', status: 'ready', error: '', real: false } })
    expect(h).toContain('Restart session')
    expect(h).toContain('Close session')
    expect(h).toContain('Ready')
  })
  it('says what the session is doing and why it ended', () => {
    expect(bar({ session: { id: 's1', status: 'starting', error: '', real: false } })).toContain('Starting…')
    expect(bar({ session: { id: 's1', status: 'closed', error: 'closed after being idle', real: false } })).toContain('Closed: closed after being idle')
    expect(bar({ error: 'bad entry' })).toContain('bad entry')
  })
  it('says there is no runner, with a link to Runners', () => {
    const h = bar({ noRunner: true })
    expect(h).toContain('No runner is online')
    expect(h).toContain('href="/admin/pipelines?runners"')
    expect(bar()).not.toContain('Open Runners')
  })
  it('has the real workspace off by default, with the amber warning always in view', () => {
    const h = bar()
    expect(h).toMatch(/data-testid="real-workspace"/)
    expect(h).not.toMatch(/checked="" [^>]*data-testid="real-workspace"|data-testid="real-workspace"[^>]*checked=""/)
    expect(h).toContain(realWarning.replace(/'/g, '&#x27;')) // the text as the markup escapes it
    expect(realWarning).toBe("Cells can read and change the pipeline's real workspace files. Uploads to collections are still only planned, never done.")
    expect(h).toMatch(/text-amber-800[^"]*"[^>]*data-testid="real-warning"/)
  })
  it('only lets the real workspace be changed before a session starts', () => {
    const open = bar({ session: { id: 's1', status: 'ready', error: '', real: false } })
    expect(open).toMatch(/disabled=""[^>]*data-testid="real-workspace"|data-testid="real-workspace"[^>]*disabled=""/)
    expect(open).toContain('can only be changed before a session starts')
    const closed = bar({ session: { id: 's1', status: 'closed', error: '', real: false } })
    expect(closed).not.toMatch(/disabled=""[^>]*data-testid="real-workspace"/)
  })
  it('turns Run all into Stop while it runs, and is not available in the raw view', () => {
    expect(bar()).toContain('Run all')
    expect(bar({ runningAll: true })).toContain('data-testid="run-all-stop"')
    expect(bar({ runningAll: true })).not.toContain('data-testid="run-all"')
    expect(bar({ raw: true })).toMatch(/disabled="" [^>]*data-testid="run-all"/)
    expect(bar()).not.toMatch(/disabled="" [^>]*data-testid="run-all"/)
  })
  it('carries the help line, and shows Checking… while a check runs', () => {
    expect(bar()).toContain(notebookHelp)
    expect(notebookHelp).toBe('Cells run in a live session on a runner. State carries between cells. Uploads are planned, never done. Stop interrupts a Python cell (state kept) or restarts the shell (variables lost).')
    expect(bar({ checking: true })).toContain('Checking…')
  })
})

describe('ProblemsPanel', () => {
  const p = (o: Partial<Problem>): Problem => ({ file: 'run.sh', line: 4, col: 3, severity: 'error', message: 'syntax error near unexpected token', tool: 'bash -n', ...o })
  const panel = (problems: Problem[], o: { checked?: boolean; checking?: boolean; hint?: string; tools?: string[] } = {}) =>
    renderToStaticMarkup(
      <MemoryRouter>
        <ProblemsPanel problems={problems} tools={o.tools ?? []} checked={o.checked ?? true} checking={o.checking ?? false} hint={o.hint ?? ''} onPick={noop} />
      </MemoryRouter>,
    )
  it('lists severity (word and icon), file:line, message, tool and code', () => {
    const h = panel([p({}), p({ severity: 'warning', line: 9, col: 0, tool: 'shellcheck', code: 'SC2086', message: 'Double quote to prevent globbing' })], { tools: ['bash -n', 'shellcheck'] })
    expect(h.match(/data-testid="problem"/g)).toHaveLength(2)
    expect(h).toContain('Error')
    expect(h).toContain('Warning')
    expect(h).toContain('run.sh:4:3')
    expect(h).toContain('run.sh:9<') // no column when the tool gave none
    expect(h).toContain('shellcheck SC2086')
    expect(h).toContain('syntax error near unexpected token')
    expect(h).toContain('1 error, 1 warning')
    expect(h).toContain('Checked with bash -n, shellcheck')
  })
  it('says None found, Not checked yet and Checking… in their places', () => {
    expect(panel([])).toContain('No problems found.')
    expect(panel([], { checked: false })).toContain('Not checked yet')
    expect(panel([], { checked: false, checking: true })).toContain('Checking…')
  })
  it('handles a missing runner quietly, with a link', () => {
    const h = panel([], { checked: false, hint: 'no_runner' })
    expect(h).toContain('Checks need a runner and none is online.')
    expect(h).toContain('href="/admin/pipelines?runners"')
  })
  it('says when a check did not finish', () => {
    expect(panel([], { checked: false, hint: 'The runner did not answer in time' })).toContain('The check did not finish: The runner did not answer in time')
  })
})

describe('History with notebook runs', () => {
  const r = (o: Partial<Run>): Run => ({ id: 1, pipeline: 'p', trigger: 'schedule', dry_run: false, status: 'ok', client_done: true, error: '', queued: false, cancel_requested: false, started_at: '2026-03-10T06:00:00Z', active_at: '2026-03-10T06:00:00Z', finished_at: '2026-03-10T06:01:00Z', counts: {}, ...o })
  const h = (runs: Run[], notebookRuns: number, show = false) =>
    renderToStaticMarkup(
      <MemoryRouter>
        <History runs={runs} loading={false} more={false} selected={undefined} name="p" now={0} onMore={noop} notebookRuns={notebookRuns} showNotebook={show} onShowNotebook={noop} />
      </MemoryRouter>,
    )
  it('labels a notebook run Notebook', () => {
    expect(h([r({ id: 2, trigger: 'test', dry_run: true })], 1, true)).toContain('Notebook')
  })
  it('offers the toggle only when the loaded runs include notebook runs, with their number', () => {
    expect(h([r({})], 0)).not.toContain('Show notebook runs')
    expect(h([r({})], 3)).toContain('Show notebook runs (3)')
    expect(h([r({})], 3, true)).toMatch(/data-testid="show-notebook-runs" checked=""/)
    expect(h([r({})], 3, false)).not.toMatch(/data-testid="show-notebook-runs" checked=""/)
  })
})
