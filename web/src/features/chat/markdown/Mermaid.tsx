import { useEffect, useRef, useState } from 'react'
import { Check, Code2, Copy, Download, Network, ZoomIn, ZoomOut } from 'lucide-react'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { CodeBlock } from './CodeBlock'
import { copyText } from '@/lib/format'

let seq = 0

// Renders a ```mermaid fence. The SVG is shown through <img>, so nothing in a
// diagram (model output can quote KB or ticket text) can run script, on top of
// mermaid's strict sanitising. A parse error falls back to the code block.
// ponytail: theme is read at render time; a theme toggle applies on next mount.
export function Mermaid({ code }: { code: string }) {
  const [url, setUrl] = useState<string | null>(null)
  const [size, setSize] = useState<[number, number]>()
  const width = size?.[0]
  const [failed, setFailed] = useState(false)
  const [showCode, setShowCode] = useState(false)
  const [copied, setCopied] = useState(false)
  // Viewer: null = closed, 0 = fit to window, else zoom factor of the natural size.
  const [view, setView] = useState<number | null>(null)
  const big = useRef<HTMLImageElement>(null)

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
        // An <img> of a width="100%" SVG has no natural size; use the viewBox.
        const vb = /viewBox="[\d.-]+ [\d.-]+ ([\d.]+) ([\d.]+)/.exec(svg)
        setSize(vb ? [Number(vb[1]), Number(vb[2])] : undefined)
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
    if (!(await copyText(code))) return
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  // From "fit", the first step starts at the scale object-contain is showing.
  const fit = () => (size && big.current ? Math.min(big.current.clientWidth / size[0], big.current.clientHeight / size[1]) : 1)
  const zoom = (f: number) => setView((v) => Math.min(8, Math.max(0.1, (v || fit()) * f)))
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
        <button type="button" onClick={() => setView(0)} title="Open larger" className="block w-full cursor-zoom-in overflow-x-auto p-3">
          <img src={url} alt="Diagram" width={width} className="mx-auto h-auto max-w-full" />
        </button>
      ) : (
        <div className="p-3 text-xs text-muted">Drawing diagram…</div>
      )}
      {url && (
        <Dialog open={view !== null} onClose={() => setView(null)} label="Diagram" className="h-[calc(100dvh-2rem)] w-[min(90rem,calc(100vw-2rem))]">
          <div className="flex h-full flex-col">
            <DialogHeader title="Diagram" onClose={() => setView(null)}>
              <IconButton label="Zoom out" onClick={() => zoom(1 / 1.25)}>
                <ZoomOut size={18} />
              </IconButton>
              <button type="button" onClick={() => setView(0)} title="Fit to window" className="w-14 rounded-lg py-1 text-sm text-muted tabular-nums hover:bg-surface-2 hover:text-fg">
                {view ? `${Math.round(view * 100)}%` : 'Fit'}
              </button>
              <IconButton label="Zoom in" onClick={() => zoom(1.25)}>
                <ZoomIn size={18} />
              </IconButton>
              <a href={url} download="diagram.svg" className="inline-flex items-center gap-1.5 rounded-lg border border-line px-2.5 py-1 text-sm hover:bg-surface-2">
                <Download size={14} /> SVG
              </a>
            </DialogHeader>
            <div className="min-h-0 flex-1 overflow-auto bg-bg-soft p-4">
              {view ? (
                <img ref={big} src={url} alt="Diagram" width={(width ?? 800) * view} className="mx-auto h-auto max-w-none" />
              ) : (
                <img ref={big} src={url} alt="Diagram" className="mx-auto h-full w-full object-contain" />
              )}
            </div>
          </div>
        </Dialog>
      )}
    </div>
  )
}
