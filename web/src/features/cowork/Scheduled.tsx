// The Scheduled tab of Cowork: saved tasks the server starts by itself. Each run is an ordinary task on the Tasks tab.
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CalendarClock, ListChecks, Pencil, Play, Plus, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useMe, useModels } from '@/api/queries'
import { cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { IconButton } from '@/ui/Dialog'
import { LoadFailed, Spinner, Switch } from '@/ui/misc'
import { Card, Confirm } from '@/features/admin/ui'
import { tabCls } from '@/features/admin/Frame'
import { ModelIcon } from '@/features/header/ModelPicker'
import { Empty, btn, btnPrimary } from '@/features/workspace/ui'
import { StatusBadge, TopBar } from './TaskPage'
import { scheduleText, type Schedule } from './lib'

const when = (sec: number) => new Date(sec * 1000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/** Row grid shared by the header and the rows: Task, Schedule, Next run, Last run, actions. */
const cols = 'sm:grid-cols-[minmax(0,2fr)_minmax(0,1.3fr)_11rem_minmax(0,1.3fr)_14.5rem]'
const head = 'text-xs font-medium text-muted'

/** Tasks | Scheduled with live counts: the Workspace/Admin tab strip, shown only to people who may schedule. */
export function CoworkTabs({ active }: { active: 'tasks' | 'scheduled' }) {
  const tasks = useQuery({ queryKey: ['chats', 'cowork'], queryFn: () => api<{ status: string }[]>('/api/v1/cowork') })
  const jobs = useQuery({ queryKey: schedulesKey(false), queryFn: () => loadSchedules(false) })
  const busy = tasks.data?.filter((t) => t.status === 'running' || t.status === 'needs_input').length ?? 0
  const tab = (to: string, label: string, Icon: typeof ListChecks, on: boolean, id: string, count: number, hot = false) => (
    <Link to={to} data-testid={id} aria-current={on ? 'page' : undefined} className={cx(tabCls({ isActive: on }), 'inline-flex items-center gap-1.5')}>
      <Icon size={15} aria-hidden className={on ? 'text-accent dark:text-accent-bright' : ''} />
      {label}
      {count > 0 && (
        <span data-testid={`${id}-count`} className={cx('rounded-full px-1.5 py-px text-xs font-medium tabular-nums', hot ? 'bg-accent-soft text-accent dark:text-accent-bright' : 'bg-surface-2 text-muted')}>
          {count}
        </span>
      )}
    </Link>
  )
  return (
    <nav aria-label="Cowork sections" className="flex flex-wrap gap-x-1 border-b border-line px-3">
      {tab('/cowork', 'Tasks', ListChecks, active === 'tasks', 'tab-tasks', busy, true)}
      {tab('/cowork?tab=scheduled', 'Scheduled', CalendarClock, active === 'scheduled', 'tab-scheduled', jobs.data?.length ?? 0)}
    </nav>
  )
}

export const schedulesKey = (all: boolean) => ['cowork', 'schedules', all] as const
export const loadSchedules = (all: boolean) => api<Schedule[]>(`/api/v1/cowork/schedules${all ? '?all=1' : ''}`)

export default function Scheduled() {
  const me = useMe()
  const models = useModels()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [all, setAll] = useState(false)
  const [doomed, setDoomed] = useState<Schedule | null>(null)
  const [busy, setBusy] = useState('')
  const admin = me.data?.role === 'admin'
  const jobs = useQuery({
    queryKey: schedulesKey(all),
    queryFn: () => loadSchedules(all),
    refetchInterval: (q) => (q.state.data?.some((j) => j.last_status === 'running') ? 5000 : false),
  })
  const refresh = () => qc.invalidateQueries({ queryKey: ['cowork', 'schedules'] })

  const toggle = async (j: Schedule, enabled: boolean) => {
    try {
      await api(`/api/v1/cowork/schedules/${j.id}`, { method: 'PUT', json: { enabled } })
      await refresh()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Could not change the task', 'error')
    }
  }
  const runNow = async (j: Schedule) => {
    setBusy(j.id)
    try {
      const r = await api<{ chat_id: string }>(`/api/v1/cowork/schedules/${j.id}/run`, { method: 'POST' })
      await refresh()
      navigate(`/cowork/${r.chat_id}`)
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Could not start the task', 'error')
    } finally {
      setBusy('')
    }
  }
  const remove = async () => {
    if (!doomed) return
    try {
      await api(`/api/v1/cowork/schedules/${doomed.id}`, { method: 'DELETE' })
      await refresh()
      setDoomed(null)
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Could not delete the task', 'error')
    }
  }

  return (
    <div className="flex h-full flex-col">
      <TopBar>
        <h1 className="flex-1 text-[15px] font-semibold">Cowork</h1>
        <Link to="/cowork/new?when=daily" data-testid="new-scheduled-task" className="inline-flex items-center gap-1.5 rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-bright">
          <Plus size={15} /> New scheduled task
        </Link>
      </TopBar>
      <CoworkTabs active="scheduled" />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[100rem] space-y-3 p-4 sm:p-6 lg:px-8">
          {admin && (
            <label className="flex items-center justify-end gap-2 text-sm text-muted">
              All users
              <Switch checked={all} onChange={setAll} label="Show every user's scheduled tasks" testId="schedules-all" />
            </label>
          )}
          {jobs.isPending ? (
            <div className="grid place-items-center py-10 text-muted">
              <Spinner size={20} />
            </div>
          ) : jobs.isError ? (
            <LoadFailed what="list of scheduled tasks" />
          ) : !jobs.data.length ? (
            <Empty
              icon={CalendarClock}
              title={all ? 'Nobody has scheduled a task yet' : 'No scheduled tasks yet'}
              action={
                !all && (
                  <Link to="/cowork/new?when=daily" className={btnPrimary}>
                    <Plus size={15} /> New scheduled task
                  </Link>
                )
              }
            >
              {all ? 'Scheduled tasks from every user show up here.' : 'Save a task with a schedule and the assistant runs it by itself, using your connected apps. Each run appears under Tasks.'}
            </Empty>
          ) : (
            <Card>
              <div className={cx('hidden gap-x-4 border-b border-line bg-surface-2/40 px-4 py-2 sm:grid', cols)} aria-hidden>
                <span className={head}>Task</span>
                <span className={head}>Schedule</span>
                <span className={head}>Next run</span>
                <span className={head}>Last run</span>
                <span className={head}>Actions</span>
              </div>
              <ul className="divide-y divide-line">
                {jobs.data.map((j) => {
                  const m = models.data?.find((x) => x.id === j.model_id)
                  return (
                    <li key={j.id} data-testid="schedule-row" data-id={j.id} data-enabled={j.enabled} className={cx('grid gap-x-4 gap-y-1.5 px-4 py-3 sm:items-center', cols)}>
                      <div className="flex min-w-0 items-center gap-3">
                        {m ? <ModelIcon m={m} size="sm" /> : <span className="size-6 shrink-0" />}
                        <span className="min-w-0">
                          <span className="block truncate font-medium">{j.title}</span>
                          <span className="block truncate text-xs text-muted">{[m?.name ?? j.model_id, j.owner && (j.owner.name || j.owner.email)].filter(Boolean).join(' · ')}</span>
                        </span>
                      </div>
                      <span className="text-sm text-muted sm:text-fg">{scheduleText(j)}</span>
                      <span className="text-sm">{j.enabled ? <span className="text-muted sm:text-fg">
                          <span className="sm:hidden">Next </span>
                          {when(j.next_run_at)}
                        </span> : <span className="rounded-full bg-surface-2 px-2 py-0.5 text-xs font-medium text-muted">Paused</span>}</span>
                      <div className="min-w-0 text-sm">
                        {j.last_chat_id && j.last_run_at ? (
                          <Link to={`/cowork/${j.last_chat_id}`} data-testid="last-run" className="inline-flex flex-wrap items-center gap-2 text-muted hover:text-fg">
                            {when(j.last_run_at)}
                            {j.last_status && <StatusBadge status={j.last_status} />}
                          </Link>
                        ) : (
                          <span className="text-muted">Never run</span>
                        )}
                      </div>
                      {!j.owner ? (
                        <div className="flex items-center gap-1">
                          <Switch checked={j.enabled} onChange={(v) => void toggle(j, v)} label={`${j.enabled ? 'Pause' : 'Resume'} ${j.title}`} testId="schedule-enabled" />
                          <button type="button" className={btn} disabled={busy === j.id || j.last_status === 'running'} onClick={() => void runNow(j)} data-testid="run-now">
                            <Play size={14} /> Run now
                          </button>
                          <Link to={`/cowork/new?schedule=${j.id}`} aria-label={`Edit ${j.title}`} title="Edit" data-testid="edit-schedule" className="inline-flex size-8 items-center justify-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg">
                            <Pencil size={15} />
                          </Link>
                          <IconButton label={`Delete ${j.title}`} onClick={() => setDoomed(j)} data-testid="delete-schedule">
                            <Trash2 size={15} />
                          </IconButton>
                        </div>
                      ) : (
                        <span />
                      )}
                      {j.last_error && (
                        <p data-testid="last-error" className="break-words text-xs text-red-600 sm:col-span-full dark:text-red-400">
                          {j.last_error}
                        </p>
                      )}
                    </li>
                  )
                })}
              </ul>
            </Card>
          )}
        </div>
      </div>
      <Confirm open={!!doomed} title={`Delete “${doomed?.title ?? ''}”?`} body="It stops running. Tasks it already ran stay on the Tasks tab." onConfirm={remove} onClose={() => setDoomed(null)} />
    </div>
  )
}
