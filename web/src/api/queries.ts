// Server state. Chat list freshness comes from invalidation on stream
// `title`/`done` events (no socket).

import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api, setToken } from './client'
import type { ApiKey, ChatSummary, Folder, Me, Model, ModelFeature, ToolServer } from './types'
import { humanizeTool } from '@/features/tools/display'
import { uiConfig } from '@/lib/ui-config'

export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      const me = await api<Me>('/api/v1/auths/')
      if (me.token) setToken(me.token)
      return me
    },
    staleTime: 5 * 60_000,
  })
}

/**
 * Models ordered by the configured modelOrder, then name. Hidden rows are
 * requested and kept (callers filter) so an explicit ?models=<hidden> resolves.
 */
export function useModels() {
  return useQuery({
    queryKey: ['models'],
    queryFn: async () => sortModels(((await api<{ data: Model[] }>('/api/models?include_hidden=true')).data ?? []).map(normalizeModel), uiConfig().modelOrder),
    staleTime: 5 * 60_000,
  })
}

/** Accepts features as ids (`["web_search"]` + `defaultFeatures`) as well as `{id,label,icon,default}` objects. */
export function normalizeModel(m: Model & { defaultFeatures?: string[] }): Model {
  if (!Array.isArray(m.features)) return m
  const defaults = new Set(m.defaultFeatures ?? [])
  const features = (m.features as (ModelFeature | string)[]).map((f) =>
    typeof f === 'string' ? { id: f, label: humanizeTool(f).replace(/^./, (c) => c.toUpperCase()), default: defaults.has(f) } : f,
  )
  return { ...m, features }
}

export function sortModels(models: Model[], order: string[]): Model[] {
  const rank = (id: string) => {
    const i = order.indexOf(id)
    return i === -1 ? Number.MAX_SAFE_INTEGER : i
  }
  return [...models].sort((a, b) => rank(a.id) - rank(b.id) || (a.name || a.id).localeCompare(b.name || b.id))
}

/** The server's page size for the chat list. */
export const CHAT_PAGE = 60

/** The next page to load, or undefined once a page came back short. */
export function nextChatPage(last: ChatSummary[], pages: ChatSummary[][]): number | undefined {
  return last.length < CHAT_PAGE ? undefined : pages.length + 1
}

/** The sidebar list, newest first, one page of CHAT_PAGE at a time. */
export function useChats() {
  return useInfiniteQuery({
    queryKey: ['chats', 'list'],
    initialPageParam: 1,
    queryFn: ({ pageParam }) => api<ChatSummary[]>(`/api/v1/chats/?page=${pageParam}`),
    getNextPageParam: nextChatPage,
  })
}

export function useArchivedChats(enabled: boolean) {
  return useQuery({ queryKey: ['chats', 'archived'], queryFn: () => api<ChatSummary[]>('/api/v1/chats/archived'), enabled })
}

export function usePinnedChats() {
  return useQuery({ queryKey: ['chats', 'pinned'], queryFn: () => api<ChatSummary[]>('/api/v1/chats/pinned') })
}

export function useFolders() {
  return useQuery({ queryKey: ['folders'], queryFn: () => api<Folder[]>('/api/v1/folders/') })
}

export function useTools() {
  return useQuery({ queryKey: ['tools'], queryFn: () => api<ToolServer[]>('/api/v1/tools/') })
}

export function useApiKeys(enabled: boolean) {
  return useQuery({ queryKey: ['api-keys'], queryFn: () => api<ApiKey[]>('/api/v1/auths/api_keys'), enabled })
}

export interface UserSettings {
  ui?: { memory?: boolean; [k: string]: unknown }
}

export function useUserSettings() {
  return useQuery({ queryKey: ['user-settings'], queryFn: () => api<UserSettings | null>('/api/v1/users/user/settings') })
}
