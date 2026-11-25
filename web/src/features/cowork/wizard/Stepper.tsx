// Progress through the wizard: one segment per step. Finished steps can be reopened; a step is open to you
// once every step before it is complete. On a phone the segments carry numbers only: the wizard prints "Step 2 of 5" under them.
import { Check } from 'lucide-react'
import { cx } from '@/lib/format'
import { STEP_LABEL, type StepId } from './steps'

export function Stepper({ steps, current, done, reachable, onGo }: { steps: StepId[]; current: StepId; done: (s: StepId) => boolean; reachable: (s: StepId) => boolean; onGo: (s: StepId) => void }) {
  const at = steps.indexOf(current)
  return (
    <nav aria-label="Steps of the new task" data-testid="stepper">
      <ol className="grid auto-cols-fr grid-flow-col gap-1.5 sm:gap-3">
        {steps.map((s, i) => {
          const isCurrent = s === current
          const isDone = i < at && done(s)
          const open = reachable(s)
          return (
            <li key={s} className="min-w-0">
              <button
                type="button"
                data-testid={`step-${s}`}
                data-state={isCurrent ? 'current' : isDone ? 'done' : 'todo'}
                aria-current={isCurrent ? 'step' : undefined}
                aria-disabled={!open || undefined}
                onClick={() => open && onGo(s)}
                className={cx('group flex w-full flex-col gap-2 rounded-lg text-left outline-offset-4', open ? 'cursor-pointer' : 'cursor-not-allowed')}
              >
                <span aria-hidden className={cx('h-1 w-full rounded-full transition-colors motion-reduce:transition-none', isCurrent || isDone ? 'bg-accent' : 'bg-line group-hover:bg-accent/30')} />
                <span className="flex items-center gap-2">
                  <span
                    aria-hidden
                    className={cx(
                      'grid size-5 shrink-0 place-items-center rounded-full text-[11px] font-semibold tabular-nums',
                      isDone ? 'bg-accent text-white' : isCurrent ? 'border-2 border-accent text-accent-ink' : 'border border-line text-muted',
                    )}
                  >
                    {isDone ? <Check size={12} strokeWidth={3} /> : i + 1}
                  </span>
                  <span className={cx('min-w-0 truncate text-sm', isCurrent ? 'font-semibold' : open ? 'text-fg' : 'text-muted', 'max-sm:sr-only')}>
                    {STEP_LABEL[s]}
                    {isDone && <span className="sr-only"> (done)</span>}
                  </span>
                </span>
              </button>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
