// The "How it runs" part of the pipeline form: who starts the runs, and for a Kera-run pipeline its timetable.
import { Link } from 'react-router'
import { Switch } from '@/ui/misc'
import { cx } from '@/lib/format'
import { inputCls } from './ui'
import { relative } from './settings/Integrations'
import { bad, VField, type FormErrors } from './settings/dialogKit'
import { frequencies, weekdays, type Frequency, type RunMode, type Schedule, type ScheduleForm } from './pipelines'

const zones = (): string[] => {
  try {
    return typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  } catch {
    return []
  }
}

/** "Wed, 26 Nov, 09:00 (Asia/Kolkata), in 3 h", the next run in the timetable's own time zone; "Not scheduled" otherwise. */
export function nextRunText(s: Pick<Schedule, 'scheduled' | 'next_run_at' | 'timezone'>): string {
  if (!s.scheduled || !s.next_run_at) return 'Not scheduled'
  let at = s.next_run_at
  try {
    at = new Date(s.next_run_at).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: s.timezone })
  } catch {
    // an unknown zone name: show the instant as the server sent it
  }
  return `${at} (${s.timezone}), ${relative(s.next_run_at)}`
}

function Option({ checked, onSelect, title, hint }: { checked: boolean; onSelect: () => void; title: string; hint: string }) {
  return (
    <label className="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-surface p-3 has-[:checked]:border-accent has-[:checked]:bg-accent-soft">
      <input type="radio" name="run_mode" checked={checked} onChange={onSelect} className="mt-0.5 size-4 accent-[var(--accent)]" />
      <span className="min-w-0">
        <span className="block text-sm font-medium">{title}</span>
        <span className="block text-xs text-muted">{hint}</span>
      </span>
    </label>
  )
}

/**
 * Two ways to run a pipeline. `scriptHref` is the Script tab of an existing pipeline (null for a new one, which has no script to save yet);
 * `noScript` says nothing is saved there, so the timetable cannot be switched on.
 */
export function HowItRuns({ value, onChange, form, saved, scriptHref, noScript }: { value: ScheduleForm; onChange: (patch: Partial<ScheduleForm>) => void; form: FormErrors; saved: Schedule | null; scriptHref: string | null; noScript: boolean }) {
  const kera = value.mode === 'kera'
  const set = (mode: RunMode) => () => onChange({ mode })
  const scheduledError = form.err('scheduled')
  const list = kera ? zones() : []
  return (
    <div className="space-y-3">
      <fieldset className="space-y-2">
        <legend className="sr-only">Who starts the runs</legend>
        <Option checked={!kera} onSelect={set('external')} title="Started from outside (cron, CI, a script)" hint="Your own scheduler calls Kera with the pipeline's token. Kera only watches that the runs happen." />
        <Option checked={kera} onSelect={set('kera')} title="Run by Kera" hint="The script, its variables and its timetable are stored here. Kera queues the runs and a runner executes them." />
        {form.err('run_mode') && (
          <span role="alert" className="block text-xs text-red-600 dark:text-red-400">
            {form.err('run_mode')}
          </span>
        )}
      </fieldset>

      {kera && (
        <div className="space-y-3 rounded-lg border border-line bg-surface p-3" data-testid="timetable">
          <div className="grid gap-3 sm:grid-cols-2">
            <VField label="Repeats" error={form.err('frequency')}>
              <select name="frequency" value={value.frequency} onChange={(e) => onChange({ frequency: e.target.value as Frequency })} className={inputCls + bad(form.err('frequency'))}>
                {frequencies.map(([f, l]) => (
                  <option key={f} value={f}>
                    {l}
                  </option>
                ))}
              </select>
            </VField>
            {value.frequency === 'weekly' && (
              <VField label="On" error={form.err('weekday')}>
                <select name="weekday" value={value.weekday} onChange={(e) => onChange({ weekday: Number(e.target.value) })} className={inputCls + bad(form.err('weekday'))}>
                  {weekdays.map((d, i) => (
                    <option key={d} value={i}>
                      {d}
                    </option>
                  ))}
                </select>
              </VField>
            )}
            {value.frequency === 'hourly' ? (
              <VField label="Minute past the hour" hint="0 to 59." error={form.err('at_minute')}>
                <input name="at_minute" type="number" min={0} max={59} step={1} value={value.minute} onChange={(e) => onChange({ minute: e.target.value })} className={inputCls + bad(form.err('at_minute'))} />
              </VField>
            ) : (
              <VField label="At (HH:MM)" hint="24-hour clock, in the time zone below." error={form.err('at_minute')}>
                <input name="at_minute" type="time" step={60} value={value.time} onChange={(e) => onChange({ time: e.target.value })} className={inputCls + bad(form.err('at_minute'))} />
              </VField>
            )}
            <VField className="sm:col-span-2" label="Time zone" hint="An IANA name, for example Asia/Kolkata. The hour keeps its clock time when daylight saving changes." error={form.err('timezone')}>
              <input name="timezone" list="pipeline-zones" value={value.timezone} autoComplete="off" spellCheck={false} onChange={(e) => onChange({ timezone: e.target.value })} className={inputCls + bad(form.err('timezone'))} />
              <datalist id="pipeline-zones">
                {list.map((z) => (
                  <option key={z} value={z} />
                ))}
              </datalist>
            </VField>
          </div>

          <div className={cx('flex items-start gap-3 rounded-lg border border-line p-3', value.scheduled && 'border-green-500/50 bg-green-500/5')}>
            <Switch checked={value.scheduled && !noScript} disabled={noScript} onChange={(v) => (onChange({ scheduled: v }), form.touch('scheduled'))} label="Scheduled" testId="scheduled" />
            <div className="min-w-0 space-y-1">
              <p className="text-sm font-medium">Scheduled</p>
              <p className="text-xs text-muted">{value.scheduled && !noScript ? 'Kera starts a run by itself on this timetable.' : 'Off: a run starts only when you press Run now.'}</p>
              {noScript && (
                <p className="text-xs text-amber-700 dark:text-amber-400" data-testid="no-script">
                  {scriptHref ? 'There is no script yet, so there is nothing to run. ' : 'Save the pipeline first, then add its script. '}
                  {scriptHref && (
                    <Link to={scriptHref} className="underline">
                      Open the Script tab
                    </Link>
                  )}
                </p>
              )}
              {scheduledError && (
                <p role="alert" className="text-xs text-red-600 dark:text-red-400">
                  {scheduledError}{' '}
                  {scriptHref && (
                    <Link to={scriptHref} className="underline">
                      Open the Script tab
                    </Link>
                  )}
                </p>
              )}
              {saved && <p className="text-xs text-muted">Next run: {nextRunText(saved)}.</p>}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
