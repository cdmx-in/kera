// Lazy chunk: Cowork. A task is a chat in mode "cowork": the assistant works on a goal with tools,
// files and knowledge, asks when blocked, and leaves files behind.
import { useEffect, useState } from 'react'
import { Link, Route, Routes, useNavigate, useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'
import { api } from '@/api/client'
import { useMe, useModels } from '@/api/queries'
import type { FileRef } from '@/api/types'
import { useChat } from '@/stores/chat'
import { useComposer } from '@/stores/composer'
import { toast } from '@/stores/toast'
import { cx } from '@/lib/format'
import { parseChatParams } from '@/app/search-params'
import { Spinner } from '@/ui/misc'
import { Select } from '@/ui/Select'
import { ModelIcon, ModelPicker } from '@/features/header/ModelPicker'
import { useModelSetup, type ModelPreset } from '@/features/chat/ChatPage'
import { Composer } from '@/features/chat/composer/Composer'
import { sendMessage } from '@/features/chat/actions'
import { useCollections } from '@/features/workspace/api'
import { input } from '@/features/workspace/ui'
import { WEEKDAYS, canCowork, canSchedule, clock, minutesOf, type Frequency, type Schedule, type TaskStatus } from './lib'
import { AppsPicker } from './AppsPicker'
import Scheduled, { CoworkTabs, loadSchedules, schedulesKey } from './Scheduled'
import TaskPage, { StatusBadge, TopBar } from './TaskPage'

interface TaskSummary {
  id: string
  title: string
  status: TaskStatus
  model: string
  files: number
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
        <div className="mx-auto w-full max-w-3xl p-4">
          {tasks.isPending ? (
            <div className="grid place-items-center py-10 text-muted">
              <Spinner size={20} />
            </div>
          ) : tasks.isError ? (
            <p className="py-10 text-center text-muted">Could not load tasks: {tasks.error.message}</p>
          ) : !tasks.data.length ? (
            <p className="py-10 text-center text-muted">No tasks yet. Start one and the assistant works on it while you do something else.</p>
          ) : (
            <ul className="divide-y divide-line">
              {tasks.data.map((t) => {
                const m = models.data?.find((x) => x.id === t.model)
                const sub = [m?.name ?? t.model, new Date(t.updated_at * 1000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }), `${t.files} file${t.files === 1 ? '' : 's'}`]
                return (
                  <li key={t.id} data-testid="task-row" data-task-id={t.id} data-status={t.status}>
                    <Link to={`/cowork/${t.id}`} className="flex items-center gap-3 rounded-xl px-2 py-3 hover:bg-surface-2">
                      {m ? <ModelIcon m={m} size="sm" /> : <span className="size-6 shrink-0" />}
                      <span className="min-w-0 flex-1">
                        <span className="block truncate font-medium">{taskTitle(t.title)}</span>
                        <span className="block truncate text-xs text-muted">{sub.filter(Boolean).join(' · ')}</span>
                      </span>
                      <StatusBadge status={t.status} />
                    </Link>
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      </div>
    </div>
  )
}

/** The new-task form; ?schedule=<id> opens a scheduled task for editing. */
function NewTask() {
  const [search] = useSearchParams()
  const id = search.get('schedule')
  const jobs = useQuery({ queryKey: schedulesKey(false), queryFn: () => loadSchedules(false), enabled: !!id })
  if (!id) return <TaskForm key="new" />
  if (jobs.isPending) {
    return (
      <div className="grid h-full place-items-center text-muted">
        <Spinner size={20} />
      </div>
    )
  }
  const job = jobs.data?.find((j) => j.id === id)
  if (!job) return <p className="grid h-full place-items-center p-6 text-center text-muted">This scheduled task no longer exists.</p>
  return <TaskForm key={job.id} job={job} />
}

const FREQUENCIES = [
  { value: 'hourly', label: 'Every hour' },
  { value: 'daily', label: 'Every day' },
  { value: 'weekdays', label: 'Weekdays' },
  { value: 'weekly', label: 'Every week' },
]

function TaskForm({ job }: { job?: Schedule }) {
  const [search] = useSearchParams()
  const params = parseChatParams(search)
  const navigate = useNavigate()
  const qc = useQueryClient()
  const me = useMe()
  const models = useModels()
  const model = useComposer((s) => s.model)
  const schedulable = canSchedule(me.data)
  const [collections, setCollections] = useState<FileRef[]>(job?.files?.filter((f) => f.type === 'collection') ?? [])
  const [kept, setKept] = useState<FileRef[]>(job?.files?.filter((f) => f.type !== 'collection') ?? []) // files a saved task already has
  const [freq, setFreq] = useState<'now' | Frequency>(job?.frequency ?? (schedulable && search.get('when') === 'daily' ? 'daily' : 'now'))
  const [time, setTime] = useState(clock(job && job.frequency !== 'hourly' ? job.at_minute : 120))
  const [minute, setMinute] = useState(job?.frequency === 'hourly' ? job.at_minute : 0)
  const [weekday, setWeekday] = useState(job?.weekday ?? 1)
  const [title, setTitle] = useState(job?.title ?? '')
  const [notes, setNotes] = useState(job?.notes ?? '')
  const preset: ModelPreset | undefined = job && { model_id: job.model_id, features: job.features, tool_ids: job.tool_ids }
  useModelSetup(job ? [job.model_id] : params.models, undefined, params.tools, preset)
  const scheduled = freq !== 'now'
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone

  // a chat or task left open in the store must not receive the new goal
  useEffect(() => {
    const st = useChat.getState()
    if (st.chatId || st.currentId || st.mode) st.reset(null)
  }, [])

  // Saves what the form would have started as a task, to run on the timetable instead.
  const save = async (text: string, files: FileRef[]) => {
    const c = useComposer.getState()
    const body = {
      title: title.trim() || text.split('\n')[0].slice(0, 60),
      prompt: text,
      model_id: c.model,
      tool_ids: c.toolIds,
      features: c.features,
      files: [...kept, ...collections, ...files],
      frequency: freq,
      at_minute: freq === 'hourly' ? minute : minutesOf(time),
      weekday,
      timezone: zone,
      ...(job ? { notes } : {}),
    }
    try {
      await api(job ? `/api/v1/cowork/schedules/${job.id}` : '/api/v1/cowork/schedules', { method: job ? 'PUT' : 'POST', json: body })
      await qc.invalidateQueries({ queryKey: ['cowork', 'schedules'] })
      navigate('/cowork?tab=scheduled')
      return true
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Could not save the scheduled task', 'error')
      return false
    }
  }

  // ponytail: ?q= only prefills; a task runs tools, so it never starts without a click.
  const start = async (text: string, files: FileRef[]) => {
    if (scheduled) return save(text, files)
    useChat.getState().reset(null, false, 'cowork')
    const r = await sendMessage(text, [...files, ...collections])
    if (r.ok && r.chatId) {
      navigate(`/cowork/${r.chatId}`)
      return true
    }
    useChat.getState().reset(null)
    return false
  }

  const description = models.data?.find((m) => m.id === model)?.info?.meta?.description
  return (
    <div className="flex h-full flex-col">
      <TopBar>
        <h1 className="flex-1 text-[15px] font-semibold">
          <Link to={job ? '/cowork?tab=scheduled' : '/cowork'} className="text-muted hover:text-fg">
            Cowork
          </Link>{' '}
          / {job ? 'Edit scheduled task' : 'New task'}
        </h1>
      </TopBar>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <form data-testid="new-task-form" onSubmit={(e) => e.preventDefault()} className="mx-auto w-full max-w-3xl space-y-5 p-4">
          <section>
            <h2 className="pb-1 text-sm font-medium">Assistant</h2>
            <ModelPicker />
            {description && <p className="px-2.5 text-xs text-muted">{description}</p>}
          </section>
          <CollectionPicker value={collections} onChange={setCollections} />
          <AppsPicker />
          {kept.length > 0 && (
            <section>
              <h2 className="pb-1 text-sm font-medium">Files</h2>
              <ul className="flex flex-wrap gap-2">
                {kept.map((f) => (
                  <li key={f.id} data-testid="kept-file" className="inline-flex items-center gap-1 rounded-full border border-line bg-surface-2 py-0.5 pl-3 pr-1 text-sm">
                    {f.name}
                    <button type="button" aria-label={`Remove ${f.name}`} onClick={() => setKept(kept.filter((x) => x.id !== f.id))} className="grid size-5 place-items-center rounded-full text-muted hover:bg-surface hover:text-fg">
                      <X size={12} />
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}
          {schedulable && (
            <section>
              <h2 className="pb-1 text-sm font-medium">When</h2>
              <div className="flex flex-wrap items-center gap-2">
                <Select
                  value={freq}
                  options={job ? FREQUENCIES : [{ value: 'now', label: 'Now (once)' }, ...FREQUENCIES]}
                  onChange={(v) => setFreq(v as 'now' | Frequency)}
                  aria-label="When to run"
                  data-testid="when"
                  className="w-44"
                />
                {freq === 'weekly' && <Select value={String(weekday)} options={WEEKDAYS.map((d, i) => ({ value: String(i), label: d }))} onChange={(v) => setWeekday(Number(v))} aria-label="Day of the week" data-testid="when-weekday" className="w-40" />}
                {(freq === 'daily' || freq === 'weekdays' || freq === 'weekly') && <input type="time" value={time} onChange={(e) => setTime(e.target.value)} aria-label="Time of day" data-testid="when-time" className={cx(input, 'max-w-32')} />}
                {freq === 'hourly' && (
                  <label className="flex items-center gap-1.5 text-sm">
                    at minute
                    <input type="number" min={0} max={59} value={minute} onChange={(e) => setMinute(Math.min(59, Math.max(0, Number(e.target.value) || 0)))} data-testid="when-minute" className={cx(input, 'max-w-20')} />
                  </label>
                )}
              </div>
              {scheduled && <p className="mt-1.5 text-xs text-muted">Runs by itself in your time zone ({zone}), using your connected apps. Each run appears under Tasks.</p>}
            </section>
          )}
          {scheduled && (
            <section>
              <h2 className="pb-1 text-sm font-medium">Name</h2>
              <input className={input} value={title} onChange={(e) => setTitle(e.target.value)} maxLength={120} placeholder="Defaults to the start of the goal" aria-label="Name" data-testid="schedule-title" />
            </section>
          )}
          <section>
            <h2 className="pb-1 text-sm font-medium">Goal</h2>
            <Composer variant="task" initialText={job?.prompt ?? params.q} sendLabel={scheduled ? (job ? 'Save changes' : 'Save schedule') : undefined} onSend={start} />
            <p className="mt-1.5 text-xs text-muted">
              {scheduled
                ? 'Nobody watches a scheduled run: it cannot ask you questions, and a step that needs a confirmation is skipped and reported.'
                : 'The assistant works on its own: it uses tools, files and knowledge, and asks when it is blocked.'}
            </p>
          </section>
          {job && (
            <section>
              <h2 className="pb-1 text-sm font-medium">Notes for the next run</h2>
              <textarea className={cx(input, 'min-h-24')} value={notes} onChange={(e) => setNotes(e.target.value)} maxLength={4000} aria-label="Notes for the next run" data-testid="schedule-notes" />
              <p className="mt-1.5 text-xs text-muted">The assistant writes these itself at the end of each run, so the next one knows where it left off. Change the assistant and they are cleared. Saved with the goal.</p>
            </section>
          )}
        </form>
      </div>
    </div>
  )
}

/** Knowledge collections the user can read, attached to the goal as {type:'collection'} files. */
function CollectionPicker({ value, onChange }: { value: FileRef[]; onChange: (v: FileRef[]) => void }) {
  const list = useCollections()
  if (!list.data?.length) return null
  const options = list.data.filter((c) => !value.some((v) => v.id === c.id)).map((c) => ({ value: c.id, label: c.name, description: c.description || undefined }))
  return (
    <section>
      <h2 className="pb-1 text-sm font-medium">Knowledge</h2>
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-2 pb-2">
          {value.map((c) => (
            <li key={c.id} data-testid="knowledge-chip" data-id={c.id} className="inline-flex items-center gap-1 rounded-full border border-line bg-surface-2 py-0.5 pl-3 pr-1 text-sm">
              {c.name}
              <button type="button" aria-label={`Remove ${c.name}`} onClick={() => onChange(value.filter((v) => v.id !== c.id))} className="grid size-5 place-items-center rounded-full text-muted hover:bg-surface hover:text-fg">
                <X size={12} />
              </button>
            </li>
          ))}
        </ul>
      )}
      {options.length > 0 && (
        <Select
          value=""
          options={options}
          placeholder="Add a knowledge collection"
          aria-label="Add a knowledge collection"
          data-testid="knowledge-picker"
          className="w-72 max-w-full"
          onChange={(id) => {
            const c = list.data.find((x) => x.id === id)
            if (c) onChange([...value, { id: c.id, name: c.name, type: 'collection' }])
          }}
        />
      )}
    </section>
  )
}
