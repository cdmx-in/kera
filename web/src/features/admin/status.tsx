// One vocabulary for every status in the pipeline pages: a small, bold, upper-case word with its own icon,
// so a status is never told by colour alone. The DOM text stays in sentence case (the CSS upper-cases it), which keeps it readable by a screen reader.
import { Ban, Check, CircleAlert, CircleDashed, CircleOff, CircleSlash, CircleX, Clock, FlaskConical, Loader2, NotebookPen, OctagonX, Square, TriangleAlert, type LucideIcon } from 'lucide-react'
import { cx } from '@/lib/format'
import type { State, StepStatus } from './pipelines'
import type { RunChip } from './runview'

export type StatusKey = State | StepStatus | RunChip | 'stopped' | 'dry' | 'notebook'
export type StatusTone = 'idle' | 'accent' | 'ok' | 'warn' | 'bad'

const defs: Record<StatusKey, { icon: LucideIcon; word: string; tone: StatusTone }> = {
  queued: { icon: Clock, word: 'Queued', tone: 'idle' },
  running: { icon: Loader2, word: 'Running', tone: 'accent' },
  stopping: { icon: Square, word: 'Stopping', tone: 'warn' },
  ok: { icon: Check, word: 'OK', tone: 'ok' },
  warning: { icon: TriangleAlert, word: 'Warning', tone: 'warn' },
  failed: { icon: CircleX, word: 'Failed', tone: 'bad' },
  cancelled: { icon: Ban, word: 'Cancelled', tone: 'warn' },
  stopped: { icon: OctagonX, word: 'Stopped', tone: 'warn' },
  skipped: { icon: CircleSlash, word: 'Skipped', tone: 'idle' },
  pending: { icon: CircleDashed, word: 'Pending', tone: 'idle' },
  late: { icon: CircleAlert, word: 'Late', tone: 'warn' },
  never: { icon: CircleDashed, word: 'Never run', tone: 'idle' },
  off: { icon: CircleOff, word: 'Off', tone: 'idle' },
  dry: { icon: FlaskConical, word: 'Dry run', tone: 'accent' },
  notebook: { icon: NotebookPen, word: 'Notebook', tone: 'accent' },
}

/** The words as the chips show them (before the CSS upper-cases them). */
export const statusWord = (s: StatusKey): string => defs[s].word
export const statusTone = (s: StatusKey): StatusTone => defs[s].tone

/** Text colour of a tone, readable on its own wash in both themes. */
export const inkOf: Record<StatusTone, string> = { idle: 'text-fg/70', accent: 'text-accent-ink', ok: 'text-ok-ink', warn: 'text-warn-ink', bad: 'text-bad-ink' }
/** The tinted background of a chip. */
const washOf: Record<StatusTone, string> = { idle: 'bg-surface-2', accent: 'bg-accent-soft', ok: 'bg-ok-soft', warn: 'bg-warn-soft', bad: 'bg-bad-soft' }
/** Solid fill for bars and rail segments. */
export const fillOf: Record<StatusTone, string> = { idle: 'bg-line', accent: 'bg-accent', ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad' }

/** The icon of a status; a running one turns, a stopping one pulses (both stand still under prefers-reduced-motion). */
export function StatusIcon({ status, size = 12 }: { status: StatusKey; size?: number }) {
  const Icon = defs[status].icon
  return <Icon size={size} strokeWidth={status === 'ok' || status === 'failed' ? 2.75 : 2.25} aria-hidden className={cx('shrink-0', status === 'running' && 'motion-safe:animate-spin', status === 'stopping' && 'fill-current motion-safe:animate-pulse')} />
}

/**
 * Icon and upper-case word. `bare` drops the tinted box (for lists, where a box on every row would be a wall of chips).
 */
export function StatusChip({ status, bare, title, className }: { status: StatusKey; bare?: boolean; title?: string; className?: string }) {
  const d = defs[status]
  return (
    <span
      title={title}
      data-status-chip={status}
      className={cx(
        'inline-flex items-center gap-1 whitespace-nowrap text-[10.5px] font-bold uppercase leading-none tracking-[0.08em]',
        inkOf[d.tone],
        !bare && cx('rounded-md px-1.5 py-1 ring-1 ring-inset ring-current/15', washOf[d.tone]),
        className,
      )}
    >
      <StatusIcon status={status} size={bare ? 11 : 12} /> {d.word}
    </span>
  )
}
