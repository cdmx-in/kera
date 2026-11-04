// Admin API shapes and queries (lazy admin chunk only).
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/api/client'
import type { Perms } from './lib'

export type RoleName = 'admin' | 'user' | 'pending'

export interface AdminUser {
  id: string
  name: string
  email: string
  role: RoleName
  profile_image_url: string
  last_active_at: number
  created_at: number
  groups: { id: string; name: string }[]
  api_key_count: number
}

export interface Group {
  id: string
  name: string
  description: string
  permissions: Perms
  member_count: number
  user_ids?: string[]
}

export interface Grant {
  principal: string
  permission: 'read' | 'write'
}

export interface Setting {
  key: string
  value?: unknown
  default: unknown
  managed: boolean
  secret?: boolean
  set: boolean
}

export interface Connection {
  id: string
  kind: 'ollama' | 'openai' | 'responses'
  base_url: string
  prefix: string
  model_ids: string[]
  enabled: boolean
  headers: Record<string, string>
  options: { forward_user_headers?: Record<string, string>; web_search?: Record<string, string>; [k: string]: unknown }
  sort: number
  managed: boolean
  has_key: boolean
  implicit?: boolean
}

export interface ToolServer {
  id: string
  kind: 'openapi' | 'mcp'
  name: string
  description: string
  url: string
  spec_path: string
  auth: { type: 'none' | 'bearer' | 'header' | 'user_oauth'; header: string; provider: string; has_key: boolean }
  enabled: boolean
  name_filter: string[]
  disabled_functions: string[]
  max_result_chars: number
  auto_enable_when_connected: boolean
  sort: number
  managed: boolean
}

export interface HookServer {
  id: string
  name: string
  url: string
  has_key: boolean
  events: string[]
  priority: number
  scope: 'global' | 'models'
  model_ids: string[]
  config: Record<string, unknown>
  forward_oauth: string[]
  timeout_ms: number
  on_error: 'skip' | 'abort'
  enabled: boolean
  managed: boolean
}

export interface OAuthProvider {
  id: string
  name: string
  login: boolean
  issuer: string
  authorization_endpoint: string
  token_endpoint: string
  revocation_endpoint: string
  userinfo_endpoint: string
  client_id: string
  has_secret: boolean
  scope: string
  pkce: boolean
  send_resource: boolean
  managed: boolean
  redirect_uri: string
}

/** A model row from GET /api/v1/models/ (admins get every row). */
export interface ModelRow {
  id: string
  name: string
  base_model_id: string | null
  connection_id: string | null
  is_active: boolean
  managed: boolean
  meta: { hidden?: boolean; description?: string }
}

export const enc = encodeURIComponent

export function useAdmin<T>(key: string[], path: string, enabled = true) {
  return useQuery({ queryKey: ['admin', ...key], queryFn: () => api<T>(path), enabled })
}

export const useGroups = () => useAdmin<Group[]>(['groups'], '/api/v1/groups/')
export const useToolServers = () => useAdmin<ToolServer[]>(['tool_servers'], '/api/v1/configs/tool_servers')
export const useHookServers = () => useAdmin<HookServer[]>(['hook_servers'], '/api/v1/configs/hook_servers')
export const useProviders = () => useAdmin<OAuthProvider[]>(['oauth_providers'], '/api/v1/configs/oauth_providers')
export const useConnections = () => useAdmin<{ connections: Connection[] }>(['connections'], '/api/v1/configs/connections')

/** Every setting by key. */
export function useSettings() {
  return useQuery({
    queryKey: ['admin', 'settings'],
    queryFn: async () => new Map((await api<Setting[]>('/api/v1/configs/settings')).map((s) => [s.key, s])),
  })
}

/** Saves {key: value} and refreshes the settings cache. */
export function useSaveSettings() {
  const qc = useQueryClient()
  return async (patch: Record<string, unknown>) => {
    const list = await api<Setting[]>('/api/v1/configs/settings', { method: 'POST', json: patch })
    qc.setQueryData(['admin', 'settings'], new Map(list.map((s) => [s.key, s])))
    // Interface settings feed /api/models ordering and the UI config.
    void qc.invalidateQueries({ queryKey: ['models'] })
  }
}
