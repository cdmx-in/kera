// Render checks for the "How it runs" choice and its timetable: static markup only.
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { HowItRuns, nextRunText } from './PipelineSchedule'
import type { FormErrors } from './settings/dialogKit'
import type { ScheduleForm } from './pipelines'

const form = (errs: Record<string, string> = {}) => ({ err: (f: string) => errs[f], touch: () => {} }) as unknown as FormErrors
const value = (o: Partial<ScheduleForm> = {}): ScheduleForm => ({ mode: 'kera', scheduled: false, frequency: 'daily', time: '09:30', minute: '0', weekday: 1, timezone: 'Asia/Kolkata', ...o })
const render = (v: ScheduleForm, o: { errs?: Record<string, string>; noScript?: boolean; scriptHref?: string | null } = {}) =>
  renderToStaticMarkup(
    <MemoryRouter>
      <HowItRuns value={v} onChange={() => {}} form={form(o.errs)} saved={null} scriptHref={o.scriptHref === undefined ? '/admin/pipelines/docs?tab=script' : o.scriptHref} noScript={o.noScript ?? false} />
    </MemoryRouter>,
  )

describe('HowItRuns', () => {
  it('offers the two ways, in words, as radio buttons', () => {
    const h = render(value({ mode: 'external' }))
    expect(h).toContain('Started from outside (cron, CI, a script)')
    expect(h).toContain('Run by Kera')
    expect(h.match(/type="radio"/g)).toHaveLength(2)
    expect(h).toMatch(/type="radio"[^>]*name="run_mode"[^>]*checked=""[^>]*\/><span class="min-w-0"><span class="block text-sm font-medium">Started from outside/)
  })
  it('shows no timetable for a pipeline something else starts', () => {
    expect(render(value({ mode: 'external' }))).not.toContain('data-testid="timetable"')
  })
  it('asks for frequency, time of day, time zone and the Scheduled switch for a Kera-run one', () => {
    const h = render(value())
    expect(h).toContain('data-testid="timetable"')
    for (const l of ['Repeats', 'At (HH:MM)', 'Time zone']) expect(h).toContain(l)
    expect(h).toContain('type="time"')
    expect(h).toContain('value="Asia/Kolkata"')
    expect(h).toContain('aria-label="Scheduled"')
    expect(h).not.toContain('>On<') // the weekday only for a weekly one
  })
  it('asks for a minute of the hour when hourly, and a weekday when weekly', () => {
    const hourly = render(value({ frequency: 'hourly', minute: '15' }))
    expect(hourly).toContain('Minute past the hour')
    expect(hourly).not.toContain('type="time"')
    const weekly = render(value({ frequency: 'weekly', weekday: 3 }))
    expect(weekly).toContain('>On<')
    expect(weekly).toContain('Wednesday')
  })
  it('says there is no script to run, with a link to the Script tab, and keeps the switch off', () => {
    const h = render(value({ scheduled: true }), { noScript: true })
    expect(h).toContain('There is no script yet')
    expect(h).toContain('href="/admin/pipelines/docs?tab=script"')
    expect(h).toMatch(/aria-label="Scheduled"[^>]*aria-checked="false"|aria-checked="false"[^>]*aria-label="Scheduled"/)
    expect(h).toContain('disabled=""')
  })
  it('tells a new pipeline to save first, without a link it cannot have', () => {
    const h = render(value(), { noScript: true, scriptHref: null })
    expect(h).toContain('Save the pipeline first')
    expect(h).not.toContain('Open the Script tab')
  })
  it('shows the server error on the switch with a way to the Script tab', () => {
    const h = render(value({ scheduled: true }), { errs: { scheduled: 'Save a script first: there is nothing to run' } })
    expect(h).toContain('role="alert"')
    expect(h).toContain('Save a script first: there is nothing to run')
    expect(h).toContain('Open the Script tab')
  })
  it('puts a server error on the time zone under its field', () => {
    expect(render(value(), { errs: { timezone: 'That is not a time zone name, for example Asia/Kolkata' } })).toContain('That is not a time zone name')
  })
})

describe('nextRunText', () => {
  it('says Not scheduled without a slot', () => {
    expect(nextRunText({ scheduled: false, next_run_at: null, timezone: 'UTC' })).toBe('Not scheduled')
    expect(nextRunText({ scheduled: true, next_run_at: null, timezone: 'UTC' })).toBe('Not scheduled')
  })
  it('names the time zone the slot is in', () => {
    const t = nextRunText({ scheduled: true, next_run_at: new Date(Date.now() + 3 * 3600_000).toISOString(), timezone: 'Asia/Kolkata' })
    expect(t).toContain('(Asia/Kolkata)')
    expect(t).toMatch(/in \d+ h$|in \d+ min$/)
  })
})
