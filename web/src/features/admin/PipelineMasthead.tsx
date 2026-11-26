// The head of a pipeline's page: its name and health at a glance, how it runs, when it last worked, the actions, and the section tabs.
import { Link } from 'react-router'
import { Ellipsis, KeyRound, Pencil, Play, RefreshCw, Trash2 } from 'lucide-react'
import { Chip } from '@/features/workspace/ui'
import { cx } from '@/lib/format'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/ui/Menu'
import { Btn, ManagedBadge } from './ui'
import { runnersUrl, StatusBadge } from './Pipelines'
import { nextRunText } from './PipelineSchedule'
import { relative, when } from './settings/Integrations'
import { everyText, scheduleSummary, stateOf, type Pipeline, type Runner, type State } from './pipelines'
import { fillOf, type StatusTone } from './status'

export type Tab = 'runs' | 'script' | 'env'
export const tabs: [Tab, string][] = [
  ['runs', 'Runs'],
  ['script', 'Script'],
  ['env', 'Environment'],
]
/** A script that runs outside Kera has no script tab; its variables matter when its token may read them. */
export const tabsFor = (kera: boolean) => (kera ? tabs : tabs.filter(([k]) => k !== 'script'))

const stateTone: Record<State, StatusTone> = { failed: 'bad', late: 'warn', warning: 'warn', running: 'accent', queued: 'idle', ok: 'ok', never: 'idle', off: 'idle' }
const lampText: Record<StatusTone, string> = { idle: 'text-muted', accent: 'text-accent', ok: 'text-ok', warn: 'text-warn', bad: 'text-bad' }

const label = 'text-[10.5px] font-bold uppercase tracking-[0.1em] text-muted'

function Fact({ name, title, children }: { name: string; title?: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0" title={title}>
      <dt className={label}>{name}</dt>
      <dd className="mt-1 text-sm font-medium text-fg">{children}</dd>
    </div>
  )
}

export function PipelineMasthead({
  p,
  tab,
  tabHref,
  runners,
  starting,
  onRunNow,
  onEdit,
  onRotate,
  onDelete,
  onRefresh,
}: {
  p: Pipeline
  tab: Tab
  tabHref: (t: Tab) => string
  runners: Runner[] | undefined
  starting: boolean
  onRunNow: () => void
  onEdit: () => void
  onRotate: () => void
  onDelete: () => void
  onRefresh: () => void
}) {
  const kera = p.run_mode === 'kera'
  const state = stateOf(p)
  const tone = stateTone[state]
  const online = runners?.filter((r) => r.online).length
  return (
    <header className="relative overflow-hidden rounded-2xl border border-line bg-surface bg-[radial-gradient(52rem_12rem_at_0%_0%,var(--accent-soft),transparent)] shadow-[0_1px_2px_rgb(0_0_0/0.04)]" data-testid="masthead">
      <span aria-hidden className={cx('absolute inset-y-0 left-0 w-1', fillOf[tone])} />
      <div className="px-5 pb-4 pt-4 sm:px-6 sm:pt-5">
        <div className="flex flex-wrap items-start gap-x-6 gap-y-3">
          <div className="min-w-[min(100%,18rem)] flex-1">
            <h2 className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 text-xl font-semibold tracking-tight sm:text-2xl">
              <span aria-hidden className={cx('inline-block size-2.5 shrink-0 rounded-full bg-current', lampText[tone], state === 'running' && 'motion-safe:animate-beacon')} />
              <span className="min-w-0 break-words">{p.title || p.name}</span>
              <StatusBadge status={state} />
              {p.managed && <ManagedBadge />}
            </h2>
            <p className="mt-1 text-[13px] text-muted">
              <code>{p.name}</code> · Owner {p.owner.name || p.owner.email}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Btn variant="ghost" aria-label="Refresh" title="Refresh" onClick={onRefresh}>
              <RefreshCw size={14} />
            </Btn>
            {kera && (
              <Btn variant="primary" onClick={onRunNow} disabled={starting || !p.enabled} title={p.enabled ? 'Queue a run now, outside the timetable' : 'Switched off'} data-testid="run-now">
                <Play size={14} aria-hidden /> Run now
              </Btn>
            )}
            <Btn onClick={onEdit} data-testid="pipeline-edit">
              <Pencil size={14} aria-hidden /> Edit
            </Btn>
            <Menu>
              <MenuTrigger asChild>
                <Btn variant="ghost" className="px-2" aria-label="More actions" title="More actions" data-testid="pipeline-more">
                  <Ellipsis size={16} aria-hidden />
                </Btn>
              </MenuTrigger>
              <MenuContent align="end">
                <MenuItem testId="pipeline-rotate" onSelect={onRotate}>
                  <KeyRound size={14} aria-hidden /> Rotate token
                </MenuItem>
                <MenuSeparator />
                <MenuItem danger testId="pipeline-delete" onSelect={onDelete}>
                  <Trash2 size={14} aria-hidden /> Delete pipeline
                </MenuItem>
              </MenuContent>
            </Menu>
          </div>
        </div>

        <dl className="mt-4 flex flex-wrap gap-x-9 gap-y-3.5" data-testid="pipeline-facts">
          <Fact name="How it runs">
            {kera ? (
              <span data-testid="kera-timetable">Run by Kera: {p.schedule.scheduled ? scheduleSummary(p.schedule) : 'not scheduled'}</span>
            ) : (
              <span>Started from outside{p.every_minutes > 0 ? `: expected every ${everyText(p.every_minutes)}` : ': no expected interval'}</span>
            )}
          </Fact>
          {kera && p.schedule.scheduled && p.schedule.next_run_at && (
            <Fact name="Next run" title={nextRunText(p.schedule)}>
              {relative(p.schedule.next_run_at)}
            </Fact>
          )}
          <Fact name="Last good run" title={p.last_ok_at ? when(p.last_ok_at) : undefined}>
            {p.last_ok_at ? relative(p.last_ok_at) : <span className="font-normal text-muted">none yet</span>}
          </Fact>
          {kera && online !== undefined && (
            <Fact name="Runner">
              {online > 0 ? (
                <span className="inline-flex items-center gap-1.5">
                  <span aria-hidden className="size-2 rounded-full bg-ok" />
                  {online} online
                </span>
              ) : (
                <Link to={runnersUrl} className="inline-flex items-center gap-1.5 text-warn-ink underline-offset-2 hover:underline">
                  <span aria-hidden className="size-2 rounded-full bg-warn" />
                  None online
                </Link>
              )}
            </Fact>
          )}
        </dl>

        {p.targets.length > 0 && (
          <div className="mt-3.5 flex flex-wrap items-center gap-1.5">
            <span className={cx(label, 'mr-1')}>Writes to</span>
            {p.targets.map((t) => (
              <Chip key={t.id} tone={t.missing ? 'red' : 'grey'} title={t.missing ? 'This collection was deleted' : undefined}>
                {t.missing ? 'Deleted collection' : t.name}
              </Chip>
            ))}
          </div>
        )}
      </div>

      <nav aria-label="Pipeline sections" className="flex gap-1 border-t border-line px-3 sm:px-4" data-testid="pipeline-tabs">
        {tabsFor(kera).map(([k, l]) => (
          <Link
            key={k}
            to={tabHref(k)}
            aria-current={tab === k ? 'page' : undefined}
            className={cx(
              'relative px-3 py-2.5 text-sm font-medium outline-none transition-colors focus-visible:rounded-md focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent',
              tab === k ? 'text-fg after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full after:bg-accent' : 'text-muted hover:text-fg',
            )}
          >
            {l}
          </Link>
        ))}
      </nav>
    </header>
  )
}
