// Attachments: POST /api/v1/files/ then poll GET /api/v1/files/{id} until the
// server finishes extracting (completed) or gives up (failed).
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, apiFetch } from '@/api/client'
import type { FileRef, UploadedFile } from '@/api/types'
import { newId } from '@/lib/format'

export interface Attachment {
  key: string
  name: string
  size: number
  contentType: string
  status: 'uploading' | 'processing' | 'completed' | 'failed'
  id?: string
  error?: string
  preview?: string
}

export function useUploads() {
  const [items, setItems] = useState<Attachment[]>([])
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  const update = useCallback((key: string, patch: Partial<Attachment>) => {
    if (alive.current) setItems((xs) => xs.map((x) => (x.key === key ? { ...x, ...patch } : x)))
  }, [])

  const add = useCallback(
    (files: File[]) => {
      for (const f of files) {
        const key = newId()
        const preview = f.type.startsWith('image/') ? URL.createObjectURL(f) : undefined
        setItems((xs) => [...xs, { key, name: f.name, size: f.size, contentType: f.type, status: 'uploading', preview }])
        void (async () => {
          try {
            const form = new FormData()
            form.append('file', f)
            const res = await apiFetch('/api/v1/files/', { method: 'POST', body: form })
            const up = (await res.json()) as UploadedFile
            update(key, { id: up.id, status: 'processing' })
            for (let i = 0; alive.current; i++) {
              const st = i === 0 ? up : await api<UploadedFile>(`/api/v1/files/${encodeURIComponent(up.id)}`)
              const s = st.data?.status ?? 'completed'
              if (s === 'completed') return update(key, { status: 'completed' })
              if (s === 'failed') return update(key, { status: 'failed', error: st.data?.error || 'Processing failed' })
              await new Promise((r) => setTimeout(r, Math.min(3000, 500 + i * 250)))
            }
          } catch (e) {
            update(key, { status: 'failed', error: (e as Error).message })
          }
        })()
      }
    },
    [update],
  )

  const remove = useCallback((key: string) => setItems((xs) => xs.filter((x) => x.key !== key)), [])
  const clear = useCallback(() => setItems([]), [])
  const busy = items.some((x) => x.status === 'uploading' || x.status === 'processing')
  const ready: FileRef[] = items
    .filter((x) => x.status === 'completed' && x.id)
    .map((x) => ({ id: x.id!, name: x.name, size: x.size, contentType: x.contentType, type: x.contentType.startsWith('image/') ? 'image' : 'file' }))
  return { items, add, remove, clear, busy, ready }
}
