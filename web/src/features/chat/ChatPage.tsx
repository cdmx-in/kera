import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { api } from '@/api/client'
import { useMe, useModels, useTools } from '@/api/queries'
import type { FileRef, Message, PendingCall } from '@/api/types'
import { normalizeHistory, useChat } from '@/stores/chat'
import { useComposer } from '@/stores/composer'
import { uiConfig } from '@/lib/ui-config'
import { Aurora } from '@/features/brand/Splash'
import { parseChatParams, withoutOneShot } from '@/app/search-params'
import { toolKey } from '@/features/tools/ids'
import { toast } from '@/stores/toast'
import { LoadFailed, Spinner, isMissing } from '@/ui/misc'
import { Header } from '@/features/header/Header'
import { MessageList } from './messages/MessageList'
import { Composer } from './composer/Composer'
import { EmptyState } from './EmptyState'
import { AskUserCard } from './AskUserCard'
import { ConfirmDialog } from './ConfirmDialog'
import { sendMessage } from './actions'
import { nextModel } from './model-select'
import { resumeStream } from './stream'

interface ChatJSON {
  id: string
  title?: string
  mode?: string
  chat?: { title?: string; history?: { currentId?: string | null; messages?: Record<string, Record<string, unknown>> } }
  history?: { currentId?: string | null; messages?: Record<string, Record<string, unknown>> }
}

export async function loadChat(id: string, path = `/api/v1/chats/${encodeURIComponent(id)}`) {
  const c = await api<ChatJSON>(path)
  const hist = c.chat?.history ?? c.history ?? {}
  const messages = normalizeHistory(hist.messages ?? {})
  let current = hist.currentId && messages[hist.currentId] ? hist.currentId : null
  if (!current) current = Object.values(messages).sort((a, b) => b.createdAt - a.createdAt)[0]?.id ?? null
  useChat.getState().load(c.id ?? id, c.title ?? c.chat?.title ?? '', messages, current, c.mode === 'cowork' ? 'cowork' : undefined)
  return messages
}

function lastAssistant(messages: Record<string, Message>): Message | undefined {
  return Object.values(messages)
    .filter((m) => m.role === 'assistant')
    .sort((a, b) => b.createdAt - a.createdAt)[0]
}

/**
 * Loads the chat (or task) named in the URL, re-attaches a running reply and adopts its model.
 * A chat stored in the other mode moves to its own page: the chat page never renders a task.
 */
export function useOpenChat(chatId: string | undefined, mode: 'chat' | 'cowork' = 'chat') {
  const navigate = useNavigate()
  const [state, setState] = useState<'ready' | 'loading' | 'missing' | 'failed'>('ready')
  useEffect(() => {
    const moveIfOther = () => {
      const stored = useChat.getState().mode ?? 'chat'
      if (chatId && stored !== mode) navigate(stored === 'cowork' ? `/cowork/${chatId}` : `/c/${chatId}`, { replace: true })
    }
    if (!chatId) return setState('ready')
    if (useChat.getState().chatId === chatId) return moveIfOther()
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
        moveIfOther()
      })
      .catch((e) => live && setState(isMissing(e) ? 'missing' : 'failed'))
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chatId])
  return state
}

