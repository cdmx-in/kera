// Render checks for the wizard's pieces: static markup only (no browser), with the data the pages would have fetched.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it } from 'vitest'
import type { Model, ToolServer } from '@/api/types'
import { useComposer } from '@/stores/composer'
import type { Collection } from '@/features/workspace/api'
import type { useUploads } from '@/features/chat/composer/uploads'
import { initialDraft, type Draft } from './draft'
import { Stepper } from './Stepper'
import type { LastTest } from './TestRun'
import { stepList, type Errors } from './steps'
import { AssistantStep } from './steps/AssistantStep'
import { EXAMPLES, GoalStep } from './steps/GoalStep'
import { ReviewStep } from './steps/ReviewStep'
import { ToolsStep } from './steps/ToolsStep'
import { WhenStep } from './steps/WhenStep'

const models = [
  { id: 'm1', name: 'Local Helper', local: true, info: { meta: { description: 'Runs on this server' } }, features: [{ id: 'web_search', label: 'Web search', default: true }] },
  { id: 'm2', name: 'Cloud Pro', info: { meta: { description: 'Hosted and fast' } } },
  { id: 'm3', name: 'Hidden One', hidden: true },
  { id: 'm4', name: 'Embedder', kind: 'embedding' },
] as Model[]
const tools: ToolServer[] = [
  { id: 'sheets', name: 'Sheets', kind: 'mcp', auth_type: 'user_oauth', authenticated: true, provider: 'google', description: 'Read and fill spreadsheets', functions: ['read_sheet', 'write_cells'] },
  { id: 'mail', name: 'Mail', kind: 'mcp', auth_type: 'user_oauth', authenticated: true, local_only: true, description: 'Read mail', functions: ['list_mail'] },
  { id: 'crm', name: 'CRM', kind: 'mcp', auth_type: 'user_oauth', authenticated: false, provider: 'crm', description: 'Customer records' },
  { id: 'kb', name: 'Team KB', kind: 'openapi', auth_type: 'none', authenticated: true, description: 'Team knowledge base', functions: ['search'] },
  { id: 'core', name: 'core', kind: 'builtin', authenticated: true },
]
const collections = [
  { id: 'k1', name: 'Handbook', description: 'HR policies', status: { files: 3, chunks: 9, pending: 0, processing: 0, completed: 3, failed: 0 } },
  { id: 'k2', name: 'Runbooks', description: '', status: { files: 1, chunks: 2, pending: 0, processing: 0, completed: 1, failed: 0 } },
] as Collection[]

/** The opening tag of the element with this test id. */
const tag = (h: string, id: string) => h.match(new RegExp(`<[^>]*data-testid="${id}"[^>]*>`))?.[0] ?? ''

const draft = (over: Partial<Draft> = {}): Draft => ({ ...initialDraft({ zone: 'Asia/Kolkata', schedulable: true }), ...over })
const uploads = { items: [], add: () => {}, remove: () => {}, clear: () => {}, busy: false, ready: [] } as unknown as ReturnType<typeof useUploads>

function render(node: React.ReactNode, data: { models?: Model[]; tools?: ToolServer[]; collections?: Collection[] } = {}) {
  const qc = new QueryClient()
  qc.setQueryData(['models'], data.models ?? models)
  qc.setQueryData(['tools'], data.tools ?? tools)
  qc.setQueryData(['ws', 'knowledge'], data.collections ?? collections)
  return renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  )
}

// Server rendering reads a zustand store's initial state, not its current one: set both.
const setComposer = (patch: Partial<ReturnType<typeof useComposer.getState>>) => {
  useComposer.setState(patch)
  Object.assign(useComposer.getInitialState(), patch)
}
beforeEach(() => setComposer({ model: 'm1', modelAuto: false, features: { web_search: true }, toolIds: ['sheets'] }))

