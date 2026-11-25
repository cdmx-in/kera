// The run history: a dense list, one or two lines a run, the selected one marked. Narrow screens fold it into a disclosure.
import { useState } from 'react'
import { Link } from 'react-router'
import { cx } from '@/lib/format'
import { runUrl } from './Pipelines'
import { relative, when } from './settings/Integrations'
import { duration, isNotebookRun, runSummary, scriptLabel, triggerLabel, useWide, type Run } from './pipelines'
import { runChip } from './runview'
import { StatusChip } from './status'
import { Btn } from './ui'

const rail = (r: Run, on: boolean) => {
  if (on) return 'border-l-accent'
  if (r.status === 'failed') return 'border-l-bad'
  if (r.status === 'warning') return 'border-l-warn'
  if (r.status === 'running' && !r.queued) return 'border-l-accent/60'
  return 'border-l-transparent'
}

export function History({
  runs,
  loading,
  more,
  selected,
  name,
  now,
  onMore,
  notebookRuns = 0,
  showNotebook = false,
  onShowNotebook,
}: {
  runs: Run[]
  loading: boolean
  more: boolean
  selected: number | undefined
  name: string
  now: number
  onMore: () => void
  /** how many runs of the loaded ones came from the script notebook */
  notebookRuns?: number
  showNotebook?: boolean
  onShowNotebook?: (v: boolean) => void
}) {
  const wide = useWide()
  const [open, setOpen] = useState(false)
  const list = (
    <nav aria-label="Run history" data-testid="run-history">
      {loading && !runs.length ? (
        <p className="px-4 py-4 text-sm text-muted">Loading runs…</p>
      ) : (
        <ul className="max-h-[min(70dvh,46rem)] divide-y divide-line/70 overflow-y-auto [scrollbar-color:var(--color-line)_transparent] [scrollbar-width:thin]">
          {runs.map((r) => {
            const on = r.id === selected
            const sum = runSummary(r)
            const quiet = r.status === 'ok' && sum === 'No changes'
            return (
              <li key={r.id}>
                <Link
                  to={runUrl(name, r.id)}
                  onClick={() => setOpen(false)}
                  aria-current={on ? 'page' : undefined}
                  data-testid="run-item"
                  data-status={r.status}
                  data-queued={r.queued ? 'true' : undefined}
                  className={cx('block border-l-[3px] px-3 py-2 text-sm outline-none hover:bg-surface-2/60 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent', rail(r, on), on && 'bg-accent-soft')}
                >
                  <span className="flex items-center gap-2">
                    <span className="font-semibold tabular-nums">#{r.id}</span>
                    <StatusChip status={runChip(r)} bare />
                    {isNotebookRun(r) ? <StatusChip status="notebook" bare /> : r.dry_run && <StatusChip status="dry" bare />}
                    <span className="ml-auto text-xs tabular-nums text-muted">{duration(r.started_at, r.finished_at, now)}</span>
                  </span>
                  <span className="mt-0.5 block truncate text-xs text-muted" title={when(r.started_at)}>
                    {relative(r.started_at)} · {triggerLabel(r.trigger)}
                    {r.script_version ? ` · ${scriptLabel(r.script_version)}` : ''}
                  </span>
                  {!quiet && (
                    <span className={cx('mt-0.5 line-clamp-2 break-words text-xs', runChip(r) === 'failed' ? 'text-bad-ink' : 'text-fg/80')} title={r.status === 'failed' ? r.error : undefined}>
                      {sum}
                    </span>
                  )}
                </Link>
              </li>
            )
          })}
        </ul>
      )}
      {onShowNotebook && notebookRuns > 0 && (
        <label className="flex cursor-pointer items-center gap-2 border-t border-line px-3 py-2.5 text-xs text-muted">
          <input type="checkbox" checked={showNotebook} onChange={(e) => onShowNotebook(e.target.checked)} className="size-4 accent-[var(--accent)]" data-testid="show-notebook-runs" />
          Show notebook runs ({notebookRuns})
        </label>
      )}
      {more && (
        <div className="border-t border-line p-2.5">
          <Btn className="w-full" onClick={onMore} data-testid="load-older">
            Load older
          </Btn>
        </div>
      )}
    </nav>
  )
  const shell = 'overflow-hidden rounded-2xl border border-line bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)]'
  const count = `${runs.length}${more ? '+' : ''}`
  if (wide)
    return (
      <section className={shell}>
        <h3 className="flex items-baseline gap-2 border-b border-line px-4 py-3 text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">
          Run history <span className="rounded-full bg-surface-2 px-1.5 py-px text-[11px] font-medium tabular-nums normal-case tracking-normal">{count}</span>
        </h3>
        {list}
      </section>
    )
  return (
    <details open={open} onToggle={(e) => setOpen(e.currentTarget.open)} className={shell}>
      <summary className="cursor-pointer select-none px-4 py-3 text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted">
        Run history <span className="font-medium normal-case tracking-normal">({count})</span>
      </summary>
      <div className="border-t border-line">{list}</div>
    </details>
  )
}
