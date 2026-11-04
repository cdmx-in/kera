import { useState } from 'react'
import { Collapsible } from 'radix-ui'
import { AlertCircle, Check, ChevronDown } from 'lucide-react'
import type { ToolStep } from '@/api/types'
import { uiConfig } from '@/lib/ui-config'
import { iconFor } from '@/lib/icons'
import { cx } from '@/lib/format'
import { Spinner } from '@/ui/misc'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { displayFor, summarizeSteps } from '@/features/tools/display'
import { FileChip } from './FileChip'

export function Steps({ steps }: { steps: ToolStep[] }) {
  const map = uiConfig().toolDisplay
  const running = steps.find((s) => s.status === 'running')
  const [open, setOpen] = useState(false)
  const [drawer, setDrawer] = useState<ToolStep | null>(null)
  const live = running ? displayFor(map, running.name, running.server) : null
  const liveLine = running?.statusLines.filter((l) => !l.done).at(-1) ?? running?.statusLines.at(-1)
  return (
    <Collapsible.Root open={open} onOpenChange={setOpen} className="my-2">
      <Collapsible.Trigger data-testid="step-summary" className="group inline-flex max-w-full items-center gap-2 rounded-lg py-1 text-sm text-muted hover:text-fg">
        {running ? <Spinner size={13} className="text-accent" /> : <Check size={14} className="text-accent" />}
        <span className="truncate">{running ? (liveLine?.description ?? `Running ${live?.label}…`) : summarizeSteps(steps, map)}</span>
        <ChevronDown size={14} className="shrink-0 transition-transform group-data-[state=open]:rotate-180" />
      </Collapsible.Trigger>
      <Collapsible.Content>
        <ol className="ml-[7px] mt-1 border-l border-line pl-4">
          {steps.map((s) => {
            const d = displayFor(map, s.name, s.server)
            const Icon = iconFor(d.icon, d.kind)
            return (
              <li key={s.callId} className="py-1">
                <button type="button" data-testid="step-item" data-status={s.status} onClick={() => setDrawer(s)} className="flex w-full items-start gap-2 rounded-lg px-1 py-0.5 text-left text-sm hover:bg-surface-2">
                  <Icon size={14} className="mt-0.5 shrink-0 text-muted" />
                  <span className="min-w-0 flex-1">
                    <span className="first-letter:uppercase">{d.label}</span>
                    {s.statusLines.map((l, i) => (
                      <span key={l.id ?? i} className={cx('block truncate text-xs', l.done ? 'text-muted' : 'text-fg')}>
                        {l.description}
                      </span>
                    ))}
                  </span>
                  {s.status === 'running' ? <Spinner size={12} className="mt-1 text-muted" /> : s.status === 'error' ? <AlertCircle size={14} className="mt-0.5 text-red-600" /> : null}
                </button>
              </li>
            )
          })}
        </ol>
      </Collapsible.Content>
      <StepDrawer step={drawer} onClose={() => setDrawer(null)} />
    </Collapsible.Root>
  )
}

function pretty(v: unknown): string {
  if (v == null || v === '') return ''
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  return JSON.stringify(v, null, 2)
}

function StepDrawer({ step, onClose }: { step: ToolStep | null; onClose: () => void }) {
  const d = step ? displayFor(uiConfig().toolDisplay, step.name, step.server) : null
  const args = pretty(step?.args)
  const out = pretty(step?.output)
  return (
    <Dialog open={!!step} onClose={onClose} drawer label="Step details" testId="step-drawer">
      {step && d && (
        <div className="flex h-full flex-col">
          <DialogHeader title={`View result from ${d.label}`} onClose={onClose} />
          <div className="flex-1 space-y-4 overflow-y-auto p-4 text-sm">
            <p className="text-xs text-muted">
              {step.server ? `${step.server} · ` : ''}
              {step.name}
            </p>
            {step.statusLines.length > 0 && (
              <ul className="space-y-1">
                {step.statusLines.map((l, i) => (
                  <li key={l.id ?? i}>{l.description}</li>
                ))}
              </ul>
            )}
            {step.urls && step.urls.length > 0 && (
              <ul className="space-y-1">
                {step.urls.map((u) => (
                  <li key={u} className="truncate">
                    <a href={u} target="_blank" rel="noreferrer" className="text-link underline">
                      {u}
                    </a>
                  </li>
                ))}
              </ul>
            )}
            {args && (
              <section>
                <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted">Input</h3>
                <pre className="max-h-64 overflow-auto rounded-lg bg-bg-soft p-3 text-xs">{args}</pre>
              </section>
            )}
            <section>
              <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted">Output</h3>
              {out ? <pre className="max-h-[50dvh] overflow-auto whitespace-pre-wrap rounded-lg bg-bg-soft p-3 text-xs">{out}</pre> : <p className="text-muted">{step.status === 'running' ? 'Still running…' : 'No output.'}</p>}
            </section>
            {step.files && step.files.length > 0 && (
              <div className="flex flex-wrap gap-2">
                {step.files.map((f) => (
                  <FileChip key={f.id} file={f} />
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </Dialog>
  )
}
