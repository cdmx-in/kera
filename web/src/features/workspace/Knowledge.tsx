import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Binary, FileText, Files, Library, Plus, RefreshCw, SearchX, Settings2, Trash2, Upload, UserCog } from 'lucide-react'
import { api } from '@/api/client'
import { useMe } from '@/api/queries'
import { cx, formatBytes } from '@/lib/format'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Spinner } from '@/ui/misc'
import { toast } from '@/stores/toast'
import { uploadFile, useCollection, useCollections, type Collection, type KnowledgeFile } from './api'
import { ingestSummary } from './model-form'
import { statusChips } from './lib'
import { serverFieldError, validateCollection } from './validate'
import { Confirm } from '@/features/admin/ui'
import { AccessEditor, Badge, Chip, EditorHeader, Empty, Field, GridSkeleton, IconTile, SearchBox, Section, TabBar, Toolbar, btn, btnPrimary, cardLink, errorText, grid, input, useTab } from './ui'

export function KnowledgeList({ canCreate }: { canCreate: boolean }) {
  const { data: colls, isLoading, error } = useCollections()
  const [creating, setCreating] = useState(false)
  const [q, setQ] = useState('')
  const words = q.toLowerCase().split(/\s+/).filter(Boolean)
  const shown = (colls ?? []).filter((c) => words.every((w) => `${c.name} ${c.description}`.toLowerCase().includes(w)))
  const newButton = canCreate && (
    <button type="button" className={btnPrimary} onClick={() => setCreating(true)} data-testid="new-knowledge">
      <Plus size={15} /> New collection
    </button>
  )
  return (
    <div className="space-y-6">
      <Toolbar action={newButton}>
        <SearchBox value={q} onChange={setQ} label="Search collections" />
        <p className="text-sm text-muted">Document collections assistants can search or read in full.</p>
      </Toolbar>
      {isLoading && <GridSkeleton className={grid} />}
      {error && <p className="rounded-xl border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300">{errorText(error)}</p>}
      {colls && colls.length === 0 && (
        <Empty icon={Library} title="No collections yet" action={newButton}>
          Upload documents into a collection, then attach it to an assistant.
        </Empty>
      )}
      {colls && colls.length > 0 && shown.length === 0 && (
        <Empty icon={SearchX} title="No collections match">
          Try another search.
        </Empty>
      )}
      {shown.length > 0 && (
        <ul className={grid}>
          {shown.map((c) => (
            <li key={c.id}>
              <Link to={`/workspace/knowledge/${c.id}`} className={cardLink} data-testid="knowledge-row">
                <span className="flex items-start gap-3">
                  <IconTile icon={Library} size="lg" />
                  <span className="min-w-0 flex-1 pt-0.5">
                    <span className="block truncate font-semibold tracking-tight group-hover:text-accent dark:group-hover:text-accent-bright">{c.name}</span>
                    <span className="block text-xs text-muted">
                      {c.status.files} file{c.status.files === 1 ? '' : 's'} · {c.status.chunks.toLocaleString()} chunks
                    </span>
                  </span>
                </span>
                <span className={cx('mt-3 line-clamp-2 text-sm leading-relaxed', c.description ? 'text-fg/80' : 'italic text-muted/80')}>{c.description || 'No description'}</span>
                <span className="mt-auto flex flex-wrap items-center gap-1.5 pt-4">
                  {statusChips(c.status).map((s) => (
                    <Chip key={s.label} tone={s.tone}>
                      {s.label}
                    </Chip>
                  ))}
                  {c.reindexing && <Chip tone="amber">re-embedding</Chip>}
                  {c.embedding_model && (
                    <Chip icon={Binary} title={`Embeddings: ${c.embedding_model.model} (${c.embedding_model.dim}-d)`}>
                      {c.embedding_model.model}
                    </Chip>
                  )}
                  {!c.write_access && <Chip>read only</Chip>}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      <CreateDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  )
}

function CreateDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [touched, setTouched] = useState(false)
  const [serverErr, setServerErr] = useState('')
  const navigate = useNavigate()
  const qc = useQueryClient()
  const err = serverErr || (touched ? validateCollection({ name }).name : undefined)
  const create = async () => {
    setServerErr('')
    try {
      const c = await api<Collection>('/api/v1/knowledge/create', { method: 'POST', json: { name, description: desc } })
      await qc.invalidateQueries({ queryKey: ['ws', 'knowledge'] })
      onClose()
      navigate(`/workspace/knowledge/${c.id}`)
    } catch (e) {
      const fe = serverFieldError(e)
      if (fe?.[0] === 'name') setServerErr(fe[1])
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
        <Field label="Name" error={err}>
          <input className={input} value={name} aria-invalid={!!err} onChange={(e) => (setName(e.target.value), setTouched(true), setServerErr(''))} onBlur={() => setTouched(true)} autoFocus required data-testid="knowledge-name" />
        </Field>
        <Field label="Description" hint="Assistants see this when choosing what to search.">
          <textarea className={cx(input, 'min-h-20')} value={desc} onChange={(e) => setDesc(e.target.value)} />
        </Field>
        <div className="flex justify-end">
          <button type="submit" className={btnPrimary} disabled={!!validateCollection({ name }).name}>
            Create
          </button>
        </div>
      </form>
    </Dialog>
  )
}

const statusTone = { completed: 'green', pending: 'amber', processing: 'amber', failed: 'red' } as const
const statusLabel = { completed: 'indexed', pending: 'waiting', processing: 'indexing', failed: 'failed' }

const TABS = ['files', 'settings', 'access'] as const

export function KnowledgeDetail() {
  const [confirmDelete, setConfirmDelete] = useState(false)
  const { id } = useParams()
  const { data: c, error, isLoading } = useCollection(id)
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [uploading, setUploading] = useState(0)
  const [model, setModel] = useState('')
  const [q, setQ] = useState('')
  const [tab, setTab] = useTab(TABS, 'files')
  const fileInput = useRef<HTMLInputElement>(null)
  const { data: me } = useMe()
  useEffect(() => {
    if (c) {
      setName(c.name)
      setDesc(c.description)
    }
  }, [c?.id, c?.name, c?.description])
  if (isLoading)
    return (
      <div className="grid place-items-center p-10 text-muted">
        <Spinner size={20} />
      </div>
    )
  if (error || !c) return <p className="rounded-xl border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300">{error ? errorText(error) : 'Not found'}</p>
  const refresh = () => qc.invalidateQueries({ queryKey: ['ws', 'knowledge'] })
  const call = async (path: string, init: RequestInit & { json?: unknown }, done?: string) => {
    try {
      await api(`/api/v1/knowledge/${c.id}${path}`, init)
      await refresh()
      if (done) toast(done)
      return true
    } catch (e) {
      toast(errorText(e), 'error')
      return false
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
    if (await call('/delete', { method: 'DELETE' })) navigate('/workspace/knowledge')
  }
  const w = c.write_access
  const nameErr = validateCollection({ name }).name
  const files = (c.files ?? []).filter((f) => !q || f.filename.toLowerCase().includes(q.toLowerCase()))
  const tabs = [
    { key: 'files' as const, label: 'Files', icon: Files, badge: <span className="rounded-full bg-surface-2 px-1.5 text-[11px] tabular-nums text-muted">{c.status.files}</span> },
    { key: 'settings' as const, label: 'Settings', icon: Settings2, error: !!nameErr },
    { key: 'access' as const, label: 'Access', icon: UserCog },
  ]
  return (
    <div className="space-y-5" data-testid="knowledge-detail">
      <EditorHeader
        back={
          <Link to="/workspace/knowledge" className="grid size-9 shrink-0 place-items-center rounded-lg border border-line bg-surface text-muted hover:bg-surface-2 hover:text-fg" aria-label="Back to knowledge" title="Back to knowledge">
            <ArrowLeft size={16} />
          </Link>
        }
        icon={<IconTile icon={Library} />}
        title={<span className="truncate">{c.name}</span>}
        subtitle={ingestSummary(c.status)}
        actions={
          w && (
            <>
              <input ref={fileInput} type="file" multiple className="sr-only" onChange={(e) => void upload(e.target.files)} data-testid="knowledge-upload" />
              <button type="button" className={btnPrimary} onClick={() => fileInput.current?.click()} disabled={uploading > 0}>
                {uploading > 0 ? <Spinner /> : <Upload size={14} />} <span className="hidden sm:inline">Upload</span>
              </button>
              <Confirm open={confirmDelete} title="Delete collection?" body={`${c.name} is deleted. Its files stay in their owners' file lists.`} onConfirm={remove} onClose={() => setConfirmDelete(false)} />
              <IconButton label="Delete collection" onClick={() => setConfirmDelete(true)}>
                <Trash2 size={16} />
              </IconButton>
            </>
          )
        }
      >
        <TabBar tabs={tabs} value={tab} onChange={setTab} label="Collection" />
      </EditorHeader>
      {tab === 'files' && (
        <Section
          title="Files"
          icon={Files}
          hint={
            <span className="inline-flex flex-wrap gap-1.5 pt-0.5">
              {statusChips(c.status).map((s) => (
                <Chip key={s.label} tone={s.tone}>
                  {s.label}
                </Chip>
              ))}
              {c.reindexing && <Chip tone="amber">moving to a new embedding model</Chip>}
            </span>
          }
          aside={(c.files ?? []).length > 5 && <SearchBox value={q} onChange={setQ} label="Search files" />}
        >
          {(c.files ?? []).length === 0 && <p className="text-sm text-muted">No files yet.{w && ' Upload documents to index them.'}</p>}
          {files.length > 0 && (
            <ul className="divide-y divide-line rounded-xl border border-line">
              {files.map((f) => (
                <FileRow key={f.id} f={f} canEdit={w} onRemove={() => call('/file/remove', { method: 'POST', json: { file_id: f.id } })} onRetry={() => call('/file/update', { method: 'POST', json: { file_id: f.id } })} />
              ))}
            </ul>
          )}
        </Section>
      )}
      {tab === 'settings' && (
        <div className="grid items-start gap-5 xl:grid-cols-2">
          <Section title="About" icon={FileText}>
            <Field label="Name" error={nameErr}>
              <input className={input} value={name} disabled={!w} aria-invalid={!!nameErr} required onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label="Description" hint="Assistants see this when choosing what to search.">
              <textarea className={cx(input, 'min-h-24 resize-y')} value={desc} disabled={!w} onChange={(e) => setDesc(e.target.value)} aria-label="Description" />
            </Field>
            {w && (
              <div className="flex justify-end">
                <button type="button" className={btnPrimary} disabled={!!nameErr || (name === c.name && desc === c.description)} onClick={() => call('/update', { method: 'POST', json: { name, description: desc } }, 'Saved')}>
                  Save
                </button>
              </div>
            )}
          </Section>
          <Section title="Index" icon={Binary}>
            <dl className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-3">
              <div className="rounded-xl border border-line p-3">
                <dt className="text-xs text-muted">Files</dt>
                <dd className="text-lg font-semibold tabular-nums">{c.status.files}</dd>
              </div>
              <div className="rounded-xl border border-line p-3">
                <dt className="text-xs text-muted">Chunks</dt>
                <dd className="text-lg font-semibold tabular-nums">{c.status.chunks.toLocaleString()}</dd>
              </div>
              <div className="col-span-2 rounded-xl border border-line p-3 sm:col-span-1">
                <dt className="text-xs text-muted">Embeddings</dt>
                <dd className="truncate font-medium" title={c.embedding_model?.model}>
                  {c.embedding_model ? `${c.embedding_model.model} (${c.embedding_model.dim}-d)` : 'Not set yet'}
                </dd>
              </div>
            </dl>
            {c.reindexing && <Badge tone="amber">moving to a new embedding model</Badge>}
            {w && (
              <>
                <p className="text-sm text-muted">
                  Re-embed every file, for example after changing the chunk settings.
                  {me?.role === 'admin' && ' Name another embedding model to move the collection to it; searches keep using the current model until every file is done.'}
                </p>
                <div className="flex flex-wrap gap-2">
                  {me?.role === 'admin' && <input className={cx(input, 'max-w-xs py-1.5')} placeholder={c.embedding_model?.model ?? 'embedding model (optional)'} value={model} onChange={(e) => setModel(e.target.value)} aria-label="Embedding model" />}
                  <button type="button" className={btn} onClick={() => call(`/reindex${model.trim() ? `?embedding_model=${encodeURIComponent(model.trim())}` : ''}`, { method: 'POST' }, 'Reindexing started')} data-testid="reindex">
                    <RefreshCw size={14} /> Reindex
                  </button>
                </div>
              </>
            )}
          </Section>
        </div>
      )}
      {tab === 'access' && (
        <Section title="Access" icon={UserCog} hint="Who can search or edit this collection." className="max-w-4xl">
          <AccessEditor url={`/api/v1/knowledge/${c.id}/grants`} canEdit={w} />
        </Section>
      )}
    </div>
  )
}

function FileRow({ f, canEdit, onRemove, onRetry }: { f: KnowledgeFile; canEdit: boolean; onRemove: () => void; onRetry: () => void }) {
  const st = f.data.status
  return (
    <li className="flex items-center gap-3 px-3 py-2.5 text-sm" data-testid="knowledge-file">
      <FileText size={16} className="shrink-0 text-muted" aria-hidden />
      <span className="min-w-0 flex-1">
        <a href={`/api/v1/files/${f.id}/content`} target="_blank" rel="noreferrer" className="block truncate hover:underline">
          {f.filename}
        </a>
        {f.data.error && <span className="block truncate text-xs text-red-600 dark:text-red-400">{f.data.error}</span>}
      </span>
      <span className="hidden text-xs tabular-nums text-muted sm:inline">{formatBytes(f.size)}</span>
      {st === 'completed' && <span className="hidden text-xs tabular-nums text-muted sm:inline">{f.chunks} chunks</span>}
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
