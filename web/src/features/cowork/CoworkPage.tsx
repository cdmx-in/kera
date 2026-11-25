// Lazy chunk: Cowork. A task is a chat in mode "cowork": the assistant works on a goal with tools,
// files and knowledge, asks when blocked, and leaves files behind.
import { Link, Route, Routes, useSearchParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { ListChecks, Plus } from 'lucide-react'
import { api } from '@/api/client'
import { useMe, useModels } from '@/api/queries'
import { ModelIcon } from '@/features/header/ModelPicker'
import { cx } from '@/lib/format'
import { Spinner } from '@/ui/misc'
import { Card } from '@/features/admin/ui'
import { Empty, btnPrimary } from '@/features/workspace/ui'
import { canCowork, canSchedule, type TaskStatus } from './lib'
import Wizard from './wizard/Wizard'
import Scheduled, { CoworkTabs, loadSchedules, schedulesKey } from './Scheduled'
import TaskPage, { StatusBadge, TopBar } from './TaskPage'

interface TaskSummary {
  id: string
  title: string
  status: TaskStatus
  model: string
  files: number
  /** a read-only test run from the new-task wizard */
  test?: boolean
  created_at: number
  updated_at: number
}

export default function CoworkPage() {
  const me = useMe()
  if (me.data && !canCowork(me.data)) {
    return (
      <div className="flex h-full flex-col">
        <TopBar>
          <h1 className="flex-1 text-[15px] font-semibold">Cowork</h1>
        </TopBar>
        <p className="grid flex-1 place-items-center p-6 text-center text-muted">Cowork is not enabled for your account.</p>
      </div>
    )
  }
  return (
    <Routes>
      <Route index element={<Index />} />
      <Route path="new" element={<NewTask />} />
      <Route path=":taskId" element={<TaskPage />} />
    </Routes>
  )
}

const taskTitle = (t: string) => (!t || t === 'New Chat' ? 'New task' : t)

/** Tasks, or the Scheduled tab for people who may schedule (?tab=scheduled). */
function Index() {
  const me = useMe()
  const [search] = useSearchParams()
  const schedulable = canSchedule(me.data)
  return schedulable && search.get('tab') === 'scheduled' ? <Scheduled /> : <TaskList tabs={schedulable} />
}

/** Row grid shared by the header and the rows: Task, Assistant, Updated, Files, Status. */
const head = 'text-xs font-medium text-muted'
const taskCols = 'sm:grid-cols-[minmax(0,2.4fr)_minmax(0,1.2fr)_10rem_5rem_7rem]'

function TaskList({ tabs }: { tabs: boolean }) {
  const models = useModels()
  // ponytail: first 200 tasks (server LIMIT), no paging; add it when someone has more.
  const tasks = useQuery({
    queryKey: ['chats', 'cowork'],
    queryFn: () => api<TaskSummary[]>('/api/v1/cowork'),
    refetchInterval: (q) => (q.state.data?.some((t) => t.status === 'running' || t.status === 'needs_input') ? 5000 : false),
  })
  return (
    <div className="flex h-full flex-col">
      <TopBar>
        <h1 className="flex-1 text-[15px] font-semibold">Cowork</h1>
        <Link to="/cowork/new" data-testid="new-task" className="inline-flex items-center gap-1.5 rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-bright">
          <Plus size={15} /> New task
        </Link>
      </TopBar>
      {tabs && <CoworkTabs active="tasks" />}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[100rem] p-4 sm:p-6 lg:px-8">
          {tasks.isPending ? (
            <div className="grid place-items-center py-10 text-muted">
              <Spinner size={20} />
            </div>
          ) : tasks.isError ? (
            <p className="py-10 text-center text-muted">Could not load tasks: {tasks.error.message}</p>
          ) : !tasks.data.length ? (
            <Empty
              icon={ListChecks}
              title="No tasks yet"
              action={
                <Link to="/cowork/new" className={btnPrimary}>
                  <Plus size={15} /> New task
                </Link>
              }
            >
              Start one and the assistant works on it while you do something else. Its files and report are kept here.
            </Empty>
          ) : (
            <Card>
              <div className={cx('hidden gap-x-4 border-b border-line bg-surface-2/40 px-4 py-2 sm:grid', taskCols)} aria-hidden>
                <span className={head}>Task</span>
                <span className={head}>Assistant</span>
                <span className={head}>Updated</span>
                <span className={head}>Files</span>
                <span className={head}>Status</span>
              </div>
              <ul className="divide-y divide-line">
                {tasks.data.map((t) => {
                  const m = models.data?.find((x) => x.id === t.model)
                  const updated = new Date(t.updated_at * 1000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
                  const sub = [m?.name ?? t.model, updated, `${t.files} file${t.files === 1 ? '' : 's'}`]
                  return (
                    <li key={t.id} data-testid="task-row" data-task-id={t.id} data-status={t.status}>
                      <Link to={`/cowork/${t.id}`} className={cx('grid items-center gap-x-4 gap-y-0.5 px-4 py-3 hover:bg-surface-2/60', taskCols)}>
                        <span className="flex min-w-0 items-center gap-3">
                          {m ? <ModelIcon m={m} size="sm" /> : <span className="size-6 shrink-0" />}
                          <span className="min-w-0 flex-1">
                            <span className="flex items-center gap-2">
                              <span className="truncate font-medium">{taskTitle(t.title)}</span>
                              {t.test && (
                                <span data-testid="test-badge" className="shrink-0 rounded-full bg-accent-soft px-2 py-px text-[11px] font-medium text-accent-ink">
                                  Test
                                </span>
                              )}
                            </span>
                            <span className="block truncate text-xs text-muted sm:hidden">{sub.filter(Boolean).join(' · ')}</span>
                          </span>
                          <span className="sm:hidden">
                            <StatusBadge status={t.status} />
                          </span>
                        </span>
                        <span className="hidden truncate text-sm text-muted sm:block">{m?.name ?? t.model}</span>
                        <span className="hidden text-sm text-muted sm:block">{updated}</span>
                        <span className="hidden text-sm tabular-nums text-muted sm:block">{t.files}</span>
                        <span className="hidden sm:block">
                          <StatusBadge status={t.status} />
                        </span>
                      </Link>
                    </li>
                  )
                })}
              </ul>
            </Card>
          )}
        </div>
      </div>
    </div>
  )
}

/** The new-task wizard; ?schedule=<id> opens a scheduled task for editing. */
function NewTask() {
  const [search] = useSearchParams()
  const id = search.get('schedule')
  const jobs = useQuery({ queryKey: schedulesKey(false), queryFn: () => loadSchedules(false), enabled: !!id })
  if (!id) return <Wizard key="new" />
  if (jobs.isPending) {
    return (
      <div className="grid h-full place-items-center text-muted">
        <Spinner size={20} />
      </div>
    )
  }
  const job = jobs.data?.find((j) => j.id === id)
  if (!job) return <p className="grid h-full place-items-center p-6 text-center text-muted">This scheduled task no longer exists.</p>
  return <Wizard key={job.id} job={job} />
}
