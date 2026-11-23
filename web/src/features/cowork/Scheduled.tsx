// The Scheduled tab of Cowork: saved tasks the server starts by itself. Each run is an ordinary task on the Tasks tab.
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Play, Plus, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useMe, useModels } from '@/api/queries'
import { cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { IconButton } from '@/ui/Dialog'
import { LoadFailed, Spinner, Switch } from '@/ui/misc'
import { Confirm } from '@/features/admin/ui'
import { ModelIcon } from '@/features/header/ModelPicker'
import { btn } from '@/features/workspace/ui'
import { StatusBadge, TopBar } from './TaskPage'
import { scheduleText, type Schedule } from './lib'

const when = (sec: number) => new Date(sec * 1000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/** Tasks | Scheduled, shown only to people who may schedule. */
export function CoworkTabs({ active }: { active: 'tasks' | 'scheduled' }) {
  const tab = (to: string, label: string, on: boolean, id: string) => (
    <Link to={to} data-testid={id} aria-current={on ? 'page' : undefined} className={cx('rounded-lg px-3 py-1.5 text-sm', on ? 'bg-surface-2 font-medium' : 'text-muted hover:text-fg')}>
      {label}
    </Link>
  )
  return (
    <nav aria-label="Cowork" className="mx-auto flex w-full max-w-3xl gap-1 px-4">
      {tab('/cowork', 'Tasks', active === 'tasks', 'tab-tasks')}
      {tab('/cowork?tab=scheduled', 'Scheduled', active === 'scheduled', 'tab-scheduled')}
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
        <div className="mx-auto w-full max-w-3xl p-4">
          {admin && (
            <label className="mb-2 flex items-center justify-end gap-2 text-sm text-muted">
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
            <p className="py-10 text-center text-muted">{all ? 'Nobody has scheduled a task yet.' : 'No scheduled tasks yet. Save a task with a schedule and the assistant runs it by itself, using your connected apps.'}</p>
          ) : (
            <ul className="divide-y divide-line">
              {jobs.data.map((j) => {
                const m = models.data?.find((x) => x.id === j.model_id)
                const next = j.enabled ? `next ${when(j.next_run_at)}` : 'paused'
                return (
                  <li key={j.id} data-testid="schedule-row" data-id={j.id} data-enabled={j.enabled} className="flex flex-wrap items-start gap-x-3 gap-y-2 px-2 py-3">
                    {m ? <ModelIcon m={m} size="sm" /> : <span className="size-6 shrink-0" />}
                    <div className="min-w-0 flex-1 basis-56">
                      <span className="block truncate font-medium">{j.title}</span>
                      <span className="block truncate text-xs text-muted">{[scheduleText(j), m?.name ?? j.model_id, next, j.owner && (j.owner.name || j.owner.email)].filter(Boolean).join(' · ')}</span>
                      {j.last_chat_id && j.last_run_at && (
                        <Link to={`/cowork/${j.last_chat_id}`} data-testid="last-run" className="mt-1 inline-flex items-center gap-2 text-xs text-muted hover:text-fg">
                          Last run {when(j.last_run_at)}
                          {j.last_status && <StatusBadge status={j.last_status} />}
                        </Link>
                      )}
                      {j.last_error && (
                        <p data-testid="last-error" className="mt-1 break-words text-xs text-red-600 dark:text-red-400">
                          {j.last_error}
                        </p>
                      )}
                    </div>
                    {!j.owner && (
                      <div className="flex w-full shrink-0 items-center gap-1 pl-9 sm:w-auto sm:pl-0">
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
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      </div>
      <Confirm open={!!doomed} title={`Delete “${doomed?.title ?? ''}”?`} body="It stops running. Tasks it already ran stay on the Tasks tab." onConfirm={remove} onClose={() => setDoomed(null)} />
    </div>
  )
}
