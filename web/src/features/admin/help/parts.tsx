// Shared pieces of the Help page: setting chips, the detail of a box, the section frame.
import type { ReactNode } from 'react'
import { ExternalLink } from 'lucide-react'
import { cx } from '@/lib/format'
import { DOCS, docUrl, setting, type Box } from './data'

export const mono = 'font-[ui-monospace,SFMono-Regular,Menlo,Consolas,monospace]'

/** A setting key with its default, as a small mono tag. */
export function KeyChip({ k }: { k: Parameters<typeof setting>[0] }) {
  const s = setting(k)
  return (
    <span className={cx('inline-flex max-w-full flex-wrap items-baseline gap-x-1.5 rounded-md border border-line bg-surface-2/60 px-1.5 py-0.5 text-xs', mono)}>
      <span className="break-all font-medium">{s.key}</span>
      <span className="text-muted">= {s.def}</span>
    </span>
  )
}

export function Label({ children }: { children: ReactNode }) {
  return <h4 className="mb-1.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">{children}</h4>
}

export function DocLink({ doc }: { doc: keyof typeof DOCS }) {
  return (
    <a href={docUrl(doc)} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-sm text-accent hover:underline dark:text-accent-bright">
      {DOCS[doc].title} <ExternalLink size={12} aria-hidden />
      <span className="sr-only">(opens GitHub)</span>
    </a>
  )
}

/** What a box does, its settings, where to look and what to read. */
export function BoxDetail({ box, narrow }: { box: Box; narrow?: boolean }) {
  return (
    <div className={cx('grid gap-5 text-sm leading-relaxed', !narrow && 'md:grid-cols-2')} data-testid="box-detail" data-box={box.id}>
      <div>
        <Label>What it does</Label>
        <ul className="space-y-2">
          {box.what.map((t) => (
            <li key={t}>{t}</li>
          ))}
        </ul>
      </div>
      <div className="space-y-5">
        <div>
          <Label>Settings that control it</Label>
          {box.keys.length === 0 && !box.notes && <p className="text-muted">None.</p>}
          {box.keys.length > 0 && (
            <ul className="space-y-1.5">
              {box.keys.map((k) => (
                <li key={k} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                  <KeyChip k={k} />
                  <span className="text-xs text-muted">{setting(k).where}</span>
                </li>
              ))}
            </ul>
          )}
          {box.notes?.map((n) => (
            <p key={n} className="mt-2 text-xs text-muted">
              {n}
            </p>
          ))}
        </div>
        <div>
          <Label>When it misbehaves</Label>
          <ul className="list-disc space-y-1 pl-4">
            {box.look.map((t) => (
              <li key={t}>{t}</li>
            ))}
          </ul>
        </div>
        <div>
          <Label>Read more</Label>
          <DocLink doc={box.doc} />
        </div>
      </div>
    </div>
  )
}

export function Section({ id, title, lede, children }: { id: string; title: string; lede?: ReactNode; children: ReactNode }) {
  return (
    <section id={id} aria-labelledby={`${id}-h`} className="scroll-mt-4 space-y-5">
      <div className="space-y-1.5">
        <h3 id={`${id}-h`} className="text-xl font-semibold tracking-tight">
          {title}
        </h3>
        {lede && <p className="max-w-[62ch] text-sm leading-relaxed text-muted">{lede}</p>}
      </div>
      {children}
    </section>
  )
}

/** Tone of a station node by kind. */
export const nodeTone: Record<NonNullable<Box['kind']> | 'step', string> = {
  step: 'border-accent/60 bg-surface text-accent dark:text-accent-bright',
  model: 'border-accent bg-accent text-white',
  guard: 'border-warn bg-warn-soft text-warn-ink',
  store: 'border-ok bg-ok-soft text-ok-ink',
  decision: 'border-accent/60 bg-surface text-accent dark:text-accent-bright',
}
