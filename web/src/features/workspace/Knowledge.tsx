import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Plus, RefreshCw, Trash2, Upload } from 'lucide-react'
import { api } from '@/api/client'
import { cx, formatBytes } from '@/lib/format'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Spinner } from '@/ui/misc'
import { toast } from '@/stores/toast'
import { uploadFile, useCollection, useCollections, type Collection, type KnowledgeFile } from './api'
import { ingestSummary } from './model-form'
import { AccessEditor, Badge, Field, Section, btn, btnPrimary, errorText, input } from './ui'

export function KnowledgeList({ canCreate }: { canCreate: boolean }) {
  const { data: colls, isLoading } = useCollections()
  const [creating, setCreating] = useState(false)
  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <p className="flex-1 text-sm text-muted">Document collections assistants can search or read in full.</p>
        {canCreate && (
          <button type="button" className={btnPrimary} onClick={() => setCreating(true)} data-testid="new-knowledge">
            <Plus size={15} /> New collection
          </button>
        )}
      </div>
      {isLoading && <Spinner />}
      {colls && colls.length === 0 && <p className="text-sm text-muted">No collections yet.</p>}
      <ul className="grid gap-3 sm:grid-cols-2">
        {colls?.map((c) => (
          <li key={c.id}>
            <Link to={`/workspace/knowledge/${c.id}`} className="block h-full rounded-xl border border-line p-4 hover:bg-surface-2" data-testid="knowledge-row">
              <span className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate font-medium">{c.name}</span>
                <StatusBadge c={c} />
              </span>
              <span className="mt-1 line-clamp-2 block text-sm text-muted">{c.description || 'No description'}</span>
              <span className="mt-2 block text-xs text-muted">
                {ingestSummary(c.status)}
                {c.embedding_model ? ` · ${c.embedding_model.model}` : ''}
              </span>
            </Link>
          </li>
        ))}
      </ul>
      <CreateDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  )
}

function StatusBadge({ c }: { c: Collection }) {
  if (c.status.pending + c.status.processing > 0 || c.reindexing) return <Badge tone="busy">indexing</Badge>
  if (c.status.failed) return <Badge tone="bad">{c.status.failed} failed</Badge>
  return null
}

function CreateDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const navigate = useNavigate()
  const qc = useQueryClient()
  const create = async () => {
    try {
      const c = await api<Collection>('/api/v1/knowledge/create', { method: 'POST', json: { name, description: desc } })
      await qc.invalidateQueries({ queryKey: ['ws', 'knowledge'] })
      onClose()
      navigate(`/workspace/knowledge/${c.id}`)
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }
  return (
    <Dialog open={open} onClose={onClose} label="New collection" className="w-[28rem]">
      <DialogHeader title="New collection" onClose={onClose} />
      <form
        className="space-y-3 p-4"
        onSubmit={(e) => {
          e.preventDefault()
          void create()
        }}
      >
        <Field label="Name">
          <input className={input} value={name} onChange={(e) => setName(e.target.value)} autoFocus required data-testid="knowledge-name" />
        </Field>
        <Field label="Description" hint="Assistants see this when choosing what to search.">
          <textarea className={input} value={desc} onChange={(e) => setDesc(e.target.value)} />
        </Field>
        <div className="flex justify-end">
          <button type="submit" className={btnPrimary} disabled={!name.trim()}>
            Create
          </button>
        </div>
      </form>
    </Dialog>
  )
}

const statusTone = { completed: 'ok', pending: 'busy', processing: 'busy', failed: 'bad' } as const
const statusLabel = { completed: 'indexed', pending: 'waiting', processing: 'indexing', failed: 'failed' }

