// Source guards for the findings of the accessibility audit of the sign-in and chat pages. They look at the
// class strings and tags that axe flagged, because the repository has no browser in its tests; the audit itself
// (axe in a real browser, light and dark) is described in the pull request.
import { describe, expect, it } from 'vitest'

const raw = import.meta.glob<string>('/src/**/*.tsx', { query: '?raw', import: 'default', eager: true })
const sources = Object.entries(raw)
  .filter(([f]) => !/\.test\./.test(f))
  .map(([f, text]) => ({ f: f.replace('/src/', ''), text }))

describe('accessibility guards', () => {
  it('gives every popover (a dialog for screen readers) an accessible name', () => {
    const bad = sources.flatMap(({ f, text }) => [...text.matchAll(/<Popover\.Content\b[^>]*>/g)].filter((m) => !m[0].includes('aria-label')).map(() => f))
    expect(bad).toEqual([])
  })
  it('does not put text-red-600 on the red tint (4.1:1 in light mode, AA needs 4.5:1): use text-red-700', () => {
    const bad = sources.flatMap(({ f, text }) => [...text.matchAll(/(['"`])((?:(?!\1).)*)\1/g)].filter((m) => m[2].includes('bg-red-500/10') && m[2].includes('text-red-600')).map(() => f))
    expect(bad).toEqual([])
  })
  it('does not put accent text on the soft accent background where text-accent-ink is meant (4.1:1 in light mode)', () => {
    // the model picker and the start screen are the chat page's own uses
    const bad = ['features/header/ModelPicker.tsx', 'features/chat/EmptyState.tsx'].filter((f) => /bg-accent-soft[^'"`]*text-accent(?!-ink)\b/.test(sources.find((s) => s.f === f)?.text ?? ''))
    expect(bad).toEqual([])
  })
})
