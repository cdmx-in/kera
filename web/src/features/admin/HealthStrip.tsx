// The health strip: the last runs as thin ticks, newest at the right. Height is how long a run took; the silhouette says how it ended
// (flat = OK, notched = warning, pointed = failed, striped = running, dashed = queued, outlined = dry run), colour backs it up.
import { useState } from 'react'
import { Link } from 'react-router'
import { cx } from '@/lib/format'
import { relative } from './settings/Integrations'
import { duration, triggerLabel, type Run } from './pipelines'
import { healthSummary, healthTicks, type Tick, type TickStatus } from './runview'
import { inkOf, statusTone, statusWord } from './status'

const look: Record<TickStatus, string> = {
  ok: 'bg-ok/55 hover:bg-ok',
  warning: 'bg-warn [clip-path:polygon(0_100%,0_0,32%_0,50%_26%,68%_0,100%_0,100%_100%)]',
  failed: 'bg-bad [clip-path:polygon(0_100%,0_22%,50%_0,100%_22%,100%_100%)]',
  cancelled: 'bg-warn/50',
  running: 'bg-accent bg-[linear-gradient(45deg,rgb(255_255_255/0.28)_25%,transparent_25%,transparent_50%,rgb(255_255_255/0.28)_50%,rgb(255_255_255/0.28)_75%,transparent_75%)] [background-size:0.6rem_0.6rem] motion-safe:animate-stripes',
  queued: 'border border-dashed border-muted bg-transparent',
}

const dur = (t: Tick) => (t.ms > 0 ? duration('1970-01-01T00:00:00Z', new Date(t.ms).toISOString()) : '0 s')

/** "#41 · FAILED · 3 h ago · 4 min 2 s · Schedule" as plain text, for the readout and the screen reader. */
export const tickText = (t: Tick): string => `#${t.id}, ${statusWord(t.status === 'cancelled' ? 'cancelled' : t.status)}${t.dry ? ', dry run' : ''}, ${relative(t.startedAt)}, ${dur(t)}, ${triggerLabel(t.trigger)}`

export function HealthStrip({ runs, selected, urlOf, now, more }: { runs: Run[]; selected: number | undefined; urlOf: (run: number) => string; now: number; more?: boolean }) {
  const [hot, setHot] = useState<Tick | null>(null)
  const ticks = healthTicks(runs, now)
  const h = healthSummary(runs)
  // the strip is one tab stop: the selected run, else the newest
  const stop = ticks.some((t) => t.id === selected) ? selected : ticks[ticks.length - 1]?.id
  if (!ticks.length) return null
  return (
    <section aria-label="Run health" data-testid="health-strip" className="rounded-2xl border border-line bg-surface px-4 py-3 shadow-[0_1px_2px_rgb(0_0_0/0.04)]">
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-0.5">
        <h3 className="text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">Last {ticks.length >= 50 ? 50 : ticks.length} {ticks.length === 1 ? 'run' : 'runs'}</h3>
        <p className="ml-auto text-xs tabular-nums text-muted" data-testid="health-summary">
          {h.total > 0 ? (
            <>
              <b className={cx('font-semibold', h.ok === h.total ? 'text-ok-ink' : 'text-fg')}>
                {h.ok} of {h.total}
              </b>{' '}
              ok{h.failed > 0 && <span className="text-bad-ink"> · {h.failed} failed</span>}
              {h.warning > 0 && <span className="text-warn-ink"> · {h.warning} with warnings</span>}
              {h.median !== null && ` · median ${duration('1970-01-01T00:00:00Z', new Date(h.median).toISOString())}`}
            </>
          ) : (
            'No finished runs yet'
          )}
        </p>
      </div>
      <ol
        className="mt-2.5 flex h-10 items-end justify-end gap-[2px] pb-2 sm:gap-[3px]"
        aria-label="Runs, oldest to newest. Arrow keys move between them."
        onKeyDown={(e) => {
          // one tab stop for the whole strip; the arrows walk it (the history list has every run too)
          const links = [...e.currentTarget.querySelectorAll('a')]
          const i = links.indexOf(document.activeElement as HTMLAnchorElement)
          const to = e.key === 'ArrowLeft' ? i - 1 : e.key === 'ArrowRight' ? i + 1 : e.key === 'Home' ? 0 : e.key === 'End' ? links.length - 1 : null
          if (to === null || i < 0) return
          e.preventDefault()
          links[Math.max(0, Math.min(links.length - 1, to))]?.focus()
        }}
      >
        {ticks.map((t) => {
          const on = t.id === selected
          return (
            <li key={t.id} className={cx('relative flex h-full max-w-[16px] min-w-[3px] flex-1 items-end', on && 'rounded-[3px] bg-accent-soft outline outline-1 outline-accent/50 after:absolute after:-bottom-2 after:left-1/2 after:size-1 after:-translate-x-1/2 after:rounded-full after:bg-accent')}>
              <Link
                to={urlOf(t.id)}
                aria-label={tickText(t)}
                aria-current={on ? 'page' : undefined}
                tabIndex={t.id === stop ? 0 : -1}
                data-status={t.status}
                onMouseEnter={() => setHot(t)}
                onMouseLeave={() => setHot(null)}
                onFocus={() => setHot(t)}
                onBlur={() => setHot(null)}
                className={cx(
                  'block w-full rounded-[2px] outline-none transition-colors focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-1 focus-visible:ring-offset-surface',
                  look[t.status],
                  t.dry && 'opacity-50 saturate-50',
                )}
                style={{ height: `${Math.round(t.h * 100)}%`, minHeight: 6 }}
              />
            </li>
          )
        })}
      </ol>
      <p className="mt-1 min-h-4 text-[11.5px] leading-4 tabular-nums text-muted" aria-hidden data-testid="health-readout">
        {hot ? (
          <>
            <b className="text-fg">#{hot.id}</b> <span className={cx('font-bold uppercase tracking-[0.06em]', inkOf[statusTone(hot.status)])}>{statusWord(hot.status)}</span>
            {hot.dry && ' · dry run'} · {relative(hot.startedAt)} · {dur(hot)} · {triggerLabel(hot.trigger)}
          </>
        ) : (
          `Newest on the right. Taller means longer${more ? '. Load older runs to see further back' : ''}.`
        )}
      </p>
    </section>
  )
}
