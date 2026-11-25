// The timetable in words. The times themselves come from the server (POST /api/v1/cowork/schedules/preview),
// which owns the time zone and daylight-saving rules; this file only writes them down.
import { minutesOf, scheduleText } from '../lib'
import type { Draft, When } from './draft'

export const WHEN_OPTIONS: { value: When; label: string; hint: string }[] = [
  { value: 'now', label: 'Run once, now', hint: 'Starts as soon as you confirm' },
  { value: 'hourly', label: 'Every hour', hint: 'At a minute you choose' },
  { value: 'daily', label: 'Every day', hint: 'At a time you choose' },
  { value: 'weekdays', label: 'Weekdays', hint: 'Monday to Friday' },
  { value: 'weekly', label: 'Every week', hint: 'On a day you choose' },
]

/** "Every day at 06:00 (Asia/Kolkata)", or "Once, as soon as you start it". */
export function whenSentence(d: Pick<Draft, 'when' | 'time' | 'minute' | 'weekday' | 'timezone'>): string {
  if (d.when === 'now') return 'Once, as soon as you start it'
  return scheduleText({ frequency: d.when, at_minute: d.when === 'hourly' ? d.minute : minutesOf(d.time), weekday: d.weekday, timezone: d.timezone })
}

/** "today 18:00", "tomorrow 06:00", "Mon 30 Nov, 06:00" in the task's zone, with the zone's short name (the person's locale decides: "IST" for en-IN, else "GMT+5:30"). */
export function describeRun(epochSec: number, zone: string, now = Date.now(), locale = 'en-US'): string {
  const fmt = (o: Intl.DateTimeFormatOptions, ms = epochSec * 1000) => new Intl.DateTimeFormat('en-GB', { timeZone: zone, ...o }).format(ms)
  const day = (ms: number) => fmt({ year: 'numeric', month: '2-digit', day: '2-digit' }, ms)
  const clock = fmt({ hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  const abbr = new Intl.DateTimeFormat(locale, { timeZone: zone, timeZoneName: 'short' }).formatToParts(epochSec * 1000).find((p) => p.type === 'timeZoneName')?.value ?? zone
  const when =
    day(epochSec * 1000) === day(now) ? 'today' : day(epochSec * 1000) === day(now + 86400_000) ? 'tomorrow' : fmt({ weekday: 'short', day: 'numeric', month: 'short' }) + ','
  return `${when} ${clock} ${abbr}`
}

/** Common times to offer next to the time box. */
export const QUICK_TIMES = ['06:00', '09:00', '12:00', '18:00', '22:00']
