// Lazy chunk: Cowork. A task is a chat in mode "cowork": the assistant works on a goal with tools,
// files and knowledge, asks when blocked, and leaves files behind.
import { useEffect, useState } from 'react'
import { Link, Route, Routes, useNavigate, useSearchParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'
import { api } from '@/api/client'
import { useMe, useModels } from '@/api/queries'
import type { FileRef } from '@/api/types'
import { useChat } from '@/stores/chat'
import { useComposer } from '@/stores/composer'
import { parseChatParams } from '@/app/search-params'
import { Spinner } from '@/ui/misc'
import { Select } from '@/ui/Select'
import { ModelIcon, ModelPicker } from '@/features/header/ModelPicker'
import { useModelSetup } from '@/features/chat/ChatPage'
import { Composer } from '@/features/chat/composer/Composer'
import { sendMessage } from '@/features/chat/actions'
import { useCollections } from '@/features/workspace/api'
import { canCowork, type TaskStatus } from './lib'
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
      <Route index element={<TaskList />} />
      <Route path="new" element={<NewTask />} />
      <Route path=":taskId" element={<TaskPage />} />
    </Routes>
  )
}

const taskTitle = (t: string) => (!t || t === 'New Chat' ? 'New task' : t)

function TaskList() {
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

function NewTask() {
  const [search] = useSearchParams()
  const params = parseChatParams(search)
  const navigate = useNavigate()
  const models = useModels()
  const model = useComposer((s) => s.model)
  const [collections, setCollections] = useState<FileRef[]>([])
  useModelSetup(params.models, undefined, params.tools)

  // a chat or task left open in the store must not receive the new goal
  useEffect(() => {
    const st = useChat.getState()
    if (st.chatId || st.currentId || st.mode) st.reset(null)
  }, [])

  // ponytail: ?q= only prefills; a task runs tools, so it never starts without a click.
  const start = async (text: string, files: FileRef[]) => {
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
          <Link to="/cowork" className="text-muted hover:text-fg">
            Cowork
          </Link>{' '}
          / New task
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
          <section>
            <h2 className="pb-1 text-sm font-medium">Goal</h2>
            <Composer variant="task" initialText={params.q} onSend={start} />
            <p className="mt-1.5 text-xs text-muted">The assistant works on its own: it uses tools, files and knowledge, and asks when it is blocked.</p>
          </section>
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
