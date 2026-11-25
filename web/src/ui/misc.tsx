import { Switch as RSwitch, Tooltip } from 'radix-ui'
import type { ReactNode } from 'react'
import { useToasts } from '@/stores/toast'
import { cx } from '@/lib/format'

export function Spinner({ size = 14, className }: { size?: number; className?: string }) {
  return <span role="status" aria-label="Loading" className={cx('inline-block animate-spin rounded-full border-2 border-current border-t-transparent', className)} style={{ width: size, height: size }} />
}

export function Switch({ checked, onChange, label, testId, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; testId?: string; disabled?: boolean }) {
  return (
    <RSwitch.Root
      checked={checked}
      onCheckedChange={onChange}
      disabled={disabled}
      aria-label={label}
      data-testid={testId}
      className="relative h-5 w-9 shrink-0 rounded-full bg-gray-300 transition-colors data-[state=checked]:bg-accent disabled:opacity-50"
    >
      <RSwitch.Thumb className="block size-4 translate-x-0.5 rounded-full bg-white shadow transition-transform data-[state=checked]:translate-x-[18px]" />
    </RSwitch.Root>
  )
}

/** Tooltip for elements outside dialogs (Radix portals sit below a modal's top layer). */
export function Tip({ text, children, side = 'bottom' }: { text: ReactNode; children: ReactNode; side?: 'top' | 'bottom' | 'left' | 'right' }) {
  return (
    <Tooltip.Root>
      <Tooltip.Trigger asChild>{children}</Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content side={side} sideOffset={6} className="z-50 max-w-64 rounded-md bg-gray-900 px-2 py-1 text-xs text-white shadow">
          {text}
        </Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip.Root>
  )
}

export function Toaster() {
  const toasts = useToasts((s) => s.toasts)
  const dismiss = useToasts((s) => s.dismiss)
  return (
    <div aria-live="polite" className="pointer-events-none fixed inset-x-0 bottom-4 z-[60] flex flex-col items-center gap-2 px-4">
      {toasts.map((t) => (
        <button
          key={t.id}
          type="button"
          onClick={() => dismiss(t.id)}
          className={cx('pointer-events-auto max-w-md rounded-xl px-4 py-2 text-left text-sm shadow-pop', t.kind === 'error' ? 'bg-red-600 text-white' : 'bg-gray-900 text-white')}
        >
          {t.text}
        </button>
      ))}
    </div>
  )
}

/** A page that failed to load for a reason other than "not found" (network, server error): say so and offer a reload. */
export function LoadFailed({ what }: { what: string }) {
  return (
    <div role="alert" className="grid flex-1 place-items-center px-4 text-center text-muted">
      <p>
        Could not load this {what}. Check your connection and try again.{' '}
        <button type="button" className="text-link underline" onClick={() => location.reload()}>
          Reload
        </button>
      </p>
    </div>
  )
}

/** Load errors that mean "not there for you": the page says so instead of offering a retry. */
export const isMissing = (e: unknown) => typeof e === 'object' && e !== null && [400, 403, 404].includes((e as { status?: number }).status ?? 0)
