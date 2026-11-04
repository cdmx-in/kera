// The open chat: a message tree keyed by id plus the active leaf. Components
// subscribe per message (useMessage) so a streamed token re-renders one row.

import { create } from 'zustand'
import { useShallow } from 'zustand/react/shallow'
import type { Message, Part } from '@/api/types'
import { toFileRef } from '@/features/chat/reduce'

export interface ChatStore {
  chatId: string | null
  title: string
  temporary: boolean
  messages: Record<string, Message>
  currentId: string | null
  streamingId: string | null
  reset(chatId: string | null, temporary?: boolean): void
  load(chatId: string, title: string, messages: Record<string, Message>, currentId: string | null): void
  put(m: Message): void
  patch(id: string, fn: (m: Message) => Message): void
  setCurrent(id: string | null): void
  setStreaming(id: string | null): void
  setChatId(id: string): void
  setTitle(t: string): void
}

export const useChat = create<ChatStore>((set) => ({
  chatId: null,
  title: '',
  temporary: false,
  messages: {},
  currentId: null,
  streamingId: null,
  reset: (chatId, temporary = false) => set({ chatId, temporary, title: '', messages: {}, currentId: null, streamingId: null }),
  load: (chatId, title, messages, currentId) => set({ chatId, title, messages, currentId, temporary: false, streamingId: null }),
  put: (m) =>
    set((s) => {
      const messages = { ...s.messages, [m.id]: m }
      const parent = m.parentId ? messages[m.parentId] : undefined
      if (parent && !parent.childrenIds.includes(m.id)) messages[parent.id] = { ...parent, childrenIds: [...parent.childrenIds, m.id] }
      return { messages }
    }),
  patch: (id, fn) =>
    set((s) => {
      const m = s.messages[id]
      return m ? { messages: { ...s.messages, [id]: fn(m) } } : {}
    }),
  setCurrent: (currentId) => set({ currentId }),
  setStreaming: (streamingId) => set({ streamingId }),
  setChatId: (chatId) => set({ chatId }),
  setTitle: (title) => set({ title }),
}))

/** Ids from the root to `leaf`. */
export function branchIds(messages: Record<string, Message>, leaf: string | null): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (let id = leaf; id && messages[id] && !seen.has(id); id = messages[id].parentId) {
    seen.add(id)
    out.push(id)
  }
  return out.reverse()
}

/** Deepest descendant following the most recent child. */
export function leafOf(messages: Record<string, Message>, id: string): string {
  let cur = id
  const seen = new Set<string>()
  while (!seen.has(cur)) {
    seen.add(cur)
    const kids = messages[cur]?.childrenIds ?? []
    const last = kids[kids.length - 1]
    if (!last || !messages[last]) break
    cur = last
  }
  return cur
}

export function siblingsOf(messages: Record<string, Message>, id: string): string[] {
  const m = messages[id]
  if (!m) return []
  if (m.parentId) return messages[m.parentId]?.childrenIds ?? [id]
  return Object.values(messages)
    .filter((x) => !x.parentId)
    .sort((a, b) => a.createdAt - b.createdAt)
    .map((x) => x.id)
}

export const useMessage = (id: string) => useChat((s) => s.messages[id])
export const useBranch = () => useChat(useShallow((s) => branchIds(s.messages, s.currentId)))

/** Converts the server's chat JSON (history.messages map) into store messages. */
export function normalizeHistory(raw: Record<string, Record<string, unknown>>): Record<string, Message> {
  const out: Record<string, Message> = {}
  for (const [key, r] of Object.entries(raw ?? {})) {
    const id = String(r.id ?? key)
    const content = typeof r.content === 'string' ? r.content : ''
    let parts = Array.isArray(r.parts) ? (r.parts as Part[]) : []
    if (!parts.length && content) parts = [{ type: 'text', text: content }]
    const ts = Number(r.createdAt ?? r.timestamp ?? 0)
    const status = (r.status as Message['status']) ?? (r.done === false ? 'streaming' : 'done')
    const err = r.error as Message['error'] | string | undefined | null
    out[id] = {
      id,
      parentId: (r.parentId as string | null) ?? null,
      childrenIds: Array.isArray(r.childrenIds) ? (r.childrenIds as string[]) : [],
      role: (r.role as Message['role']) ?? 'assistant',
      model: (r.model as string) ?? undefined,
      createdAt: ts < 1e12 ? ts * 1000 : ts,
      status,
      parts,
      files: Array.isArray(r.files) ? (r.files as Record<string, unknown>[]).map(toFileRef) : [],
      sources: Array.isArray(r.sources) ? (r.sources as Message['sources']) : [],
      pendingCalls: Array.isArray(r.pendingCalls) ? (r.pendingCalls as Message['pendingCalls']) : [],
      followUps: (r.followUps ?? r.follow_ups) as string[] | undefined,
      usage: r.usage as Message['usage'],
      error: typeof err === 'string' ? (err ? { message: err } : undefined) : (err ?? undefined),
      seq: typeof r.seq === 'number' ? r.seq : undefined,
    }
  }
  // older exports may omit childrenIds: derive them from parentId
  for (const m of Object.values(out)) {
    const p = m.parentId ? out[m.parentId] : undefined
    if (p && !p.childrenIds.includes(m.id)) p.childrenIds.push(m.id)
  }
  return out
}
