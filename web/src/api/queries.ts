// Server state. Chat list freshness comes from invalidation on stream
// `title`/`done` events (no socket).

import { useQuery } from '@tanstack/react-query'
import { api, setToken } from './client'
import type { ApiKey, ChatSummary, Folder, Me, Model, ToolServer } from './types'
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

/** Models ordered by the configured modelOrder, then name. Hidden rows stay in the list (callers filter). */
export function useModels() {
  return useQuery({
    queryKey: ['models'],
    queryFn: async () => sortModels((await api<{ data: Model[] }>('/api/models')).data ?? [], uiConfig().modelOrder),
    staleTime: 5 * 60_000,
  })
}

export function sortModels(models: Model[], order: string[]): Model[] {
  const rank = (id: string) => {
    const i = order.indexOf(id)
    return i === -1 ? Number.MAX_SAFE_INTEGER : i
  }
  return [...models].sort((a, b) => rank(a.id) - rank(b.id) || (a.name || a.id).localeCompare(b.name || b.id))
}

export function useChats() {
  return useQuery({ queryKey: ['chats', 'list'], queryFn: () => api<ChatSummary[]>('/api/v1/chats/?page=1') })
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
