// The test run: the task, once, now, with what the wizard holds, and its result in place. It runs the way a
// scheduled run goes (nobody answers questions) but read-only: tools that may change data are not offered, so a
// test cannot write to a connected app. Nothing is scheduled; the run is kept as a task marked Test.
import { useState } from 'react'
import { Link } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { FlaskConical, Play, RotateCw, Square, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useModels } from '@/api/queries'
import { useChat } from '@/stores/chat'
import { toast } from '@/stores/toast'
import { Spinner } from '@/ui/misc'
import { useOpenChat } from '@/features/chat/ChatPage'
import { MessageList } from '@/features/chat/messages/MessageList'
import { stopStream } from '@/features/chat/stream'
import { taskStatus } from '../lib'
import { StatusBadge } from '../TaskPage'
import { Pill } from './ui'

/** The last test of this wizard: its chat, and the settings (as the request body) it ran with. The wizard holds it, so it is still here after a visit to another step. */
export interface LastTest {
  chatId?: string
  ranWith: string
}

export function TestRun({ body, signature, last, onChange }: { body: () => object; signature: string; last: LastTest; onChange: (t: LastTest) => void }) {
  const qc = useQueryClient()
  const models = useModels()
  const { chatId, ranWith } = last
  const [starting, setStarting] = useState(false)
  const state = useOpenChat(chatId, 'cowork')
  const status = useChat((s) => taskStatus(s.messages, s.currentId))
  const running = useChat((s) => s.streamingId !== null && s.streamingId === s.currentId) && state === 'ready'
  const modelName = (id?: string) => models.data?.find((m) => m.id === id)?.name ?? id ?? ''

  const start = async () => {
    setStarting(true)
    try {
      const r = await api<{ chat_id: string }>('/api/v1/cowork/test-run', { method: 'POST', json: body() })
      onChange({ chatId: r.chat_id, ranWith: signature })
      void qc.invalidateQueries({ queryKey: ['chats', 'cowork'] })
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Could not start the test run', 'error')
    } finally {
      setStarting(false)
    }
  }
  const discard = async () => {
    if (!chatId) return
    try {
      await api(`/api/v1/chats/${encodeURIComponent(chatId)}`, { method: 'DELETE' })
      onChange({ ranWith: '' })
      void qc.invalidateQueries({ queryKey: ['chats', 'cowork'] })
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Could not delete the test run', 'error')
    }
  }

  const busy = starting || running
  return (
    <section aria-label="Test run" data-testid="test-run" className="rounded-2xl border border-dashed border-accent/50 bg-accent-soft/40 p-4">
      <div className="flex flex-wrap items-start gap-3">
        <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-accent-soft text-accent-ink">
          <FlaskConical size={18} aria-hidden />
        </span>
        <div className="min-w-0 flex-[1_1_16rem]">
          <h3 className="font-semibold">Test it first</h3>
          <p className="mt-0.5 text-sm leading-relaxed text-muted">
            Runs the task once, right now, with exactly these settings. A test is <strong className="font-medium text-fg">read-only</strong>: the assistant can read your connected apps and search, but tools that may change things are left out, and it says what it would have changed. It cannot ask you questions, as in a scheduled run. Nothing is scheduled or saved.
          </p>
        </div>
        <button type="button" data-testid="run-test" disabled={busy} onClick={() => void start()} className="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-accent bg-surface px-3.5 py-2 text-sm font-medium text-accent-ink hover:bg-accent-soft disabled:opacity-50">
          {starting ? <Spinner size={14} /> : chatId ? <RotateCw size={14} aria-hidden /> : <Play size={14} aria-hidden />}
          {chatId ? 'Run the test again' : 'Run a test'}
        </button>
      </div>
      {chatId && (
        <div data-testid="test-result" data-status={status} className="mt-4 overflow-hidden rounded-xl border border-line bg-surface">
          <div className="flex flex-wrap items-center gap-2 border-b border-line bg-surface-2/50 px-3 py-2">
            <Pill tone="accent">Test, read-only</Pill>
            {state === 'ready' && <StatusBadge status={status} testId="test-status" />}
            <span className="flex-1" />
            {running && (
              <button type="button" data-testid="stop-test" onClick={() => void stopStream()} className="inline-flex items-center gap-1.5 rounded-lg border border-line px-2.5 py-1 text-xs font-medium hover:bg-surface-2">
                <Square size={11} aria-hidden /> Stop
              </button>
            )}
            <Link to={`/cowork/${chatId}`} className="text-xs font-medium text-link underline">
              Open as a task
            </Link>
            <button type="button" data-testid="discard-test" disabled={running} onClick={() => void discard()} className="inline-flex items-center gap-1 text-xs font-medium text-muted hover:text-fg disabled:opacity-50">
              <Trash2 size={12} aria-hidden /> Discard
            </button>
          </div>
          {ranWith !== signature && (
            <p role="status" data-testid="test-stale" className="border-b border-line bg-warn-soft px-3 py-2 text-xs text-warn-ink">
              You changed the task after this test. Run the test again to try the new settings.
            </p>
          )}
          {state === 'loading' ? (
            <div className="grid h-40 place-items-center text-muted">
              <Spinner size={20} />
            </div>
          ) : state === 'ready' ? (
            <div className="flex h-[26rem] max-h-[60dvh] flex-col">
              <MessageList readOnly modelName={modelName} />
            </div>
          ) : (
            <p role="alert" className="px-4 py-6 text-center text-sm text-muted">
              Could not load the test run. It is listed under Tasks.
            </p>
          )}
        </div>
      )}
    </section>
  )
}
