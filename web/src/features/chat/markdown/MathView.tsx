// Lazy chunk: KaTeX and its stylesheet load only when an answer contains a formula.
import { useMemo } from 'react'
import katex from 'katex'
import 'katex/dist/katex.min.css'

export default function MathView({ tex, display }: { tex: string; display: boolean }) {
  const html = useMemo(() => {
    try {
      // trust stays off (no \href, \includegraphics); an error shows the source instead of breaking the answer
      return katex.renderToString(tex, { displayMode: display, throwOnError: true, strict: 'ignore', trust: false, output: 'htmlAndMathml' })
    } catch {
      return null
    }
  }, [tex, display])
  if (html === null) return <code>{tex}</code>
  return <span className={display ? 'my-3 block overflow-x-auto overflow-y-hidden' : undefined} dangerouslySetInnerHTML={{ __html: html }} />
}
