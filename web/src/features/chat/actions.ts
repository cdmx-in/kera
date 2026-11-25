// User actions on the open chat: send, regenerate, edit, branch switch.
import { api } from '@/api/client'
import type { FileRef, Message } from '@/api/types'
import { useChat, leafOf, siblingsOf } from '@/stores/chat'
import { useComposer } from '@/stores/composer'
import { toast } from '@/stores/toast'
import { queryClient } from '@/app/query'
import { newId } from '@/lib/format'
import { startCompletion } from './stream'

function userMessage(text: string, files: FileRef[], parentId: string | null): Message {
  return { id: newId(), parentId, childrenIds: [], role: 'user', createdAt: Date.now(), status: 'done', parts: [{ type: 'text', text }], files, sources: [], pendingCalls: [] }
}

async function ensureChat(model: string, title = 'New Chat'): Promise<string | null> {
  const st = useChat.getState()
  if (st.temporary || st.chatId) return st.chatId
  const chat = await api<{ id: string }>('/api/v1/chats/new', { method: 'POST', json: { chat: { title, models: [model], history: { messages: {}, currentId: null }, ...(st.mode && { mode: st.mode }) } } })
  useChat.getState().setChatId(chat.id)
  useChat.getState().setTitle(title)
  void queryClient.invalidateQueries({ queryKey: ['chats'] })
  return chat.id
}

function run(user: Message, chatId: string | null) {
  const c = useComposer.getState()
  startCompletion({ chatId, temporary: useChat.getState().temporary, model: c.model, user, assistantId: newId(), features: c.features, toolIds: c.toolIds })
}

/** Sends a new user turn after the active leaf. New chats get their id here (temporary chats have none) and the given title instead of the one the assistant writes. */
export async function sendMessage(text: string, files: FileRef[] = [], title?: string): Promise<{ ok: boolean; chatId: string | null }> {
  const model = useComposer.getState().model
  if (!model) {
    toast('Select a model first', 'error')
    return { ok: false, chatId: null }
  }
  let chatId: string | null
  try {
    chatId = await ensureChat(model, title)
  } catch (e) {
    toast(`Could not start the chat: ${(e as Error).message}`, 'error')
    return { ok: false, chatId: null }
  }
  const st = useChat.getState()
  const user = userMessage(text, files, st.currentId)
  st.put(user)
  st.setCurrent(user.id)
  run(user, chatId)
  return { ok: true, chatId }
}

/** New assistant sibling for the reply to `assistantId`'s parent. */
export function regenerate(assistantId: string) {
  const st = useChat.getState()
  const parent = st.messages[assistantId]?.parentId
  const user = parent ? st.messages[parent] : undefined
  if (!user) return
  const model = st.messages[assistantId]?.model
  if (model) useComposer.getState().setModel(model)
  run(user, st.chatId)
}

/** Edit = a new user sibling (new branch) followed by a fresh reply. */
export function editMessage(userId: string, text: string) {
  const st = useChat.getState()
  const old = st.messages[userId]
  if (!old) return
  const user = userMessage(text, old.files, old.parentId)
  st.put(user)
  st.setCurrent(user.id)
  run(user, st.chatId)
}

export function switchBranch(id: string, dir: -1 | 1) {
  const st = useChat.getState()
  const sibs = siblingsOf(st.messages, id)
  const next = sibs[sibs.indexOf(id) + dir]
  if (!next) return
  const leaf = leafOf(st.messages, next)
  st.setCurrent(leaf)
  // remember the visible branch so a reload (and the next reply) continues from it
  if (st.chatId && !st.temporary && !st.chatId.startsWith('temp:')) {
    void api(`/api/v1/chats/${encodeURIComponent(st.chatId)}`, { method: 'POST', json: { chat: { history: { currentId: leaf } } } }).catch(() => undefined)
  }
}
