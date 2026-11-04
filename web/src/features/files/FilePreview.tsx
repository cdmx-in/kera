// Lazy chunk: near-fullscreen preview. Tables come pre-parsed from the server
// (GET /api/v1/files/{id}/preview?sheet=n) so dates and numbers show exactly as
// stored; PDFs use an iframe (cookie auth); Download is always available.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { api } from '@/api/client'
import type { FilePreview as Preview, FileRef } from '@/api/types'
import { cx } from '@/lib/format'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Spinner } from '@/ui/misc'
import { fileUrl } from '@/features/chat/messages/FileChip'
import { Markdown } from '@/features/chat/markdown/Markdown'
import { tableModel } from './table'

export default function FilePreview({ file, onClose }: { file: FileRef; onClose: () => void }) {
  const [sheet, setSheet] = useState(0)
  const q = useQuery({
    queryKey: ['preview', file.id, sheet],
    queryFn: () => api<Preview>(`/api/v1/files/${encodeURIComponent(file.id)}/preview?sheet=${sheet}`),
    staleTime: Infinity,
  })
  return (
    <Dialog open onClose={onClose} label={`Preview of ${file.name}`} testId="file-preview" className="h-[calc(100dvh-2rem)] w-[min(90rem,calc(100vw-2rem))]">
      <div className="flex h-full flex-col">
        <DialogHeader title={file.name} onClose={onClose}>
          <a href={fileUrl(file.id, true)} download={file.name} className="inline-flex items-center gap-1.5 rounded-lg border border-line px-2.5 py-1 text-sm hover:bg-surface-2">
            <Download size={14} /> Download
          </a>
        </DialogHeader>
        {q.data?.sheets && q.data.sheets.length > 1 && (
          <div role="tablist" className="flex gap-1 overflow-x-auto border-b border-line px-3 py-1.5">
            {q.data.sheets.map((s, i) => (
              <button key={s + i} role="tab" aria-selected={i === sheet} type="button" onClick={() => setSheet(i)} className={cx('whitespace-nowrap rounded-md px-2.5 py-1 text-sm', i === sheet ? 'bg-accent-soft font-medium' : 'text-muted hover:bg-surface-2')}>
                {s}
              </button>
            ))}
          </div>
        )}
        <div className="min-h-0 flex-1 overflow-auto">
          {q.isPending ? (
            <div className="grid h-full place-items-center text-muted">
              <Spinner size={20} />
            </div>
          ) : q.isError ? (
            <p className="p-6 text-sm text-muted">No preview available for this file. Use Download instead.</p>
          ) : (
            <Body preview={q.data} file={file} />
          )}
        </div>
      </div>
    </Dialog>
  )
}

function Body({ preview, file }: { preview: Preview; file: FileRef }) {
  if (preview.kind === 'pdf') return <iframe title={file.name} src={fileUrl(file.id)} className="h-full w-full border-0" />
  if (preview.kind === 'image') return <img src={fileUrl(file.id)} alt={file.name} className="mx-auto max-h-full max-w-full object-contain p-4" />
  if (preview.kind === 'text' && preview.text && /\.(md|markdown)$/i.test(file.name)) return <Markdown text={preview.text} className="md p-4" />
  if (preview.kind === 'text' || preview.kind === 'docx') return <pre className="whitespace-pre-wrap p-4 text-sm">{preview.text ?? (preview.rows ?? []).map((r) => r.join('\t')).join('\n')}</pre>
  if (preview.kind === 'other') return <p className="p-6 text-sm text-muted">No preview available for this file. Use Download instead.</p>
  const t = tableModel(preview.rows)
  return (
    <>
      <table className="border-separate border-spacing-0 text-sm">
        <thead>
          <tr>
            {t.header.map((h, i) => (
              <th key={i} className={cx('sticky top-0 z-10 whitespace-nowrap border-b border-r border-line bg-bg-soft px-3 py-1.5 text-left font-semibold', i === 0 && 'left-0 z-20')}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {t.rows.map((r, i) => (
            <tr key={i} className="hover:bg-surface-2">
              {r.map((c, j) => (
                <td key={j} className={cx('max-w-96 truncate whitespace-nowrap border-b border-r border-line px-3 py-1', j === 0 && 'sticky left-0 bg-surface font-medium')} title={c}>
                  {c}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {preview.truncated && <p className="p-3 text-xs text-muted">Showing the first {t.rows.length} rows. Download the file for everything.</p>}
    </>
  )
}
