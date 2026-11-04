// Small building blocks shared by the admin pages.
import { useState, type ReactNode } from 'react'
import { FileLock2 } from 'lucide-react'
import { cx } from '@/lib/format'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Spinner } from '@/ui/misc'
import { toast } from '@/stores/toast'

export const inputCls = 'w-full min-w-0 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm outline-none focus:border-accent disabled:opacity-60'
/** inputCls sized to its content (for selects and dates in rows). */
export const inputAuto = inputCls.replace('w-full ', 'w-auto ')
export const thCls = 'whitespace-nowrap px-3 py-2 text-left text-xs font-medium text-muted'
export const tdCls = 'px-3 py-2 align-middle'
/** Table scroll area inside a card: grows with its rows up to about a screen, then scrolls under a sticky header. */
export const scrollCls = 'max-h-[max(20rem,calc(100dvh-15rem))] overflow-auto'
/** Sticky <thead> for tables inside scrollCls (the inset shadow is the bottom border, which sticky cells lose). */
export const theadCls = 'sticky top-0 z-10 bg-surface shadow-[inset_0_-1px_0_var(--color-line)]'

export function Btn({ variant = 'secondary', className, ...rest }: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'danger' | 'ghost' }) {
  return (
    <button
      type="button"
      className={cx(
        'inline-flex items-center justify-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium disabled:opacity-50',
        variant === 'primary' && 'bg-accent text-white hover:opacity-90',
        variant === 'secondary' && 'border border-line bg-surface hover:bg-surface-2',
        variant === 'danger' && 'bg-red-600 text-white hover:bg-red-700',
        variant === 'ghost' && 'text-muted hover:bg-surface-2 hover:text-fg',
        className,
      )}
      {...rest}
    />
  )
}

export function Badge({ tone = 'grey', children, title }: { tone?: 'grey' | 'accent' | 'red' | 'amber' | 'green'; children: ReactNode; title?: string }) {
  const tones = {
    grey: 'bg-surface-2 text-muted',
    accent: 'bg-accent-soft text-accent dark:text-accent-bright',
    red: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
    amber: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
    green: 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300',
  }
  return (
    <span title={title} className={cx('inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2 py-0.5 text-[11px] font-medium', tones[tone])}>
      {children}
    </span>
  )
}

/** Marks a row or key that `kera apply` owns. compact = icon only (settings rows). */
export function ManagedBadge({ compact }: { compact?: boolean }) {
  const title = 'Managed by the config file: kera apply overwrites changes made here'
  if (compact)
    return (
      <span title={title} aria-label="Managed by the config file" className="inline-flex text-accent/70 dark:text-accent-bright/70">
        <FileLock2 size={13} />
      </span>
    )
  return (
    <Badge tone="accent" title={title}>
      <FileLock2 size={11} /> Config file
    </Badge>
  )
}

export const managedWarning = 'Managed by the config file: the next kera apply overwrites this change.'

export function Page({ title, actions, children }: { title?: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <div className="mx-auto w-full max-w-[100rem] space-y-5 p-4 sm:p-6 lg:px-8">
      {(title || actions) && (
        <div className="flex flex-wrap items-center gap-3">
          {title && <h2 className="flex-1 text-lg font-semibold">{title}</h2>}
          {actions}
        </div>
      )}
      {children}
    </div>
  )
}

export function Card({ title, hint, actions, children, className }: { title?: ReactNode; hint?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx('overflow-hidden rounded-2xl border border-line bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)]', className)}>
      {(title || actions) && (
        <header className="flex flex-wrap items-center gap-2 border-b border-line bg-surface-2/40 px-5 py-3.5">
          <div className="min-w-0 flex-1">
            {title && <h3 className="text-[15px] font-semibold tracking-tight">{title}</h3>}
            {hint && <p className="mt-0.5 text-xs leading-relaxed text-muted">{hint}</p>}
          </div>
          {actions}
        </header>
      )}
      {children}
    </section>
  )
}

/** A labelled field; error (inline validation or a field-specific API error) replaces the hint. */
export function Field({ label, hint, error, children, className }: { label: ReactNode; hint?: ReactNode; error?: string; children: ReactNode; className?: string }) {
  return (
    <label className={cx('block space-y-1', className)}>
      <span className="block text-xs font-medium text-muted">{label}</span>
      {children}
      {error ? (
        <span role="alert" className="block text-xs text-red-600 dark:text-red-400" data-testid="field-error">
          {error}
        </span>
      ) : (
        hint && <span className="block text-xs text-muted">{hint}</span>
      )}
    </label>
  )
}

export function Loading({ q }: { q: { isLoading: boolean; error: unknown } }) {
  if (q.isLoading)
    return (
      <div className="grid place-items-center p-10 text-muted">
        <Spinner size={20} />
      </div>
    )
  if (q.error) return <p className="rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300">{(q.error as Error).message}</p>
  return null
}