/** Resolves the composer model (explicit ?models= wins) and applies the model's default pills and tools. */
export function useModelSetup(requestedModels: string[], chatId: string | undefined, toolsParamList: string[]) {
  const models = useModels()
  const tools = useTools()
  const model = useComposer((s) => s.model)

  // Resolve the model once the list arrives (explicit ?models= wins).
  const requested = requestedModels.join(',')
  useEffect(() => {
    if (!models.data) return
    const c = useComposer.getState()
    const next = nextModel(models.data, { current: c.model, auto: c.modelAuto, requested: requestedModels, inChat: !!chatId, defaults: uiConfig().defaultModels })
    if (next) c.setModel(next.id, next.auto)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [models.data, requested, chatId])

  // Model change -> its default feature pills and tools, plus connected auto-enabled integrations.
  // ?tools= is removed from the URL once applied, so remember it for this page view.
  const toolsParam = toolsParamList.join(',')
  const urlTools = useRef<string[]>([])
  if (toolsParamList.length) urlTools.current = toolsParamList
  // Mounting on a chat the store already holds (New task -> its task page) keeps the pills and tools
  // chosen for it; defaults apply again once the model changes.
  const keepFor = useRef(chatId && useChat.getState().chatId === chatId ? useComposer.getState().model : '')
  useEffect(() => {
    if (keepFor.current && keepFor.current === model) return
    keepFor.current = ''
    const m = models.data?.find((x) => x.id === model)
    if (!m) return
    const c = useComposer.getState()
    c.setFeatures(Object.fromEntries((m.features ?? []).map((f) => [f.id, !!f.default])))
    const connected = (id: string) => tools.data?.some((t) => t.id === id && t.authenticated)
    const auto = [
      ...uiConfig().integrations.flatMap((i) => (i.kind === 'oauthToolServer' && i.autoEnable && connected(i.toolServerId) ? [i.toolServerId] : [])),
      ...(tools.data ?? []).filter((t) => t.auto_enable && t.authenticated).map((t) => t.id),
    ]
    c.setToolIds([...(m.toolIds ?? []).map(toolKey), ...auto, ...urlTools.current])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model, models.data, tools.data, toolsParam])
}

/** The open reply's pending ask_user / confirm calls. */
export function usePending() {
  const pending = useChat((s) => {
    const m = s.currentId ? s.messages[s.currentId] : undefined
    return m?.role === 'assistant' && m.pendingCalls.length ? m : undefined
  })
  const ask = pending?.pendingCalls.find((c): c is Extract<PendingCall, { kind: 'ask_user' }> => c.kind === 'ask_user')
  const confirm = pending?.pendingCalls.find((c): c is Extract<PendingCall, { kind: 'confirm' }> => c.kind === 'confirm')
  return { pending, ask, confirm }
}

export default function ChatPage() {
  const { chatId } = useParams()
  const [search, setSearch] = useSearchParams()
  const navigate = useNavigate()
  const params = parseChatParams(search)
  const me = useMe()
  const models = useModels()
  const tools = useTools()
  const state = useOpenChat(chatId)
  const hasMessages = useChat((s) => s.currentId !== null)
  const temporary = useChat((s) => s.temporary)
  const model = useComposer((s) => s.model)

  // "/" starts fresh (also after a task, so its mode never leaks into a new chat).
  useEffect(() => {
    if (chatId) return
    const st = useChat.getState()
    if ((st.chatId && !st.chatId.startsWith('temp:')) || st.mode) st.reset(null, params.temporary)
    else if (!st.currentId && st.temporary !== params.temporary) st.reset(null, params.temporary)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chatId])

  useModelSetup(params.models, chatId, params.tools)
  const toolsParam = params.tools.join(',')

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

  // A failed integration connect comes back as ?oauth_error=<code>.
  const oauthError = search.get('oauth_error')
  useEffect(() => {
    if (!oauthError) return
    toast(oauthError === 'provider_denied' ? 'Connection cancelled' : `Could not connect the integration (${oauthError.replace(/_/g, ' ')})`, 'error')
    setSearch(withoutOneShot(`?${search}`), { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [oauthError])

  // ?tools= is one-shot too (OAuth return); drop it once applied.
  useEffect(() => {
    if (!params.q && params.tools.length && tools.data) setSearch(withoutOneShot(`?${search}`), { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [toolsParam, tools.data])

  const { pending, ask, confirm } = usePending()
  const modelName = useCallback((id?: string) => models.data?.find((m) => m.id === id)?.name ?? id ?? '', [models.data])

  return (
    <div className="relative isolate flex h-full min-w-0 flex-1 flex-col">
      {uiConfig().brand.background === 'aurora' && <Aurora />}
      <Header />
      {state === 'loading' ? (
        <div className="grid flex-1 place-items-center text-muted">
          <Spinner size={20} />
        </div>
      ) : state === 'failed' ? (
        <LoadFailed what="chat" />
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
