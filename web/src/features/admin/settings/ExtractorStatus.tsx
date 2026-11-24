// The document extractor card on Retrieval: the address in effect, whether it answers, what it reads.
import { CheckCircle2, CircleAlert, Loader2, XCircle } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { cx } from '@/lib/format'
import { useAdmin } from '../api'
import { Btn } from '../ui'

export interface ExtractorStatusOut {
  url: string
  ok: boolean
  ms: number
  error?: string
  types?: string[]
  ocr?: boolean
  ocr_langs?: string
}

const tones = {
  red: 'border-red-300 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300',
  amber: 'border-amber-300 bg-amber-50 text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300',
  green: 'border-green-300 bg-green-50 text-green-800 dark:border-green-900 dark:bg-green-950 dark:text-green-300',
}

/** One sentence per state, and the tone to show it in. */
export function extractorVerdict(s: ExtractorStatusOut): { tone: keyof typeof tones; text: string } {
  if (!s.url) return { tone: 'amber', text: 'No extractor address is set. PDF, DOCX, PPTX and ODT uploads fail until one is.' }
  if (!s.ok) return { tone: 'red', text: `Cannot use ${s.url}: ${s.error ?? 'no answer'}. PDF, DOCX, PPTX and ODT uploads fail until it works.` }
  const reads = (s.types ?? []).map((t) => t.replace('.', '').toUpperCase()).join(', ')
  if (s.ocr === false) return { tone: 'amber', text: `Running at ${s.url} (${reads}), but OCR is not installed in it: scanned pages stay empty.` }
  const ocr = s.ocr ? ` · OCR of scans on${s.ocr_langs ? ` (${s.ocr_langs})` : ''}` : ''
  return { tone: 'green', text: `Running at ${s.url} · reads ${reads}${ocr} · ${s.ms} ms` }
}

export function ExtractorStatus() {
  const q = useAdmin<ExtractorStatusOut>(['extractor'], '/api/v1/configs/extractor/status')
  const qc = useQueryClient()
  const v = q.data ? extractorVerdict(q.data) : null
  const Icon = v?.tone === 'green' ? CheckCircle2 : v?.tone === 'red' ? XCircle : CircleAlert
  return (
    <div className="px-5 py-4" data-testid="extractor-status">
      <div role="status" className={cx('flex items-start gap-2 rounded-lg border p-3 text-sm', tones[v?.tone ?? 'amber'])}>
        {q.isFetching || !v ? <Loader2 size={16} className="mt-0.5 shrink-0 animate-spin" aria-hidden /> : <Icon size={16} className="mt-0.5 shrink-0" aria-hidden />}
        <span className="min-w-0 flex-1 break-words">{q.error ? (q.error as Error).message : q.isFetching && !v ? 'Checking the extractor…' : v?.text}</span>
        <Btn variant="ghost" onClick={() => void qc.invalidateQueries({ queryKey: ['admin', 'extractor'] })} disabled={q.isFetching}>
          Check again
        </Btn>
      </div>
    </div>
  )
}
