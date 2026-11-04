import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { api } from '@/api/client'
import { useMe, useModels, useTools } from '@/api/queries'
import type { FileRef, Message, PendingCall } from '@/api/types'
import { normalizeHistory, useChat } from '@/stores/chat'
import { useComposer } from '@/stores/composer'
import { uiConfig } from '@/lib/ui-config'
import { parseChatParams, withoutOneShot } from '@/app/search-params'
import { Spinner } from '@/ui/misc'
import { Header } from '@/features/header/Header'
import { MessageList } from './messages/MessageList'
import { Composer } from './composer/Composer'
import { EmptyState } from './EmptyState'
import { AskUserCard } from './AskUserCard'
import { ConfirmDialog } from './ConfirmDialog'
import { sendMessage } from './actions'
import { resolveModel } from './model-select'
import { resumeStream } from './stream'

interface ChatJSON {
  id: string
  title?: string
  chat?: { title?: string; history?: { currentId?: string | null; messages?: Record<string, Record<string, unknown>> } }
  history?: { currentId?: string | null; messages?: Record<string, Record<string, unknown>> }
}

export async function loadChat(id: string, path = `/api/v1/chats/${encodeURIComponent(id)}`) {
  const c = await api<ChatJSON>(path)
  const hist = c.chat?.history ?? c.history ?? {}
  const messages = normalizeHistory(hist.messages ?? {})
  let current = hist.currentId && messages[hist.currentId] ? hist.currentId : null
  if (!current) current = Object.values(messages).sort((a, b) => b.createdAt - a.createdAt)[0]?.id ?? null
  useChat.getState().load(c.id ?? id, c.title ?? c.chat?.title ?? '', messages, current)
  return messages
}

function lastAssistant(messages: Record<string, Message>): Message | undefined {
  return Object.values(messages)
    .filter((m) => m.role === 'assistant')
    .sort((a, b) => b.createdAt - a.createdAt)[0]
}

export default function ChatPage() {
  const { chatId } = useParams()
  const [search, setSearch] = useSearchParams()
  const navigate = useNavigate()
  const params = parseChatParams(search)
  const me = useMe()
  const models = useModels()
  const tools = useTools()
  const [state, setState] = useState<'ready' | 'loading' | 'missing'>('ready')
  const hasMessages = useChat((s) => s.currentId !== null)
  const temporary = useChat((s) => s.temporary)
  const model = useComposer((s) => s.model)

  // Load the chat named in the URL, or start fresh on "/".
  useEffect(() => {
    const st = useChat.getState()
    if (!chatId) {
      if (st.chatId && !st.chatId.startsWith('temp:')) st.reset(null, params.temporary)
      else if (!st.currentId && st.temporary !== params.temporary) st.reset(null, params.temporary)
      setState('ready')
      return
    }
    if (st.chatId === chatId) return
    let live = true
    setState('loading')
    loadChat(chatId)
      .then((messages) => {
        if (!live) return
        setState('ready')
        const streaming = Object.values(messages).find((m) => m.status === 'streaming')
        if (streaming) resumeStream(chatId, streaming.id, streaming.seq ?? 0)
        const m = lastAssistant(messages)?.model
        if (m) useComposer.getState().setModel(m)
      })
      .catch(() => live && setState('missing'))
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chatId])

  // Resolve the model once the list arrives (explicit ?models= wins).
  const requested = params.models.join(',')
  useEffect(() => {
    if (!models.data) return
    const cur = useComposer.getState().model
    const pick = resolveModel(models.data, { requested: params.models, chatModel: chatId ? cur : undefined, defaults: uiConfig().defaultModels })
    if (requested || !cur || !models.data.some((m) => m.id === cur)) useComposer.getState().setModel(pick)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [models.data, requested, chatId])

  // Model change -> its default feature pills and tools, plus connected auto-enabled integrations.
  const toolsParam = params.tools.join(',')
  useEffect(() => {
    const m = models.data?.find((x) => x.id === model)
    if (!m) return
    const c = useComposer.getState()
    c.setFeatures(Object.fromEntries((m.features ?? []).map((f) => [f.id, !!f.default])))
    const auto = uiConfig().integrations.flatMap((i) => (i.kind === 'oauthToolServer' && i.autoEnable && tools.data?.some((t) => t.id === i.toolServerId && t.authenticated) ? [i.toolServerId] : []))
    c.setToolIds([...(m.toolIds ?? []), ...auto, ...params.tools])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model, models.data, tools.data, toolsParam])

  const onSend = useCallback(
    async (text: string, files: FileRef[]) => {
      const r = await sendMessage(text, files)
      if (r.ok && r.chatId && r.chatId !== chatId && !useChat.getState().temporary) navigate(`/c/${r.chatId}`)
      return r.ok
    },
    [chatId, navigate],
  )

  // ?q= sends once: signed in, model resolved, nothing sent yet.
  const sentQ = useRef(false)
  useEffect(() => {
    if (sentQ.current || !params.q || !me.data || !model || !models.data?.some((m) => m.id === model) || chatId) return
    sentQ.current = true
    const q = params.q
    setSearch(withoutOneShot(search.toString() ? `?${search}` : ''), { replace: true })
    void onSend(q, [])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params.q, me.data, model, models.data, chatId])

  // ?tools= is one-shot too (OAuth return); drop it once applied.
  useEffect(() => {
    if (!params.q && params.tools.length && tools.data) setSearch(withoutOneShot(`?${search}`), { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [toolsParam, tools.data])

  const pending = useChat((s) => {
    const m = s.currentId ? s.messages[s.currentId] : undefined
    return m?.role === 'assistant' && m.pendingCalls.length ? m : undefined
  })
  const ask = pending?.pendingCalls.find((c): c is Extract<PendingCall, { kind: 'ask_user' }> => c.kind === 'ask_user')
  const confirm = pending?.pendingCalls.find((c): c is Extract<PendingCall, { kind: 'confirm' }> => c.kind === 'confirm')
  const modelName = useCallback((id?: string) => models.data?.find((m) => m.id === id)?.name ?? id ?? '', [models.data])

  return (
    <div className="flex h-full min-w-0 flex-1 flex-col">
      <Header />
      {state === 'loading' ? (
        <div className="grid flex-1 place-items-center text-muted">
          <Spinner size={20} />
        </div>
      ) : state === 'missing' ? (
        <div className="grid flex-1 place-items-center px-4 text-center text-muted">
          <p>
            This chat does not exist or you do not have access to it.{' '}
            <button type="button" className="text-link underline" onClick={() => navigate('/')}>
              Start a new chat
            </button>
          </p>
        </div>
      ) : hasMessages ? (
        <MessageList modelName={modelName} />
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <EmptyState onPick={(t) => void onSend(t, [])} />
        </div>
      )}
      <div className="mx-auto w-full max-w-3xl px-3 pb-3 sm:px-4 sm:pb-4">
        {pending && ask && <AskUserCard key={ask.callId} messageId={pending.id} call={ask} />}
        <Composer onSend={onSend} disabled={state !== 'ready'} />
        <p className="mt-1.5 text-center text-xs text-muted">{temporary ? 'Temporary chat: it will not be saved.' : 'AI can make mistakes. Check important information.'}</p>
      </div>
      {pending && confirm && <ConfirmDialog key={confirm.callId} messageId={pending.id} call={confirm} />}
    </div>
  )
}
