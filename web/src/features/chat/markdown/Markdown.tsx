import { isValidElement, memo, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Source } from '@/api/types'
import { prettyFileName } from '@/lib/format'
import { Favicon, SourceLink } from '../messages/Sources'
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

// Messages imported from other chat apps (and some model output) carry a few
// inline HTML tags. Raw HTML is never rendered, so an allow-list is mapped to
// Markdown and tags without an equivalent (<u>, <span>) are dropped, outside code.
const inlineTags: [RegExp, string][] = [
  [/<br\s*\/?>/gi, '  \n'],
  [/<\/?(?:b|strong)>/gi, '**'],
  [/<\/?(?:i|em)>/gi, '*'],
  [/<\/?(?:s|del|strike)>/gi, '~~'],
  [/<\/?code>/gi, '`'],
  [/<\/?(?:u|ins|span|mark|small|font)(?:\s[^>]*)?>/gi, ''],
]

export function inlineHtmlToMarkdown(text: string): string {
  if (!text.includes('<')) return text
  return text
    .split(/(```[\s\S]*?(?:```|$)|`[^`\n]*`)/)
    .map((seg, i) => (i % 2 ? seg : inlineTags.reduce((t, [re, md]) => t.replace(re, md), seg)))
    .join('')
}

// Typed messages keep their single line breaks (Markdown would join them).
export function keepLineBreaks(text: string): string {
  return text
    .split(/(```[\s\S]*?(?:```|$))/)
    .map((seg, i) => (i % 2 ? seg : seg.replace(/([^\n])\n(?!\n)/g, '$1  \n')))
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
    // Inline code naming a cited file ("Sources: `x.md`") becomes a source chip.
    code({ className, children }) {
      const name = textOf(children)
      const s = !className && sources.find((x) => x.name === name)
      if (!s) return <code className={className}>{children}</code>
      return (
        <SourceLink source={s} className="inline-flex max-w-full items-center gap-1 rounded-md border border-line bg-surface px-1.5 py-0.5 align-middle text-xs font-medium no-underline hover:bg-surface-2">
          <Favicon url={s.url} size={14} />
          <span className="truncate">{s.title ?? prettyFileName(name)}</span>
        </SourceLink>
      )
    },
    a({ href, children }) {
      const cite = href?.match(/^#cite-(\d+)$/)
      if (cite) {
        const s = sources[Number(cite[1]) - 1] ?? {}
        return (
          <sup>
            <SourceLink source={s} className="ml-0.5 rounded bg-accent-soft px-1 text-[10px] font-semibold no-underline">
              {cite[1]}
            </SourceLink>
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

export const Markdown = memo(function Markdown({ text, sources = [], breaks = false, className = 'md' }: { text: string; sources?: Source[]; breaks?: boolean; className?: string }) {
  const md = inlineHtmlToMarkdown(breaks ? keepLineBreaks(text) : text)
  return (
    <div className={className}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components(sources)}>
        {linkCitations(md, sources.length)}
      </ReactMarkdown>
    </div>
  )
})
