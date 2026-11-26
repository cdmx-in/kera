// Step 4: when the task runs. Once now, or on a timetable the server keeps (the same four frequencies as before),
// with the timetable in words and the next three runs as the server computes them.
import type { Dispatch } from 'react'
import { useQuery, keepPreviousData } from '@tanstack/react-query'
import { CalendarClock } from 'lucide-react'
import { api } from '@/api/client'
import { cx } from '@/lib/format'
import { Select } from '@/ui/Select'
import { input } from '@/features/workspace/ui'
import { WEEKDAYS, minutesOf } from '../../lib'
import type { Action, Draft } from '../draft'
import type { Errors } from '../steps'
import { QUICK_TIMES, WHEN_OPTIONS, describeRun, whenSentence } from '../when'
import { Block, StepHeader, choiceCard } from '../ui'

/** The next runs of the timetable in the draft, from the server's own timetable maths. */
export function useNextRuns(d: Draft, enabled: boolean, errors: Errors) {
  const valid = enabled && d.when !== 'now' && !errors.time && !errors.minute && !errors.timezone
  const body = { frequency: d.when, at_minute: d.when === 'hourly' ? d.minute : minutesOf(d.time), weekday: d.weekday, timezone: d.timezone }
  return useQuery({
    queryKey: ['cowork', 'preview', body],
    queryFn: async () => (await api<{ runs: number[] }>('/api/v1/cowork/schedules/preview', { method: 'POST', json: body })).runs,
    enabled: valid,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  })
}

const zones = (current: string): string[] => {
  const all = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  return all.includes(current) ? all : [current, ...all]
}

export function WhenStep({ draft, dispatch, errors, edit }: { draft: Draft; dispatch: Dispatch<Action>; errors: Errors; edit: boolean }) {
  const set = (patch: Partial<Draft>) => dispatch({ type: 'set', patch })
  const options = edit ? WHEN_OPTIONS.filter((o) => o.value !== 'now') : WHEN_OPTIONS
  const scheduled = draft.when !== 'now'
  const runs = useNextRuns(draft, true, errors)
  const timed = draft.when === 'daily' || draft.when === 'weekdays' || draft.when === 'weekly'
  return (
    <div className="space-y-6">
      <StepHeader title="When should it run?">Run it once right now, or put it on a timetable and the server starts it by itself, as you, with your connected apps. Every run appears under Tasks.</StepHeader>

      <div role="radiogroup" aria-label="When to run" data-testid="when" className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        {options.map((o) => (
          <label key={o.value} className={cx(choiceCard, 'flex-col gap-0.5')} data-testid={`when-${o.value}`}>
            <input type="radio" name="when" value={o.value} checked={draft.when === o.value} onChange={() => set({ when: o.value })} className="peer sr-only" />
            <span className="text-sm font-medium">{o.label}</span>
            <span className="text-xs leading-snug text-muted">{o.hint}</span>
          </label>
        ))}
      </div>

      {/* the timing beside what it means, where there is room for both */}
      <div className={cx('grid items-start gap-6', scheduled && 'xl:grid-cols-2')}>
        {scheduled && (
          <Block title="Timing">
            <div className="flex flex-wrap items-start gap-3">
              {draft.when === 'weekly' && (
                <label className="space-y-1">
                  <span className="block text-xs font-medium text-muted">Day</span>
                  <Select value={String(draft.weekday)} options={WEEKDAYS.map((d, i) => ({ value: String(i), label: d }))} onChange={(v) => set({ weekday: Number(v) })} aria-label="Day of the week" data-testid="when-weekday" className="w-40" />
                </label>
              )}
              {timed && (
                <div className="space-y-1">
                  <label htmlFor="wizard-time" className="block text-xs font-medium text-muted">
                    Time of day
                  </label>
                  <input id="wizard-time" type="time" value={draft.time} onChange={(e) => set({ time: e.target.value })} aria-label="Time of day" aria-invalid={!!errors.time} data-testid="when-time" className={cx(input, 'max-w-32')} />
                  <div className="flex flex-wrap gap-1.5 pt-1" aria-label="Common times" role="group">
                    {QUICK_TIMES.map((t) => (
                      <button key={t} type="button" aria-pressed={draft.time === t} onClick={() => set({ time: t })} className={cx('rounded-full border px-2.5 py-0.5 text-xs tabular-nums', draft.time === t ? 'border-accent bg-accent-soft' : 'border-line hover:bg-surface-2')}>
                        {t}
                      </button>
                    ))}
                  </div>
                </div>
              )}
              {draft.when === 'hourly' && (
                <label className="space-y-1">
                  <span className="block text-xs font-medium text-muted">At minute</span>
                  <input type="number" min={0} max={59} value={draft.minute} onChange={(e) => set({ minute: Math.min(59, Math.max(0, Math.trunc(Number(e.target.value)) || 0)) })} aria-invalid={!!errors.minute} data-testid="when-minute" className={cx(input, 'max-w-24')} />
                </label>
              )}
              <label className="min-w-0 space-y-1">
                <span className="block text-xs font-medium text-muted">Time zone</span>
                <select value={draft.timezone} onChange={(e) => set({ timezone: e.target.value })} aria-invalid={!!errors.timezone} data-testid="when-zone" className={cx(input, 'w-auto max-w-full')}>
                  {zones(draft.timezone).map((z) => (
                    <option key={z} value={z}>
                      {z.replace(/_/g, ' ')}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            {(errors.time || errors.minute || errors.timezone) && (
              <p role="alert" data-testid="field-error" className="text-xs font-medium text-red-600 dark:text-red-400">
                {errors.time ?? errors.minute ?? errors.timezone}
              </p>
            )}
          </Block>
        )}

        <section aria-label="Preview schedule" data-testid="schedule-preview" className="rounded-2xl border border-line bg-surface p-4">
          <div className="flex items-start gap-3">
            <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-accent-soft text-accent-ink">
              <CalendarClock size={18} aria-hidden />
            </span>
            <div className="min-w-0 flex-1">
              <p data-testid="when-summary" className="font-medium">
                {whenSentence(draft)}
              </p>
              {!scheduled ? (
                <p className="mt-0.5 text-sm text-muted">It starts when you confirm on the last step, and you can watch it work. Nothing is scheduled.</p>
              ) : runs.data?.length ? (
                <>
                  <p className="mt-0.5 text-sm text-muted">
                    Next run <span data-testid="next-run" className="font-medium text-fg">{describeRun(runs.data[0], draft.timezone, Date.now(), navigator.language)}</span>
                  </p>
                  <p className="mt-3 text-xs font-medium text-muted">Next {runs.data.length} runs</p>
                  <ol className="mt-1 space-y-0.5 text-sm tabular-nums" data-testid="next-runs">
                    {runs.data.map((r) => (
                      <li key={r}>{describeRun(r, draft.timezone, Date.now(), navigator.language)}</li>
                    ))}
                  </ol>
                </>
              ) : runs.isError ? (
                <p className="mt-0.5 text-sm text-muted">Could not work out the next runs. The timetable still saves.</p>
              ) : (
                <p className="mt-0.5 text-sm text-muted">{runs.isPending && !(errors.time || errors.minute) ? 'Working out the next runs…' : 'Fix the timing above to see the next runs.'}</p>
              )}
            </div>
          </div>
          {scheduled && <p className="mt-3 border-t border-line pt-3 text-xs leading-relaxed text-muted">Nobody watches a scheduled run: it cannot ask you questions, and a step that needs a confirmation is skipped and reported.</p>}
        </section>
      </div>
    </div>
  )
}
