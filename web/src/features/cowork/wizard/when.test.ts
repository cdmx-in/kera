import { describe, expect, it } from 'vitest'
import { initialDraft } from './draft'
import { describeRun, whenSentence } from './when'

const d = { ...initialDraft({ zone: 'Asia/Kolkata', schedulable: true }), time: '06:00' }

describe('whenSentence', () => {
  it('says each choice in words, with the zone', () => {
    expect(whenSentence({ ...d, when: 'now' })).toBe('Once, as soon as you start it')
    expect(whenSentence({ ...d, when: 'daily' })).toBe('Every day at 06:00 (Asia/Kolkata)')
    expect(whenSentence({ ...d, when: 'weekdays', time: '09:30' })).toBe('Weekdays at 09:30 (Asia/Kolkata)')
    expect(whenSentence({ ...d, when: 'weekly', weekday: 5 })).toBe('Every Friday at 06:00 (Asia/Kolkata)')
    expect(whenSentence({ ...d, when: 'hourly', minute: 5 })).toBe('Every hour at :05 (Asia/Kolkata)')
  })
})

describe('describeRun', () => {
  // 2026-11-25 12:00 UTC is 17:30 in Kolkata
  const now = Date.UTC(2026, 10, 25, 12, 0)
  const at = (iso: string) => Date.parse(iso) / 1000
  it('says today and tomorrow in the task\'s own zone, with its short name', () => {
    expect(describeRun(at('2026-11-25T14:00:00Z'), 'Asia/Kolkata', now)).toBe('today 19:30 GMT+5:30')
    expect(describeRun(at('2026-11-25T20:30:00Z'), 'Asia/Kolkata', now)).toBe('tomorrow 02:00 GMT+5:30') // still the 25th in UTC, the 26th in Kolkata
    expect(describeRun(at('2026-11-25T14:00:00Z'), 'UTC', now)).toBe('today 14:00 UTC')
  })
  it('names the day for anything later', () => {
    expect(describeRun(at('2026-11-30T03:30:00Z'), 'UTC', now)).toBe('Mon 30 Nov, 03:30 UTC')
  })
  it('names the zone the way the person\'s locale does', () => {
    expect(describeRun(at('2026-11-25T14:00:00Z'), 'Asia/Kolkata', now, 'en-IN')).toBe('today 19:30 IST')
  })
  it('follows daylight saving in the zone', () => {
    expect(describeRun(at('2026-07-01T13:00:00Z'), 'America/New_York', Date.UTC(2026, 6, 1, 5, 0))).toBe('today 09:00 EDT')
  })
})
