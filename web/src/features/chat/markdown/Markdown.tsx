import { isValidElement, memo, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Source } from '@/api/types'
import { domainOf } from '@/lib/format'
import { CodeBlock } from './CodeBlock'

// "[3]" citation markers (outside code) become links to "#cite-3" so the
// anchor renderer can turn them into superscript chips.
export function linkCitations(text: string, count: number): string {
  if (!count) return text
  return text
    .split(/(```[\s\S]*?(?:```|$)|`[^`\n]*`)/)
    .map((seg, i) =>
      i % 2
        ? seg
        : seg.replace(/(?<![\]\\])\[(\d{1,3})\](?![([])/g, (m, n: string, at: number, all: string) => {
            // "[1]: url" at the start of a line is a link reference definition
            const lineStart = all.lastIndexOf('\n', at - 1) + 1
            if (all[at + m.length] === ':' && all.slice(lineStart, at).trim() === '') return m
            return Number(n) >= 1 && Number(n) <= count ? `[${n}](#cite-${n})` : m
          }),
    )
    .join('')
}

function textOf(node: ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(textOf).join('')
  if (isValidElement<{ children?: ReactNode }>(node)) return textOf(node.props.children)
  return ''
}

function components(sources: Source[]): Components {
  return {
    pre({ children }) {
      const child = Array.isArray(children) ? children[0] : children
      if (isValidElement<{ className?: string; children?: ReactNode }>(child)) {
        const lang = /language-([\w+-]+)/.exec(child.props.className ?? '')?.[1] ?? ''
        return <CodeBlock code={textOf(child.props.children).replace(/\n$/, '')} lang={lang.toLowerCase()} />
      }
      return <pre>{children}</pre>
    },
    a({ href, children }) {
      const cite = href?.match(/^#cite-(\d+)$/)
      if (cite) {
        const s = sources[Number(cite[1]) - 1]
        return (
          <sup>
            <a href={s?.url || undefined} target="_blank" rel="noreferrer" title={s?.title ?? s?.name ?? domainOf(s?.url)} className="ml-0.5 rounded bg-accent-soft px-1 text-[10px] font-semibold no-underline">
              {cite[1]}
            </a>
          </sup>
        )
      }
      return (
        <a href={href} target="_blank" rel="noreferrer">
          {children}
        </a>
      )
    },
  }
}

export const Markdown = memo(function Markdown({ text, sources = [] }: { text: string; sources?: Source[] }) {
  return (
    <div className="md">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components(sources)}>
        {linkCitations(text, sources.length)}
      </ReactMarkdown>
    </div>
  )
})