describe('Stepper', () => {
  const steps = stepList(true)
  const html = renderToStaticMarkup(<Stepper steps={steps} current="assistant" done={(s) => s === 'what'} reachable={(s) => s === 'what' || s === 'assistant'} onGo={() => {}} />)
  it('marks the current step, the finished one and the ones not open yet', () => {
    expect(html).toContain('aria-current="step"')
    expect(html.match(/aria-current="step"/g)).toHaveLength(1)
    expect(html).toMatch(/data-testid="step-what"[^>]*data-state="done"/)
    expect(html).toMatch(/data-testid="step-assistant"[^>]*data-state="current"/)
    expect(html).toMatch(/data-testid="step-review"[^>]*data-state="todo"[^>]*aria-disabled="true"/)
    expect(html).not.toMatch(/data-testid="step-assistant"[^>]*aria-disabled/)
  })
  it('names every step, and says (done) to a screen reader for a finished one', () => {
    for (const l of ['Goal', 'Assistant', 'Apps and tools', 'Schedule', 'Review']) expect(html).toContain(l)
    expect(html).toContain('(done)')
  })
  it('has one segment fewer for people who may not schedule', () => {
    const h = renderToStaticMarkup(<Stepper steps={stepList(false)} current="what" done={() => false} reachable={() => true} onGo={() => {}} />)
    expect(h).not.toContain('Schedule')
    expect(h.match(/<li /g)).toHaveLength(4)
  })
})

describe('GoalStep', () => {
  const html = (d: Draft, errors: Errors = {}, edit = false) => render(<GoalStep draft={d} dispatch={() => {}} errors={errors} uploads={uploads} edit={edit} />)
  it('offers example goals while the box is empty, and takes them away once the person has written their own', () => {
    const empty = html(draft())
    expect(empty).toContain('data-testid="task-goal"')
    expect(empty.match(/data-testid="example"/g)).toHaveLength(EXAMPLES.length)
    expect(html(draft({ goal: EXAMPLES[1] }))).toContain('data-testid="examples"')
    expect(html(draft({ goal: 'my own goal' }))).not.toContain('data-testid="examples"')
  })
  it('shows the goal, an error under it, and a name box', () => {
    const h = html(draft({ goal: 'Write it' }), { goal: 'Describe what the assistant should do.' })
    expect(h).toContain('>Write it</textarea>')
    expect(h).toContain('Describe what the assistant should do.')
    expect(h).toContain('aria-invalid="true"')
    expect(h).toContain('data-testid="schedule-title"')
  })
  it('lists the files a saved task keeps, and the notes box only when editing', () => {
    const d = draft({ goal: 'x', kept: [{ id: 'f1', name: 'rates.csv' }] })
    expect(html(d)).toContain('data-testid="kept-file"')
    expect(html(d)).toContain('rates.csv')
    expect(html(d)).not.toContain('schedule-notes')
    expect(html(d, {}, true)).toContain('data-testid="schedule-notes"')
  })
})

describe('AssistantStep', () => {
  const html = (d = draft(), errors: Errors = {}) => render(<AssistantStep draft={d} dispatch={() => {}} errors={errors} onPick={() => {}} />)
  it('shows a card per chat assistant, not hidden ones or embedding models, as radio buttons', () => {
    const h = html()
    expect(h.match(/data-testid="model-option"/g)).toHaveLength(2)
    expect(h).toContain('Local Helper')
    expect(h).toContain('Cloud Pro')
    expect(h).not.toContain('Hidden One')
    expect(h).not.toContain('Embedder')
    expect(h.match(/type="radio"/g)).toHaveLength(2)
    expect(h).toContain('role="radiogroup"')
  })
  it('checks the chosen assistant and says which run on this server and which are hosted', () => {
    const h = html()
    expect(h.match(/<input[^>]*value="m1"[^>]*>/)?.[0]).toContain('checked=""')
    expect(h.match(/<input[^>]*value="m2"[^>]*>/)?.[0]).not.toContain('checked')
    expect(h).toContain('On server')
    expect(h).toContain('Hosted')
    expect(h).toContain('Runs on this server')
  })
  it('lists a hidden assistant when it is the current choice', () => {
    setComposer({ model: 'm3' })
    expect(html()).toContain('Hidden One')
  })
  it('offers the assistant\'s switches as toggle buttons, pressed as set', () => {
    expect(tag(html(), 'pill-web_search')).toContain('aria-pressed="true"')
    setComposer({ model: 'm2' })
    expect(html()).not.toContain('pill-web_search')
  })
  it('lists the knowledge collections as checkboxes, with the chosen ones as chips', () => {
    const h = html(draft({ collections: [{ id: 'k1', name: 'Handbook', type: 'collection' }] }))
    expect(h).toContain('data-testid="knowledge-chip"')
    expect(tag(h, 'knowledge-k1')).toContain('checked=""')
    expect(tag(h, 'knowledge-k2')).not.toContain('checked')
    expect(h).toContain('Runbooks')
    expect(h).toContain('3 files')
    expect(h).toContain('1 file<')
  })
  it('leaves out the knowledge block when there is none, and says so when there is no assistant', () => {
    expect(render(<AssistantStep draft={draft()} dispatch={() => {}} errors={{}} onPick={() => {}} />, { collections: [] })).not.toContain('Knowledge')
    expect(render(<AssistantStep draft={draft()} dispatch={() => {}} errors={{ model: 'Choose an assistant.' }} onPick={() => {}} />, { models: [] })).toContain('No assistants are available')
  })
})

