// Friendly names for tool steps. The server's toolDisplay map wins; otherwise
// the function name is humanised ("docs_searchPagesTool" -> "search pages").

import type { ToolDisplay, ToolKind } from '@/lib/ui-config'
import type { ToolStep } from '@/api/types'

export function humanizeTool(name: string, server?: string): string {
  let n = name
  if (server && n.toLowerCase().startsWith(server.toLowerCase() + '_')) n = n.slice(server.length + 1)
  n = n.replace(/Tool$/, '').replace(/_tool$/i, '')
  const words = n
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/([A-Z]+)([A-Z][a-z])/g, '$1 $2')
    .split(/[\s_\-.:]+/)
    .filter(Boolean)
    .map((w) => (/^[A-Z0-9]+$/.test(w) && w.length > 1 ? w : w.toLowerCase()))
  return words.join(' ') || name
}

/** Pluralises the last word of a label ("web search" -> "web searches"). */
export function pluralize(label: string): string {
  const m = label.match(/^(.*?)(\w+)$/)
  if (!m) return label
  const [, head, w] = m
  let p: string
  if (/(s|x|z|ch|sh)$/i.test(w)) p = w + 'es'
  else if (/[^aeiou]y$/i.test(w)) p = w.slice(0, -1) + 'ies'
  else p = w + 's'
  return head + p
}

export function displayFor(map: Record<string, ToolDisplay>, name: string, server?: string): Required<Pick<ToolDisplay, 'label' | 'kind'>> & ToolDisplay {
  // a tool server's own name for its function ("<server>_<fn>") wins over the bare name
  const d = (server ? map[`${server}_${name}`] : undefined) ?? map[name]
  return { kind: 'other', ...d, label: d?.label ?? humanizeTool(name, server) }
}

export function countLabel(n: number, d: ToolDisplay): string {
  return `${n} ${n === 1 ? d.label : d.plural ?? pluralize(d.label)}`
}

const verbs: Record<ToolKind, string> = {
  search: 'Explored',
  read: 'Explored',
  code: 'Executed',
  ask: 'Asked',
  write: 'Updated',
  memory: 'Remembered',
  other: 'Used',
}

export function verbFor(kind: ToolKind | undefined): string {
  return verbs[kind ?? 'other'] ?? verbs.other
}

/**
 * Header for a group of consecutive steps: "Explored 2 web searches, 1 page read".
 * Kinds sharing a verb are listed together in first-seen order; different verbs
 * are joined with "; " ("Explored 1 web search; executed 1 code run").
 */
export function summarizeSteps(steps: Pick<ToolStep, 'name' | 'server'>[], map: Record<string, ToolDisplay>): string {
  const byVerb = new Map<string, Map<string, { n: number; d: ToolDisplay }>>()
  for (const s of steps) {
    const d = displayFor(map, s.name, s.server)
    const verb = verbFor(d.kind)
    const group = byVerb.get(verb) ?? new Map()
    const e = group.get(d.label) ?? { n: 0, d }
    e.n++
    group.set(d.label, e)
    byVerb.set(verb, group)
  }
  return [...byVerb.entries()]
    .map(([verb, g], i) => {
      const items = [...g.values()].map((e) => countLabel(e.n, e.d)).join(', ')
      return `${i === 0 ? verb : verb.toLowerCase()} ${items}`
    })
    .join('; ')
}
