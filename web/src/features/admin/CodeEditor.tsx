// A textarea with a line-number gutter that scrolls with it. No dependency: both use the same font, line height and padding.
// The gutter can start at any line number (a notebook cell shows its place in the whole file) and mark lines that have problems.
import { useImperativeHandle, useMemo, useRef, type KeyboardEvent, type Ref } from 'react'
import { CircleAlert, Info, TriangleAlert } from 'lucide-react'
import { cx } from '@/lib/format'
import type { Severity } from './pipelines'

export interface EditorHandle {
  focus: () => void
  /** puts the cursor on a line of this editor (1-based), selects it and scrolls it into view */
  focusLine: (line: number) => void
}

const LINE = 20 // leading-5
const PAD = 8 // py-2

const markIcon = { error: CircleAlert, warning: TriangleAlert, info: Info }
const markTone = { error: 'text-red-600 dark:text-red-400', warning: 'text-amber-600 dark:text-amber-400', info: 'text-sky-600 dark:text-sky-400' }
const markWord = { error: 'Error', warning: 'Warning', info: 'Note' }

export function CodeEditor({
  value,
  onChange,
  readOnly,
  label,
  startLine = 1,
  marks,
  rows,
  editorRef,
  onKeyDown,
}: {
  value: string
  onChange?: (v: string) => void
  readOnly?: boolean
  label: string
  /** the number shown for the first line */
  startLine?: number
  /** the worst problem on a line, by line within this editor (1-based) */
  marks?: Record<number, Severity>
  /** a height that fits this many lines (3 to 24); without it the editor is as tall as the page allows */
  rows?: number
  editorRef?: Ref<EditorHandle>
  onKeyDown?: (e: KeyboardEvent<HTMLTextAreaElement>) => void
}) {
  const gutter = useRef<HTMLDivElement>(null)
  const area = useRef<HTMLTextAreaElement>(null)
  const count = value.split('\n').length
  const numbers = useMemo(() => Array.from({ length: count }, (_, i) => i + startLine).join('\n'), [count, startLine])
  useImperativeHandle(
    editorRef,
    () => ({
      focus: () => area.current?.focus(),
      focusLine: (line) => {
        const ta = area.current
        if (!ta) return
        const lines = ta.value.split('\n')
        const at = Math.min(Math.max(line, 1), lines.length) - 1
        const from = lines.slice(0, at).reduce((n, l) => n + l.length + 1, 0)
        ta.focus()
        ta.setSelectionRange(from, from + lines[at].length)
        ta.scrollTop = Math.max(0, (at - 2) * LINE)
        if (gutter.current) gutter.current.scrollTop = ta.scrollTop
      },
    }),
    [],
  )
  return (
    <div
      className={cx('flex overflow-hidden rounded-lg border border-line bg-surface font-mono text-xs leading-5 focus-within:border-accent', !rows && 'h-[min(32rem,70dvh)]')}
      style={rows ? { height: Math.min(Math.max(rows, 3), 24) * LINE + 2 * PAD + 14 } : undefined}
      data-testid="code-editor"
    >
      {/* the extra bottom padding lets it scroll as far as the textarea, whose horizontal scrollbar takes some of its height */}
      <div ref={gutter} aria-hidden data-testid="gutter" className="relative w-12 shrink-0 select-none overflow-hidden border-r border-line bg-surface-2/50 py-2 pr-2 pb-8 text-right text-muted">
        <pre>{numbers}</pre>
        {Object.entries(marks ?? {}).map(([line, sev]) => {
          const Icon = markIcon[sev]
          return (
            <span key={line} data-testid="gutter-mark" data-severity={sev} title={`${markWord[sev]} on line ${Number(line) + startLine - 1}`} className={cx('absolute left-1 grid size-5 place-items-center', markTone[sev])} style={{ top: PAD + (Number(line) - 1) * LINE }}>
              <Icon size={11} />
            </span>
          )
        })}
      </div>
      <textarea
        ref={area}
        aria-label={label}
        value={value}
        readOnly={readOnly}
        wrap="off"
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        onChange={(e) => onChange?.(e.target.value)}
        onKeyDown={onKeyDown}
        onScroll={(e) => gutter.current && (gutter.current.scrollTop = e.currentTarget.scrollTop)}
        className={cx('min-w-0 flex-1 resize-none overflow-auto whitespace-pre bg-transparent px-3 py-2 outline-none', readOnly && 'text-muted')}
      />
    </div>
  )
}
