import { lazy, Suspense, useState } from 'react'
import { Download, Library } from 'lucide-react'
import type { FileRef } from '@/api/types'
import { cx, fileExt, formatBytes } from '@/lib/format'

const FilePreview = lazy(() => import('@/features/files/FilePreview'))

const extColor: Record<string, string> = {
  csv: 'bg-emerald-600',
  xlsx: 'bg-emerald-700',
  xls: 'bg-emerald-700',
  pdf: 'bg-red-600',
  docx: 'bg-blue-600',
  doc: 'bg-blue-600',
  pptx: 'bg-orange-600',
  json: 'bg-amber-600',
  md: 'bg-gray-600',
  txt: 'bg-gray-500',
  png: 'bg-violet-600',
  jpg: 'bg-violet-600',
  jpeg: 'bg-violet-600',
  zip: 'bg-stone-600',
}

export const fileUrl = (id: string, attachment = false) => `/api/v1/files/${encodeURIComponent(id)}/content${attachment ? '?attachment=true' : ''}`

export function ExtBadge({ name }: { name: string }) {
  const ext = fileExt(name)
  return <span className={cx('flex h-8 w-9 shrink-0 items-center justify-center rounded-md text-[10px] font-bold uppercase text-white', extColor[ext] ?? 'bg-gray-500')}>{ext.slice(0, 4) || 'file'}</span>
}

export function FileChip({ file }: { file: FileRef }) {
  const [open, setOpen] = useState(false)
  const ext = fileExt(file.name).toUpperCase()
  if (file.type === 'collection') {
    // a knowledge collection picked for this message: nothing to preview or download
    return (
      <div data-testid="file-chip" data-name={file.name} data-kind="collection" title="Knowledge collection" className="flex max-w-72 items-center gap-2 rounded-xl border border-line bg-surface p-1.5 pr-3">
        <span className="flex h-8 w-9 shrink-0 items-center justify-center rounded-md bg-accent text-white">
          <Library size={16} />
        </span>
        <span className="min-w-0 truncate text-sm font-medium">{file.name}</span>
      </div>
    )
  }
  if (file.type === 'image') {
    return (
      <a href={fileUrl(file.id)} target="_blank" rel="noreferrer" data-testid="file-chip" data-name={file.name} className="block overflow-hidden rounded-xl border border-line">
        <img src={fileUrl(file.id)} alt={file.name} loading="lazy" className="max-h-48 max-w-64 object-cover" />
      </a>
    )
  }
  return (
    <div data-testid="file-chip" data-name={file.name} className="flex max-w-72 items-center gap-2 rounded-xl border border-line bg-surface p-1.5 pr-1">
      <button type="button" onClick={() => setOpen(true)} className="flex min-w-0 flex-1 items-center gap-2 text-left" aria-label={`Preview ${file.name}`}>
        <ExtBadge name={file.name} />
        <span className="min-w-0">
          <span className="block truncate text-sm font-medium">{file.name}</span>
          <span className="block text-xs text-muted">{[ext, formatBytes(file.size)].filter(Boolean).join(' · ')}</span>
        </span>
      </button>
      <a href={fileUrl(file.id, true)} download={file.name} aria-label={`Download ${file.name}`} title="Download" className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg">
        <Download size={16} />
      </a>
      {open && (
        <Suspense fallback={null}>
          <FilePreview file={file} onClose={() => setOpen(false)} />
        </Suspense>
      )}
    </div>
  )
}
