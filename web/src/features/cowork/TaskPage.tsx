// One task: the chat engine's live timeline, its ask/confirm cards, follow-ups and the files it produced.
import { useCallback } from 'react'
import { Link, useParams } from 'react-router'
import { useShallow } from 'zustand/react/shallow'
import { ArrowLeft } from 'lucide-react'
import { useModels } from '@/api/queries'
import { branchIds, useChat } from '@/stores/chat'
import { cx } from '@/lib/format'
import { Spinner } from '@/ui/misc'
import { SidebarToggle, UserMenu } from '@/features/header/Header'
import { ModelPicker } from '@/features/header/ModelPicker'
import { useModelSetup, useOpenChat, usePending } from '@/features/chat/ChatPage'
import { MessageList } from '@/features/chat/messages/MessageList'
import { FileChip } from '@/features/chat/messages/FileChip'
import { Composer } from '@/features/chat/composer/Composer'
import { AskUserCard } from '@/features/chat/AskUserCard'
import { ConfirmDialog } from '@/features/chat/ConfirmDialog'
import { sendMessage } from '@/features/chat/actions'
import { stopStream } from '@/features/chat/stream'
import { DesktopPanel } from './DesktopPanel'
import { STATUS_LABEL, STATUS_TONE, collectOutputs, taskStatus, type TaskStatus } from './lib'

export function TopBar({ children }: { children: React.ReactNode }) {
  return (
    <header className="flex h-14 shrink-0 items-center gap-2 px-3">
      <SidebarToggle />
      {children}
      <UserMenu />
    </header>
  )
}

export function StatusBadge({ status, testId }: { status: TaskStatus; testId?: string }) {
  return (
    <span data-testid={testId} data-status={status} className={cx('inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-xs font-medium', STATUS_TONE[status])}>
      {STATUS_LABEL[status]}
    </span>
  )
}

const onSend = async (text: string, files: Parameters<typeof sendMessage>[1]) => (await sendMessage(text, files)).ok

export default function TaskPage() {
  const { taskId } = useParams()
  const state = useOpenChat(taskId, 'cowork')
  useModelSetup([], taskId, [])
  const { pending, ask, confirm } = usePending()
  const models = useModels()
  const title = useChat((s) => s.title)
  const status = useChat((s) => taskStatus(s.messages, s.currentId))
  const outputs = useChat(useShallow((s) => collectOutputs(s.messages, branchIds(s.messages, s.currentId))))
  const modelName = useCallback((id?: string) => models.data?.find((m) => m.id === id)?.name ?? id ?? '', [models.data])
  // this task's own reply, not whatever run is current (an orphan task has none)
  const active = useChat((s) => s.streamingId !== null && s.streamingId === s.currentId) && state === 'ready'

  return (
    <div data-testid="task-page" data-task-id={taskId} data-status={status} className="flex h-full min-w-0 flex-1 flex-col">
      <TopBar>
        <Link to="/cowork" aria-label="Back to tasks" title="Back to tasks" className="grid size-8 shrink-0 place-items-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg">
          <ArrowLeft size={17} />
        </Link>
        <h1 className="min-w-0 truncate text-[15px] font-semibold">{!title || title === 'New Chat' ? 'New task' : title}</h1>
        {state === 'ready' && <StatusBadge status={status} testId="task-status" />}
        <div className="min-w-0 flex-1">
          <ModelPicker />
        </div>
        {active && (
          <button type="button" data-testid="cancel-task" onClick={() => void stopStream()} className="shrink-0 rounded-lg border border-line px-2.5 py-1 text-sm hover:bg-surface-2">
            Cancel
          </button>
        )}
      </TopBar>
      {state === 'loading' ? (
        <div className="grid flex-1 place-items-center text-muted">
          <Spinner size={20} />
        </div>
      ) : state === 'missing' ? (
        <div className="grid flex-1 place-items-center px-4 text-center text-muted">
          <p>
            This task does not exist or you do not have access to it.{' '}
            <Link to="/cowork" className="text-link underline">
              Back to tasks
            </Link>
          </p>
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col lg:flex-row">
          <div className="flex min-h-0 min-w-0 flex-1 flex-col">
            <MessageList readOnly modelName={modelName} />
            <div className="mx-auto w-full max-w-3xl px-3 pb-3 sm:px-4 sm:pb-4">
              {pending && ask && <AskUserCard key={ask.callId} messageId={pending.id} call={ask} />}
              <Composer onSend={onSend} />
              <p className="mt-1.5 text-center text-xs text-muted">Follow-up instructions start the assistant's next turn.</p>
            </div>
          </div>
          <aside className="flex shrink-0 flex-col border-line max-lg:border-t lg:w-80 lg:border-l lg:has-[[data-testid=desktop-panel]]:w-[34rem]">
            {taskId && <DesktopPanel chatId={taskId} live={status === 'running' || status === 'needs_input'} />}
            <section data-testid="task-outputs" aria-label="Outputs" className="min-h-0 overflow-y-auto p-3 max-lg:max-h-40">
              <h2 className="pb-2 text-xs font-medium text-muted">Outputs</h2>
              {outputs.length ? (
                <ul className="space-y-2">
                  {outputs.map((f) => (
                    <li key={f.id} data-testid="output-file" data-file-id={f.id} data-name={f.name}>
                      <FileChip file={f} />
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-sm text-muted">No files yet.</p>
              )}
            </section>
          </aside>
        </div>
      )}
      {pending && confirm && <ConfirmDialog key={confirm.callId} messageId={pending.id} call={confirm} />}
    </div>
  )
}
