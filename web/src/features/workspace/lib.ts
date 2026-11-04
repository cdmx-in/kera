// Pure helpers for the workspace lists (unit-tested in lib.test.ts).
import type { IngestStatus } from './api'

/** One card on the Models page: a preset, a base model with settings, or a discovered model without a row. */
export interface ModelCardData {
  id: string
  name: string
  description: string
  preset: boolean
  local: boolean
  hidden: boolean
}

export const MODEL_FILTERS = [
  ['all', 'All'],
  ['assistants', 'Assistants'],
  ['base', 'Base models'],
  ['local', 'On server'],
  ['cloud', 'Cloud'],
  ['hidden', 'Hidden'],
] as const
export type ModelFilter = (typeof MODEL_FILTERS)[number][0]

/** Matches every word of q against id, name and description, then applies the chip filter. */
export function filterModels<T extends ModelCardData>(items: T[], q: string, filter: ModelFilter): T[] {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean)
  return items.filter((m) => {
    const hay = `${m.id} ${m.name} ${m.description}`.toLowerCase()
    if (!words.every((w) => hay.includes(w))) return false
    switch (filter) {
      case 'assistants':
        return m.preset
      case 'base':
        return !m.preset
      case 'local':
        return m.local
      case 'cloud':
        return !m.local
      case 'hidden':
        return m.hidden
      default:
        return true
    }
  })
}

export type ChipTone = 'green' | 'amber' | 'red' | 'grey'

/** Status chips for a collection: "82 indexed", "3 indexing", "2 failed"; "No files" when empty. */
export function statusChips(s?: Pick<IngestStatus, 'files' | 'completed' | 'pending' | 'processing' | 'failed'>): { label: string; tone: ChipTone }[] {
  if (!s || !s.files) return [{ label: 'No files', tone: 'grey' }]
  const out: { label: string; tone: ChipTone }[] = []
  if (s.completed) out.push({ label: `${s.completed} indexed`, tone: 'green' })
  if (s.pending + s.processing) out.push({ label: `${s.pending + s.processing} indexing`, tone: 'amber' })
  if (s.failed) out.push({ label: `${s.failed} failed`, tone: 'red' })
  return out
}

/** Free-text search over prompts (command, title, content). */
export function filterPrompts<T extends { command: string; title: string; content: string }>(items: T[], q: string): T[] {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean)
  return items.filter((p) => {
    const hay = `${p.command} ${p.title} ${p.content}`.toLowerCase()
    return words.every((w) => hay.includes(w))
  })
}
