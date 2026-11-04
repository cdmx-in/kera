import { useState } from 'react'
import { FileText } from 'lucide-react'
import type { Source } from '@/api/types'
import { domainOf } from '@/lib/format'
import { Dialog, DialogHeader } from '@/ui/Dialog'

export function Favicon({ url, size = 16 }: { url?: string; size?: number }) {
  const d = domainOf(url)
  const [broken, setBroken] = useState(false)
  if (!d || broken) return <FileText size={size - 2} className="text-muted" />
  return <img src={`/api/v1/favicon?domain=${encodeURIComponent(d)}`} alt="" width={size} height={size} loading="lazy" onError={() => setBroken(true)} className="rounded-sm" />
}

export function Sources({ sources }: { sources: Source[] }) {
  const [open, setOpen] = useState(false)
  if (!sources.length) return null
  const shown = sources.slice(0, 5)
  return (
    <>
      <button type="button" data-testid="sources" onClick={() => setOpen(true)} className="mt-2 inline-flex items-center gap-2 rounded-full border border-line px-2.5 py-1 text-xs text-muted hover:bg-surface-2 hover:text-fg">
        <span className="flex -space-x-1.5">
          {shown.map((s, i) => (
            <span key={i} className="grid size-6 place-items-center rounded-full border border-line bg-surface">
              <Favicon url={s.url} size={14} />
            </span>
          ))}
        </span>
        {sources.length} {sources.length === 1 ? 'source' : 'sources'}
      </button>
      <Dialog open={open} onClose={() => setOpen(false)} drawer label="Sources">
        <div className="flex h-full flex-col">
          <DialogHeader title="Sources" onClose={() => setOpen(false)} />
          <ol className="flex-1 space-y-1 overflow-y-auto p-3">
            {sources.map((s, i) => (
              <li key={i}>
                <a href={s.url || undefined} target="_blank" rel="noreferrer" className="flex gap-3 rounded-lg p-2 hover:bg-surface-2">
                  <span className="mt-0.5 text-xs text-muted">{i + 1}</span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-1.5 text-xs text-muted">
                      <Favicon url={s.url} size={14} />
                      {domainOf(s.url) || s.name}
                      {s.page != null && ` · p. ${s.page}`}
                    </span>
                    <span className="block truncate text-sm font-medium">{s.title ?? s.name ?? s.url}</span>
                    {s.snippet && <span className="line-clamp-2 text-xs text-muted">{s.snippet}</span>}
                  </span>
                </a>
              </li>
            ))}
          </ol>
        </div>
      </Dialog>
    </>
  )
}
