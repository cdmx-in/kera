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

import { inlineHtmlToMarkdown, keepLineBreaks } from './Markdown'

describe('inlineHtmlToMarkdown', () => {
  it('maps allow-listed tags and drops ones without Markdown', () => {
    expect(inlineHtmlToMarkdown('Fill **<u>https://x.io/a</u>** <b>now</b><br>next')).toBe('Fill **https://x.io/a** **now**  \nnext')
    expect(inlineHtmlToMarkdown('<span style="color:red">red</span> <em>e</em> <del>x</del>')).toBe('red *e* ~~x~~')
  })
  it('leaves code and unknown tags alone', () => {
    expect(inlineHtmlToMarkdown('`<b>x</b>` and <script>alert(1)</script>')).toBe('`<b>x</b>` and <script>alert(1)</script>')
    expect(inlineHtmlToMarkdown('```\n<u>keep</u>\n```')).toBe('```\n<u>keep</u>\n```')
  })
})

describe('keepLineBreaks', () => {
  it('turns single newlines into hard breaks outside fences', () => {
    expect(keepLineBreaks('a\nb\n\nc')).toBe('a  \nb\n\nc')
    expect(keepLineBreaks('```\na\nb\n```')).toBe('```\na\nb\n```')
  })
})
