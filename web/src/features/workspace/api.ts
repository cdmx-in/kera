// Workspace server state: model rows, knowledge collections, prompts, grants.

import { useQuery } from '@tanstack/react-query'
import { api, apiFetch } from '@/api/client'
import type { ModelRow } from './model-form'

export interface Grant {
  principal: string
  permission: 'read' | 'write'
  managed?: boolean
}

export interface IngestStatus {
  files: number
  chunks: number
  pending: number
  processing: number
  completed: number
  failed: number
}

export interface Collection {
  id: string
  user_id: string
  name: string
  description: string
  write_access: boolean
  reindexing?: boolean
  embedding_model?: { id: string; connection: string; model: string; dim: number } | null
  status: IngestStatus
  created_at: number
  updated_at: number
  files?: KnowledgeFile[]
}

export interface KnowledgeFile {
  id: string
  filename: string
  size?: number
  chunks: number
  meta: Record<string, unknown>
  data: { status: 'pending' | 'processing' | 'completed' | 'failed'; file_status?: string; error?: string }
}

export interface Prompt {
  id: string
  user_id: string
  command: string
  title: string
  content: string
  write_access: boolean
  updated_at: number
}

export interface Group {
  id: string
  name: string
}

export const useModelRows = () => useQuery({ queryKey: ['ws', 'model-rows'], queryFn: () => api<ModelRow[]>('/api/v1/models/') })

export const useModelRow = (id: string | undefined) =>
  useQuery({ queryKey: ['ws', 'model-row', id], queryFn: () => api<ModelRow>(`/api/v1/models/model?id=${encodeURIComponent(id!)}`), enabled: !!id })

export const useCollections = () =>
  useQuery({
    queryKey: ['ws', 'knowledge'],
    queryFn: async () => (await api<{ items: Collection[] }>('/api/v1/knowledge/')).items ?? [],
  })

export const useCollection = (id: string | undefined) =>
  useQuery({
    queryKey: ['ws', 'knowledge', id],
    queryFn: () => api<Collection>(`/api/v1/knowledge/${id}`),
    enabled: !!id,
    // Poll while files are being indexed.
    refetchInterval: (q) => (q.state.data && q.state.data.status.pending + q.state.data.status.processing > 0 ? 2000 : false),
  })

export const usePrompts = () => useQuery({ queryKey: ['ws', 'prompts'], queryFn: () => api<Prompt[]>('/api/v1/prompts/') })

/** Groups for the access editor; only admins may list them (others get []). */
export const useGroups = (enabled: boolean) =>
  useQuery({ queryKey: ['ws', 'groups'], queryFn: () => api<Group[]>('/api/v1/groups/').catch(() => [] as Group[]), enabled, staleTime: 60_000 })

/** Uploads one file and returns its id. */
export async function uploadFile(file: File): Promise<string> {
  const fd = new FormData()
  fd.append('file', file, file.name)
  const res = await apiFetch('/api/v1/files/', { method: 'POST', body: fd })
  return ((await res.json()) as { id: string }).id
}
