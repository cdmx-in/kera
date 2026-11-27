// Runs a completion over SSE and folds its events into the chat store, one
// store update per animation frame. A dropped connection resumes from the last
// seq via GET .../messages/{mid}/stream?after=<seq>. A reply goes on while the
// server restarts, so the stream is reopened for about a minute before the
// message is shown as lost.

import { ApiError, apiFetch, api } from '@/api/client'
import { readSSE } from '@/api/sse'
import type { Message, StreamEvent } from '@/api/types'
import { useChat } from '@/stores/chat'
import { toast } from '@/stores/toast'
import { queryClient } from '@/app/query'
import { fileAttach } from './attach'
import { applyEvent, messageText } from './reduce'

export function decodeEvent(event: string, data: string): StreamEvent | null {
  try {
    const d = JSON.parse(data)
    if (!d || typeof d !== 'object') return null
    const type = event && event !== 'message' ? event : d.type
    return typeof type === 'string' ? { ...d, type } : null
  } catch {
    return null
  }
}

export interface StreamLoop {
  start: () => Promise<Response>
  resume: (after: number) => Promise<Response>
  onEvent: (ev: StreamEvent) => void
  signal: AbortSignal
  after?: number
  retries?: number
  delayMs?: (attempt: number) => number
}

/**
 * Reads events until `done` or `error`, resuming after drops. Events at or
 * below the last seen seq are skipped (replays after a resume). `retries` is
 * how many times in a row a stream may bring nothing new; one that brought an
 * event starts the count again. Returns 'finished', 'aborted' or 'lost'.
 */
export async function streamLoop(o: StreamLoop): Promise<'finished' | 'aborted' | 'lost'> {
  let last = o.after ?? 0
  let finished = false
  let received = false
  const retries = o.retries ?? 10
  const handle = (event: string, data: string) => {
    const ev = decodeEvent(event, data)
    if (!ev) return
    if (typeof ev.seq === 'number') {
      if (ev.seq <= last) return
      last = ev.seq
    }
    if (ev.type === 'done' || ev.type === 'error') finished = true
    received = true
    o.onEvent(ev)
  }
  let failures = 0
  for (let first = true; ; first = false) {
    if (o.signal.aborted) return 'aborted'
    received = false
    try {
      const res = first && o.after == null ? await o.start() : await o.resume(last)
      if (!res.body) throw new Error('no body')
      await readSSE(res.body, (m) => handle(m.event, m.data), o.signal)
      if (finished) return 'finished'
    } catch (e) {
      if (o.signal.aborted) return 'aborted'
      // a 4xx on the first request is a real error (bad model, local lock): surface it
      if (first && o.after == null && (e as { status?: number }).status && (e as { status: number }).status < 500) throw e
    }
    if (received) failures = 0
    if (failures >= retries) return o.signal.aborted ? 'aborted' : 'lost'
    await new Promise((r) => setTimeout(r, (o.delayMs ?? ((n) => Math.min(8000, 500 * 2 ** n)))(failures)))
    failures++
  }
}

// ---- store wiring -----------------------------------------------------------

interface Run {
  chatId: string | null
  messageId: string
  controller: AbortController
  /** Replaced by a newer run (chat switch, resume): the server keeps going, so no "stopped". */
  superseded?: boolean
  queue: StreamEvent[]
  scheduled: boolean
}

let current: Run | null = null
const newRun = (chatId: string | null, messageId: string): Run => ({ chatId, messageId, controller: new AbortController(), queue: [], scheduled: false })

const raf = (fn: () => void) => (typeof requestAnimationFrame === 'function' && !document.hidden ? requestAnimationFrame(fn) : setTimeout(fn, 16))

function flush(run: Run) {
  run.scheduled = false
  const evs = run.queue
  run.queue = []
  if (!evs.length || run.superseded) return
  const st = useChat.getState()
  if (st.chatId !== run.chatId && !(st.temporary && run.chatId?.startsWith('temp:'))) return
  st.patch(run.messageId, (m) => evs.reduce((acc, e) => applyEvent(acc, e), m))
}

function onEvent(run: Run, ev: StreamEvent) {
  if (ev.chatId && !run.chatId) {
    run.chatId = ev.chatId
    if (useChat.getState().temporary) useChat.getState().setChatId(ev.chatId)
  }
  run.queue.push(ev)
  if (!run.scheduled) {
    run.scheduled = true
    raf(() => flush(run))
  }
  switch (ev.type) {
    case 'title':
      if (typeof ev.title === 'string' && useChat.getState().chatId === run.chatId) useChat.getState().setTitle(ev.title)
      void queryClient.invalidateQueries({ queryKey: ['chats'] })
      break
    case 'error':
      toast(ev.code === 'local_lock' ? 'This chat used a local model, so it stays on local models.' : String(ev.message ?? 'The model returned an error'), 'error')
      break
    case 'done':
      void queryClient.invalidateQueries({ queryKey: ['chats'] })
      break
  }
}

