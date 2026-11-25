import { describe, expect, it } from 'vitest'
import type { Source } from '@/api/types'
import { citeCount, citeSource, sourceKey } from './citations'
import { linkCitations } from './markdown/Markdown'

// passages 1 and 2 are from the same file, 4 came from a later search
const numbered: Source[] = [
  { id: '1', fileId: 'a', name: 'a.md' },
  { id: '2', fileId: 'a', name: 'a.md' },
  { id: '4', fileId: 'b', name: 'b.md' },
]

describe('citations', () => {
  it('resolves [n] by the source number, not by position', () => {
    expect(citeSource(numbered, 2)?.id).toBe('2')
    expect(citeSource(numbered, 4)?.fileId).toBe('b')
    expect(citeSource(numbered, 3)).toBeUndefined() // a gap is not linked to the wrong file
    expect(citeCount(numbered)).toBe(4)
    expect(linkCitations('x [3] y [4] z [5]', citeCount(numbered))).toBe('x [3](#cite-3) y [4](#cite-4) z [5]')
  })
  it('keeps the position rule for sources without numbers', () => {
    const web: Source[] = [{ url: 'https://x.example/a' }, { url: 'https://x.example/b' }]
    expect(citeSource(web, 2)?.url).toBe('https://x.example/b')
    expect(citeSource(web, 3)).toBeUndefined()
    expect(citeCount(web)).toBe(2)
  })
  it('lists two passages of one file as two sources', () => {
    expect(sourceKey(numbered[0])).not.toBe(sourceKey(numbered[1]))
    expect(sourceKey({ url: 'https://x.example/a' })).toBe('https://x.example/a')
  })
})
