// A boxed message with an icon and optional actions: the notices of the pipeline pages.
import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { cx } from '@/lib/format'

// Tones come from the status tokens (app.css), so they follow the app's light/dark switch, not only the OS scheme.
const noticeTone = {
  red: 'border-bad/30 bg-bad-soft text-bad-ink',
  amber: 'border-warn/35 bg-warn-soft text-warn-ink',
  accent: 'border-accent/30 bg-accent-soft',
  grey: 'border-line bg-surface-2/50 text-muted',
}

export function Notice({ tone, icon: Icon, actions, role, testId, children }: { tone: keyof typeof noticeTone; icon?: LucideIcon; actions?: ReactNode; role?: 'alert' | 'status'; testId?: string; children: ReactNode }) {
  return (
    <div role={role} data-testid={testId} className={cx('flex flex-wrap items-start gap-x-3 gap-y-2 rounded-xl border px-4 py-3 text-sm', noticeTone[tone])}>
      {Icon && <Icon size={16} className="mt-0.5 shrink-0" aria-hidden />}
      {/* the text keeps a readable width, so the actions wrap under it on a phone; text-fg because a Btn sets only its (app theme) background, and the tone colours come from the status tokens */}
      <div className="min-w-[min(100%,14rem)] flex-1">{children}</div>
      {actions && <div className="text-fg">{actions}</div>}
    </div>
  )
}
