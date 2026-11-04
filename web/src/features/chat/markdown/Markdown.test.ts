import { describe, expect, it } from 'vitest'
import { linkCitations } from './Markdown'

describe('linkCitations', () => {
  it('links markers in range, outside code', () => {
    expect(linkCitations('A [1] and [2], not [9].', 2)).toBe('A [1](#cite-1) and [2](#cite-2), not [9].')
    expect(linkCitations('`x[1]` ```\na[1]\n``` b[1]', 1)).toBe('`x[1]` ```\na[1]\n``` b[1](#cite-1)')
    expect(linkCitations('[1](http://x) [text][1]\n[1]: http://y', 1)).toBe('[1](http://x) [text][1]\n[1]: http://y')
    expect(linkCitations('the guide [1]:', 1)).toBe('the guide [1](#cite-1):')
  })
  it('leaves text alone without sources', () => {
    expect(linkCitations('a [1]', 0)).toBe('a [1]')
  })
})
