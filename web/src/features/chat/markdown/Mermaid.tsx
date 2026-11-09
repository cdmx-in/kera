import { useEffect, useState } from 'react'
import { Check, Code2, Copy, Download, Network } from 'lucide-react'
import { CodeBlock } from './CodeBlock'

let seq = 0

// Renders a ```mermaid fence. The SVG is shown through <img>, so nothing in a
// diagram (model output can quote KB or ticket text) can run script, on top of
// mermaid's strict sanitising. A parse error falls back to the code block.
// ponytail: theme is read at render time; a theme toggle applies on next mount.
export function Mermaid({ code }: { code: string }) {
  const [url, setUrl] = useState<string | null>(null)
  const [width, setWidth] = useState<number>()
  const [failed, setFailed] = useState(false)
  const [showCode, setShowCode] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    let live = true
    let made = ''
    setFailed(false)
    import('mermaid')
      .then(async ({ default: mermaid }) => {
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          theme: document.documentElement.classList.contains('dark') ? 'dark' : 'default',
          suppressErrorRendering: true,
        })
        const html = (await mermaid.render(`kera-mermaid-${++seq}`, code)).svg
        if (!live) return
        // HTML labels contain void tags like <br>; an <img> or .svg file parses
        // the SVG as XML, so re-serialise it into well-formed XML first.
        const el = new DOMParser().parseFromString(html, 'text/html').querySelector('svg')
        const svg = el ? new XMLSerializer().serializeToString(el) : html
        made = URL.createObjectURL(new Blob([svg], { type: 'image/svg+xml' }))
        // An <img> of a width="100%" SVG has no natural size; use the viewBox width.
        setWidth(Number(/viewBox="[\d.-]+ [\d.-]+ ([\d.]+)/.exec(svg)?.[1]) || undefined)
        setUrl(made)
      })
      .catch(() => live && setFailed(true))
    return () => {
      live = false
      if (made) URL.revokeObjectURL(made)
    }
  }, [code])

  if (failed)
    return (
      <div>
        <CodeBlock code={code} lang="mermaid" />
        <p className="-mt-2 mb-3 text-xs text-muted">This diagram could not be drawn, so its source is shown.</p>
      </div>
    )

  const copy = async () => {
    await navigator.clipboard?.writeText(code).catch(() => undefined)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  const btn = 'inline-flex items-center gap-1 rounded px-1.5 py-0.5 hover:bg-surface-2 hover:text-fg'
  return (
    <div className="my-3 overflow-hidden rounded-xl border border-line bg-bg-soft">
      <div className="flex items-center justify-between border-b border-line px-3 py-1 text-xs text-muted">
        <span>diagram</span>
        <span className="flex items-center gap-1">
          <button type="button" onClick={() => setShowCode((v) => !v)} className={btn}>
            {showCode ? <Network size={13} /> : <Code2 size={13} />}
            {showCode ? 'Diagram' : 'Code'}
          </button>
          <button type="button" onClick={copy} aria-label="Copy diagram source" className={btn}>
            {copied ? <Check size={13} /> : <Copy size={13} />}
            {copied ? 'Copied' : 'Copy'}
          </button>
          {url && (
            <a href={url} download="diagram.svg" className={`${btn} no-underline`}>
              <Download size={13} />
              SVG
            </a>
          )}
        </span>
      </div>
      {showCode ? (
        <pre className="m-0 overflow-x-auto p-3 text-[13px] leading-relaxed">
          <code>{code}</code>
        </pre>
      ) : url ? (
        <a href={url} target="_blank" rel="noreferrer" title="Open full size" className="block overflow-x-auto p-3">
          <img src={url} alt="Diagram" width={width} className="mx-auto h-auto max-w-full" />
        </a>
      ) : (
        <div className="p-3 text-xs text-muted">Drawing diagram…</div>
      )}
    </div>
  )
}