describe('ToolsStep', () => {
  const html = (data?: { tools?: ToolServer[] }) => render(<ToolsStep schedulable />, data)
  it('lists your accounts apart from shared tools, and never the built-in sets', () => {
    const h = html()
    expect(h).toContain('Your accounts')
    expect(h).toContain('Shared tools')
    expect(h.match(/data-testid="app-row"/g)).toHaveLength(4)
    expect(h).not.toContain('data-id="core"')
  })
  it('gives each app a status in words: connected, connect needed, local assistants only', () => {
    const h = html()
    expect(h).toMatch(/data-testid="app-status-sheets"[^>]*>Connected</)
    expect(h).toMatch(/data-testid="app-status-crm"[^>]*>Connect needed</)
    expect(h).toMatch(/data-testid="app-status-mail"[^>]*>Connected</) // m1 runs on this server: the local-only app is fine
  })
  it('greys an app that cannot join and says why: a hosted assistant cannot use a local-only app', () => {
    setComposer({ model: 'm2' })
    const h = html()
    expect(h).toMatch(/data-testid="app-status-mail"[^>]*>Local assistants only</)
    expect(h).toContain('works only with a local assistant')
    expect(tag(h, 'app-mail')).toContain('disabled=""')
    expect(tag(h, 'app-kb')).not.toContain('disabled')
  })
  it('offers Connect only for an app that is not linked, back to this step', () => {
    const h = html()
    expect(h).toContain('data-testid="connect-crm"')
    expect(h).not.toContain('data-testid="connect-sheets"')
    expect(h).toContain('/oauth/clients/crm/authorize?next=')
    expect(h).toContain(encodeURIComponent('step=tools'))
  })
  it('counts what is switched on and can join, and says what is left out', () => {
    setComposer({ model: 'm2', toolIds: ['sheets', 'mail', 'builtin:code'] })
    const h = html()
    expect(h).toMatch(/data-testid="selected-apps"[\s\S]*>1<\/span>/)
    expect(h).toContain('data-testid="selected-app"')
    expect(h).toContain('Left out of this task: Mail')
    expect(tag(h, 'app-sheets')).toContain('checked=""')
    expect(tag(h, 'app-mail')).not.toContain('checked')
  })
  it('shows one line of each description, with the whole of it and the tool names on request', () => {
    const h = html()
    expect(h).toContain('Read and fill spreadsheets')
    expect(h).toContain('aria-expanded="false"')
    expect(h).toContain('data-testid="check-connections"')
  })
  it('warns about a scheduled run that finds an app disconnected only for people who may schedule', () => {
    expect(html()).toContain('A scheduled task fails')
    expect(render(<ToolsStep schedulable={false} />)).not.toContain('A scheduled task fails')
  })
  it('says so when no apps are set up, and carries on', () => {
    expect(html({ tools: [tools[4]] })).toContain('data-testid="no-apps"')
  })
})

