// Small pieces the wizard steps share.
import type { ReactNode } from 'react'
import { X } from 'lucide-react'
import { cx } from '@/lib/format'

/** The heading of a step. The wizard moves focus here when the step changes, so a screen reader hears where it is. */
export function StepHeader({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <header className="mb-5">
      <h2 tabIndex={-1} data-step-title className="text-xl font-semibold tracking-tight outline-none">
        {title}
      </h2>
      {children && <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted">{children}</p>}
    </header>
  )
}

/** A titled block of a step. */
export function Block({ title, hint, children, className }: { title: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx('space-y-2', className)}>
      <div>
        <h3 className="text-sm font-semibold">{title}</h3>
        {hint && <p className="mt-0.5 text-xs leading-relaxed text-muted">{hint}</p>}
      </div>
      {children}
    </section>
  )
}

/** A removable chip (knowledge collections, selected apps, files). */
export function RemovableChip({ children, onRemove, label, testId, id }: { children: ReactNode; onRemove: () => void; label: string; testId?: string; id?: string }) {
  return (
    <li data-testid={testId} data-id={id} className="inline-flex max-w-full items-center gap-1 rounded-full border border-line bg-surface-2 py-0.5 pl-3 pr-1 text-sm">
      <span className="min-w-0 truncate">{children}</span>
      <button type="button" aria-label={label} onClick={onRemove} className="grid size-5 shrink-0 place-items-center rounded-full text-muted hover:bg-surface hover:text-fg">
        <X size={12} aria-hidden />
      </button>
    </li>
  )
}

const tones = {
  ok: 'bg-ok-soft text-ok-ink',
  warn: 'bg-warn-soft text-warn-ink',
  bad: 'bg-bad-soft text-bad-ink',
  accent: 'bg-accent-soft text-accent-ink',
  grey: 'bg-surface-2 text-muted',
}

/** A status word on its own wash. Never the only signal: the word is always there. */
export function Pill({ tone, children, title, testId }: { tone: keyof typeof tones; children: ReactNode; title?: string; testId?: string }) {
  return (
    <span title={title} data-testid={testId} className={cx('inline-flex shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-2 py-0.5 text-[11px] font-medium', tones[tone])}>
      {children}
    </span>
  )
}

/** The look of a choice card whose radio or checkbox is a visually hidden input before the card's content (peer). */
export const choiceCard =
  'relative flex min-w-0 cursor-pointer gap-3 rounded-xl border border-line bg-surface p-3.5 text-left transition-colors hover:border-accent/50 has-[:checked]:border-accent has-[:checked]:bg-accent-soft has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-accent has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60 has-[:disabled]:hover:border-line motion-reduce:transition-none'