export function Kpi({ label, value, hint }: { label: string; value: ReactNode; hint?: string }) {
  return (
    <div className="rounded-xl border border-line bg-surface p-4" data-testid="kpi">
      <div className="text-2xl font-semibold tabular-nums">{value}</div>
      <div className="text-xs text-muted">{label}</div>
      {hint && <div className="mt-1 text-xs text-muted">{hint}</div>}
    </div>
  )
}

/** Horizontal CSS bar for table cells, width = value/max. */
export function Bar({ value, max }: { value: number; max: number }) {
  return (
    <div className="h-1.5 w-28 rounded-full bg-accent-soft" aria-hidden>
      <i className="block h-full rounded-full bg-accent" style={{ width: `${max > 0 ? (100 * value) / max : 0}%` }} />
    </div>
  )
}

/** Vertical CSS bars, one per day. */
export function DayBars({ days, format }: { days: { day: string; value: number }[]; format: (v: number) => string }) {
  const max = Math.max(0, ...days.map((d) => d.value)) || 1
  if (!days.length) return <p className="rounded-xl border border-line bg-surface p-4 text-sm text-muted">No data</p>
  return (
    <div className="flex h-28 items-end gap-0.5 rounded-xl border border-line bg-surface p-3" data-testid="day-bars">
      {days.map((d) => (
        <i key={d.day} title={`${d.day}: ${format(d.value)}`} className="min-h-px flex-1 rounded-t-sm bg-accent" style={{ height: `${(100 * d.value) / max}%` }} />
      ))}
    </div>
  )
}

export function DateRange({ value, onChange }: { value: [string, string]; onChange: (v: [string, string]) => void }) {
  const [draft, setDraft] = useState(value)
  return (
    <form
      className="flex flex-wrap items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        onChange(draft)
      }}
    >
      <input type="date" aria-label="Start date" required value={draft[0]} max={draft[1]} onChange={(e) => setDraft([e.target.value, draft[1]])} className={inputAuto} />
      <span className="text-muted">to</span>
      <input type="date" aria-label="End date" required value={draft[1]} min={draft[0]} onChange={(e) => setDraft([draft[0], e.target.value])} className={inputAuto} />
      <Btn type="submit" variant="primary">
        Apply
      </Btn>
    </form>
  )
}

/** Native confirm dialog. */
export function Confirm({ open, title, body, confirmLabel = 'Delete', onConfirm, onClose }: { open: boolean; title: string; body: ReactNode; confirmLabel?: string; onConfirm: () => Promise<void> | void; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  return (
    <Dialog open={open} onClose={onClose} label={title} testId="confirm-dialog" className="w-[min(28rem,calc(100vw-2rem))]">
      <DialogHeader title={title} onClose={onClose} />
      <div className="space-y-4 p-4 text-sm">
        <div>{body}</div>
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose} data-testid="cancel">
            Cancel
          </Btn>
          <Btn
            variant="danger"
            data-testid="confirm"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              try {
                await onConfirm()
                onClose()
              } catch (e) {
                toast((e as Error).message, 'error')
              } finally {
                setBusy(false)
              }
            }}
          >
            {confirmLabel}
          </Btn>
        </div>
      </div>
    </Dialog>
  )
}

/** Runs fn, toasting success or the error; returns whether it succeeded. */
export async function attempt(fn: () => Promise<unknown>, ok?: string): Promise<boolean> {
  try {
    await fn()
    if (ok) toast(ok)
    return true
  } catch (e) {
    toast((e as Error).message, 'error')
    return false
  }
}

/** Write-only secret input: empty keeps the stored value; "Clear" removes it. */
export function SecretInput({ has, value, onChange, label, error }: { has: boolean; value: string | null; onChange: (v: string | null) => void; label: string; error?: string }) {
  const cleared = value === ''
  return (
    <div className="flex gap-2">
      <input
        type="password"
        autoComplete="new-password"
        aria-label={label}
        value={value ?? ''}
        placeholder={cleared ? 'Will be removed on save' : has ? '•••••••• stored (type to replace)' : 'Not set'}
        onChange={(e) => onChange(e.target.value || null)}
        aria-invalid={!!error}
        className={inputCls + (error ? ' border-red-500! focus:border-red-500!' : '')}
      />
      {has && (
        <Btn variant="ghost" title={cleared ? 'Keep the stored value' : 'Remove the stored value on save'} onClick={() => onChange(cleared ? null : '')}>
          {cleared ? 'Keep' : 'Clear'}
        </Btn>
      )}
    </div>
  )
}

export function Check({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: ReactNode }) {
  return (
    <label className="inline-flex items-center gap-2 text-sm">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="size-4 accent-[var(--accent)]" />
      {children}
    </label>
  )
}