describe('WhenStep', () => {
  const html = (d: Draft, edit = false, errors: Errors = {}) => render(<WhenStep draft={d} dispatch={() => {}} errors={errors} edit={edit} />)
  it('offers once-now and the four frequencies; a saved task cannot go back to "once"', () => {
    const h = html(draft())
    expect(h.match(/type="radio"/g)).toHaveLength(5)
    for (const l of ['Run once, now', 'Every hour', 'Every day', 'Weekdays', 'Every week']) expect(h).toContain(l)
    expect(html(draft({ when: 'daily' }), true).match(/type="radio"/g)).toHaveLength(4)
    expect(html(draft({ when: 'daily' }), true)).not.toContain('Run once, now')
  })
  it('says nothing is scheduled for a run now, and shows no timing', () => {
    const h = html(draft())
    expect(h).toContain('Once, as soon as you start it')
    expect(h).toContain('Nothing is scheduled')
    expect(h).not.toContain('data-testid="when-time"')
  })
  it('asks for what each frequency needs: a time, a day and a time, a minute', () => {
    const daily = html(draft({ when: 'daily', time: '06:00' }))
    expect(daily).toContain('type="time"')
    expect(daily).toContain('Every day at 06:00 (Asia/Kolkata)')
    expect(daily).toContain('value="06:00"')
    expect(daily).not.toContain('data-testid="when-weekday"')
    expect(html(draft({ when: 'weekly', weekday: 5 }))).toContain('Every Friday at 02:00 (Asia/Kolkata)')
    expect(html(draft({ when: 'weekly' }))).toContain('data-testid="when-weekday"')
    const hourly = html(draft({ when: 'hourly', minute: 15 }))
    expect(hourly).toContain('data-testid="when-minute"')
    expect(hourly).not.toContain('type="time"')
    expect(hourly).toContain('Every hour at :15 (Asia/Kolkata)')
  })
  it('names the time zone, selected, and has a place for the next runs and the warning about unattended runs', () => {
    const h = html(draft({ when: 'daily' }))
    expect(h).toMatch(/<option value="Asia\/Kolkata" selected/)
    expect(h).toContain('data-testid="schedule-preview"')
    expect(h).toContain('Nobody watches a scheduled run')
  })
  it('shows a timing error', () => {
    expect(html(draft({ when: 'daily', time: '' }), false, { time: 'Pick a time of day.' })).toContain('Pick a time of day.')
  })
})

describe('ReviewStep', () => {
  const html = (d: Draft, steps = stepList(true), saveError?: string, lastTest: LastTest = { ranWith: '' }) => render(<ReviewStep draft={d} steps={steps} goTo={() => {}} files={2} testBody={() => ({})} signature="s" lastTest={lastTest} onTest={() => {}} saveError={saveError} />)
  const d = draft({ title: 'Morning digest', goal: 'Summarise the tickets', collections: [{ id: 'k1', name: 'Handbook', type: 'collection' }], when: 'daily', time: '06:00' })
  it('summarises every choice with an Edit button per section', () => {
    const h = html(d)
    expect(h).toContain('Morning digest')
    expect(h).toContain('Summarise the tickets')
    expect(h).toContain('Local Helper')
    expect(h).toContain('Abilities: Web search')
    expect(h).toContain('Knowledge: Handbook')
    expect(h).toContain('Sheets')
    expect(h).toContain('Every day at 06:00 (Asia/Kolkata)')
    expect(h).toContain('2 files attached')
    for (const s of ['review-goal', 'review-assistant', 'review-apps', 'review-when']) expect(h).toContain(`data-testid="edit-${s}"`)
  })
  it('has no Edit for the schedule when the person may not schedule', () => {
    const h = html(draft({ goal: 'x' }), stepList(false))
    expect(h).not.toContain('data-testid="edit-review-when"')
    expect(h).toContain('Once, as soon as you start it')
  })
  it('names the apps left out for this assistant', () => {
    setComposer({ model: 'm2', toolIds: ['sheets', 'mail'] })
    expect(html(d)).toContain('Left out: Mail')
  })
  it('offers the test run and says what a test can and cannot do', () => {
    const h = html(d)
    expect(h).toContain('data-testid="test-run"')
    expect(h).toContain('Run a test')
    expect(h).toContain('read-only')
    expect(h).toContain('Nothing is scheduled or saved')
    expect(h).not.toContain('data-testid="test-result"')
  })
  it('keeps the last test in place, and says when the task changed since', () => {
    const same = html(d, stepList(true), undefined, { chatId: 'c1', ranWith: 's' })
    expect(same).toContain('data-testid="test-result"')
    expect(same).toContain('Run the test again')
    expect(same).not.toContain('data-testid="test-stale"')
    const stale = html(d, stepList(true), undefined, { chatId: 'c1', ranWith: 'an older body' })
    expect(stale).toContain('data-testid="test-stale"')
    expect(stale).toContain('You changed the task after this test')
  })
  it('shows a save error from the server', () => {
    expect(html(d, stepList(true), 'You have reached the limit of scheduled tasks')).toContain('You have reached the limit of scheduled tasks')
  })
})
