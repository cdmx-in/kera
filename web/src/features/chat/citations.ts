import type { Source } from '@/api/types'

// Passages the model cites by number carry that number as their id ("1", "2", ...).
// One numbering covers the system prompt and every tool call of a run, so [n]
// is resolved by id; several passages of one file are several entries. Sources
// without such ids (web results, messages saved before this) are cited by position.
const numbered = (s: Source) => /^\d+$/.test(s.id ?? '')

// identity of a source in a list: its number, else its link, file or name
export const sourceKey = (s: Source): string => (numbered(s) ? `cite:${s.id}` : (s.url ?? s.fileId ?? s.name ?? ''))

// the highest number [n] can refer to
export function citeCount(sources: Source[]): number {
  const ids = sources.filter(numbered).map((s) => Number(s.id))
  return ids.length ? Math.max(...ids) : sources.length
}

// the source [n] cites, if the list has one
export function citeSource(sources: Source[], n: number): Source | undefined {
  return sources.some(numbered) ? sources.find((s) => s.id === String(n)) : sources[n - 1]
}
