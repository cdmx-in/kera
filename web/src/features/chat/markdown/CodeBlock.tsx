import { useEffect, useState, type ReactNode } from 'react'
import { Check, Copy } from 'lucide-react'
import { copyText } from '@/lib/format'

export function CodeBlock({ code, lang }: { code: string; lang: string }) {
  const [html, setHtml] = useState<ReactNode | null>(null)
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    let live = true
    if (!lang) return
    const t = setTimeout(() => {
      import('./highlight')
        .then((m) => live && setHtml(m.highlight(code, lang)))
        .catch(() => undefined)
    }, 60)
    return () => {
      live = false
      clearTimeout(t)
    }
  }, [code, lang])
  const copy = async () => {
    if (!(await copyText(code))) return
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div className="my-3 overflow-hidden rounded-xl border border-line bg-bg-soft">
      <div className="flex items-center justify-between border-b border-line px-3 py-1 text-xs text-muted">
        <span>{lang || 'text'}</span>
        <button type="button" onClick={copy} aria-label="Copy code" className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 hover:bg-surface-2 hover:text-fg">
          {copied ? <Check size={13} /> : <Copy size={13} />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre className="m-0 overflow-x-auto p-3 text-[13px] leading-relaxed">
        <code>{html ?? code}</code>
      </pre>
    </div>
  )
}
