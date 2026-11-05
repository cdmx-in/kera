// A small note from the server about this reply, e.g. what the guardrails redacted.
import { ShieldCheck } from 'lucide-react'
import type { Block } from '@/api/types'

export default function Notice({ block }: { block: Block }) {
  const text = typeof block.text === 'string' ? block.text : ''
  if (!text) return null
  return (
    <p className="my-2 inline-flex items-center gap-1.5 rounded-full bg-surface-2 px-2.5 py-0.5 text-xs text-muted" data-testid="notice" data-kind={typeof block.kind === 'string' ? block.kind : undefined}>
      <ShieldCheck size={13} className="shrink-0 text-accent" />
      {text}
    </p>
  )
}
