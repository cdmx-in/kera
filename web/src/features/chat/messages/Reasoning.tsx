import { Collapsible } from 'radix-ui'
import { ChevronDown } from 'lucide-react'
import type { Part } from '@/api/types'
import { Markdown } from '../markdown/Markdown'

export function Reasoning({ part }: { part: Extract<Part, { type: 'reasoning' }> }) {
  const secs = part.durationMs != null ? Math.max(1, Math.round(part.durationMs / 1000)) : null
  return (
    <Collapsible.Root className="my-2">
      <Collapsible.Trigger className="group inline-flex items-center gap-1.5 text-sm text-muted hover:text-fg">
        <span className={part.done ? '' : 'animate-pulse'}>{part.done ? (secs ? `Thought for ${secs} s` : 'Thought') : 'Thinking…'}</span>
        <ChevronDown size={14} className="transition-transform group-data-[state=open]:rotate-180" />
      </Collapsible.Trigger>
      <Collapsible.Content className="mt-1 border-l-2 border-line pl-3 text-sm text-muted">
        <Markdown text={part.text} />
      </Collapsible.Content>
    </Collapsible.Root>
  )
}