export function KnowledgeDetail() {
  const { id } = useParams()
  const { data: c, error, isLoading } = useCollection(id)
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [uploading, setUploading] = useState(0)
  const [model, setModel] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (c) {
      setName(c.name)
      setDesc(c.description)
    }
  }, [c?.id, c?.name, c?.description])
  if (isLoading) return <Spinner />
  if (error || !c) return <p className="text-red-600">{error ? errorText(error) : 'Not found'}</p>
  const refresh = () => qc.invalidateQueries({ queryKey: ['ws', 'knowledge'] })
  const call = async (path: string, init: RequestInit & { json?: unknown }, done?: string) => {
    try {
      await api(`/api/v1/knowledge/${c.id}${path}`, init)
      await refresh()
      if (done) toast(done)
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }
  const upload = async (files: FileList | null) => {
    if (!files?.length) return
    setUploading(files.length)
    for (const file of Array.from(files)) {
      try {
        const fid = await uploadFile(file)
        await api(`/api/v1/knowledge/${c.id}/file/add`, { method: 'POST', json: { file_id: fid } })
      } catch (e) {
        toast(`${file.name}: ${errorText(e)}`, 'error')
      }
      setUploading((n) => n - 1)
      await refresh()
    }
    if (fileInput.current) fileInput.current.value = ''
  }
  const remove = async () => {
    if (!confirm(`Delete the collection ${c.name}? Its files stay in their owners' file lists.`)) return
    await call('/delete', { method: 'DELETE' })
    navigate('/workspace/knowledge')
  }
  const w = c.write_access
  return (
    <div className="space-y-4" data-testid="knowledge-detail">
      <div className="flex items-center gap-2">
        <Link to="/workspace/knowledge" className={btn}>
          <ArrowLeft size={15} /> Knowledge
        </Link>
        <h2 className="min-w-0 flex-1 truncate text-lg font-semibold">{c.name}</h2>
        {w && (
          <IconButton label="Delete collection" onClick={remove}>
            <Trash2 size={16} />
          </IconButton>
        )}
      </div>
      <Section title="About">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Name">
            <input className={input} value={name} disabled={!w} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label="Description">
            <input className={input} value={desc} disabled={!w} onChange={(e) => setDesc(e.target.value)} />
          </Field>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
          <span>{ingestSummary(c.status)}</span>
          <span>· {c.status.chunks} chunks</span>
          {c.embedding_model && (
            <span>
              · embeddings {c.embedding_model.model} ({c.embedding_model.dim}-d)
            </span>
          )}
          {c.reindexing && <Badge tone="busy">moving to a new embedding model</Badge>}
          <span className="flex-1" />
          {w && (
            <button type="button" className={btnPrimary} disabled={name === c.name && desc === c.description} onClick={() => call('/update', { method: 'POST', json: { name, description: desc } }, 'Saved')}>
              Save
            </button>
          )}
        </div>
      </Section>
      <Section
        title="Files"
        aside={
          w && (
            <>
              <input ref={fileInput} type="file" multiple className="sr-only" onChange={(e) => void upload(e.target.files)} data-testid="knowledge-upload" />
              <button type="button" className={btn} onClick={() => fileInput.current?.click()} disabled={uploading > 0}>
                {uploading > 0 ? <Spinner /> : <Upload size={14} />} Upload
              </button>
            </>
          )
        }
      >
        {(c.files ?? []).length === 0 && <p className="text-sm text-muted">No files yet.</p>}
        <ul className="divide-y divide-line rounded-lg border border-line">
          {(c.files ?? []).map((f) => (
            <FileRow key={f.id} f={f} canEdit={w} onRemove={() => call('/file/remove', { method: 'POST', json: { file_id: f.id } })} onRetry={() => call('/file/update', { method: 'POST', json: { file_id: f.id } })} />
          ))}
        </ul>
      </Section>
      {w && (
        <Section title="Index">
          <p className="text-sm text-muted">Re-embed every file, for example after changing the chunk settings. Name another embedding model to move the collection to it; searches keep using the current model until every file is done.</p>
          <div className="flex flex-wrap gap-2">
            <input className={cx(input, 'max-w-xs py-1.5')} placeholder={c.embedding_model?.model ?? 'embedding model (optional)'} value={model} onChange={(e) => setModel(e.target.value)} aria-label="Embedding model" />
            <button type="button" className={btn} onClick={() => call(`/reindex${model.trim() ? `?embedding_model=${encodeURIComponent(model.trim())}` : ''}`, { method: 'POST' }, 'Reindexing started')} data-testid="reindex">
              <RefreshCw size={14} /> Reindex
            </button>
          </div>
        </Section>
      )}
      <Section title="Access">
        <AccessEditor url={`/api/v1/knowledge/${c.id}/grants`} canEdit={w} />
      </Section>
    </div>
  )
}

function FileRow({ f, canEdit, onRemove, onRetry }: { f: KnowledgeFile; canEdit: boolean; onRemove: () => void; onRetry: () => void }) {
  const st = f.data.status
  return (
    <li className="flex items-center gap-3 px-3 py-2 text-sm" data-testid="knowledge-file">
      <span className="min-w-0 flex-1">
        <a href={`/api/v1/files/${f.id}/content`} target="_blank" rel="noreferrer" className="block truncate hover:underline">
          {f.filename}
        </a>
        {f.data.error && <span className="block truncate text-xs text-red-600">{f.data.error}</span>}
      </span>
      <span className="hidden text-xs text-muted sm:inline">{formatBytes(f.size)}</span>
      {st === 'completed' && <span className="text-xs text-muted">{f.chunks} chunks</span>}
      <Badge tone={statusTone[st]} title={f.data.error}>
        {st === 'pending' && f.data.file_status !== 'completed' ? 'extracting' : statusLabel[st]}
      </Badge>
      {canEdit && st === 'failed' && (
        <IconButton label="Retry" onClick={onRetry}>
          <RefreshCw size={15} />
        </IconButton>
      )}
      {canEdit && (
        <IconButton label="Remove from collection" onClick={onRemove}>
          <Trash2 size={15} />
        </IconButton>
      )}
    </li>
  )
}