function finish(run: Run, outcome: 'finished' | 'aborted' | 'lost', err?: unknown) {
  if (run.superseded) return
  flush(run)
  const st = useChat.getState()
  if (outcome !== 'finished') {
    st.patch(run.messageId, (m) =>
      m.status !== 'streaming'
        ? m
        : outcome === 'aborted'
          ? { ...m, status: 'stopped', statusLine: undefined }
          : { ...m, status: 'error', statusLine: undefined, error: { message: err instanceof Error ? err.message : 'Connection lost' , code: (err as { code?: string })?.code } },
    )
    if (err && (err as { code?: string }).code === 'local_lock') toast('This chat used a local model, so it stays on local models.', 'error')
  }
  if (current === run) {
    current = null
    if (st.streamingId === run.messageId) st.setStreaming(null)
  }
}

async function drive(run: Run, start: (() => Promise<Response>) | null, after?: number) {
  if (current) {
    current.superseded = true
    current.controller.abort()
  }
  current = run
  useChat.getState().setStreaming(run.messageId)
  const resume = (n: number) => {
    if (!run.chatId) return Promise.reject(new Error('cannot resume'))
    return apiFetch(`/api/v1/chats/${encodeURIComponent(run.chatId)}/messages/${encodeURIComponent(run.messageId)}/stream?after=${n}`, { signal: run.controller.signal })
  }
  try {
    const outcome = await streamLoop({ start: start ?? (() => resume(after ?? 0)), resume, after: start ? undefined : (after ?? 0), onEvent: (ev) => onEvent(run, ev), signal: run.controller.signal })
    finish(run, outcome)
  } catch (e) {
    finish(run, 'lost', e)
  }
}

export interface SendInput {
  chatId: string | null
  temporary: boolean
  model: string
  user: Message
  assistantId: string
  features: Record<string, boolean>
  toolIds: string[]
}

/** Starts a completion for `user` (already in the store) into a new assistant message. */
export function startCompletion(i: SendInput) {
  const st = useChat.getState()
  const assistant: Message = { id: i.assistantId, parentId: i.user.id, childrenIds: [], role: 'assistant', model: i.model, createdAt: Date.now(), status: 'streaming', parts: [], files: [], sources: [], pendingCalls: [] }
  st.put(assistant)
  st.setCurrent(assistant.id)
  const history = historyFor(i.user.id)
  const run = newRun(i.chatId, assistant.id)
  const body = {
    model: i.model,
    stream: true,
    stream_format: 'events',
    chat_id: i.chatId ?? undefined,
    id: assistant.id,
    parent_id: i.user.id,
    user_message: { id: i.user.id, parentId: i.user.parentId, content: messageText(i.user), files: i.user.files },
    messages: history,
    files: i.user.files.map(fileAttach),
    features: i.features,
    tool_ids: i.toolIds,
    temporary: i.temporary || undefined,
  }
  void drive(run, () => apiFetch('/api/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' }, body: JSON.stringify(body), signal: run.controller.signal }))
}

/** Re-attaches to a message that is still streaming (page reload, chat switch). */
export function resumeStream(chatId: string, messageId: string, after = 0) {
  void drive(newRun(chatId, messageId), null, after)
}

/** Stops the reply streaming in the open chat; a run left running in another chat is not touched. */
export async function stopStream() {
  const run = current
  if (!run || useChat.getState().streamingId !== run.messageId) return
  // Cancel on the server first: aborting first showed "Stopped" while a failed
  // cancel left the answer generating. 404 = the run already ended.
  if (run.chatId) {
    try {
      await api(`/api/v1/chats/${encodeURIComponent(run.chatId)}/messages/${encodeURIComponent(run.messageId)}/cancel`, { method: 'POST' })
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 404)) {
        toast(`Could not stop the answer: ${(e as Error).message}`, 'error')
        return
      }
    }
  }
  run.controller.abort()
}

/** Answers an ask_user/confirm call over HTTP; the run continues on the open stream. */
export async function answerCall(messageId: string, callId: string, value: unknown) {
  const { chatId } = useChat.getState()
  if (!chatId) throw new Error('no chat')
  await api(`/api/v1/chats/${encodeURIComponent(chatId)}/messages/${encodeURIComponent(messageId)}/calls/${encodeURIComponent(callId)}`, { method: 'POST', json: { value } })
  useChat.getState().patch(messageId, (m) => ({ ...m, pendingCalls: m.pendingCalls.filter((c) => c.callId !== callId) }))
}

function historyFor(leaf: string) {
  const { messages } = useChat.getState()
  const out: { role: string; content: string; files?: { type: string; id: string }[] }[] = []
  for (let id: string | null = leaf; id && messages[id]; id = messages[id].parentId) {
    const m: Message = messages[id]
    out.push({ role: m.role, content: messageText(m), ...(m.files.length ? { files: m.files.map(fileAttach) } : {}) })
  }
  return out.reverse()
}
