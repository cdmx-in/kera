import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ReactMarkdown from 'react-markdown'
import { describe, expect, it } from 'vitest'
import { mathPlugin, texDelimiters } from './math'

const html = (md: string) => renderToStaticMarkup(createElement(ReactMarkdown, { remarkPlugins: [mathPlugin] }, texDelimiters(md)))

describe('texDelimiters', () => {
  it('turns \\( \\) and \\[ \\] into the $$ form', () => {
    expect(texDelimiters('area \\(\\pi r^2\\) done')).toBe('area $$\\pi r^2$$ done')
    expect(texDelimiters('so \\[x = \\frac{1}{2}\\] ok')).toBe('so \n\n$$\nx = \\frac{1}{2}\n$$\n\n ok')
  })
  it('leaves citations, plain brackets and code alone', () => {
    expect(texDelimiters('see \\[1\\] and \\(note\\)')).toBe('see \\[1\\] and \\(note\\)')
    expect(texDelimiters('`\\(x^2\\)` and\n```\n\\[a=b\\]\n```')).toBe('`\\(x^2\\)` and\n```\n\\[a=b\\]\n```')
  })
})

describe('maths in markdown', () => {
  it('keeps money as text: a single dollar sign is not a formula', () => {
    const out = html('It cost $5 and then $10 more, so $15 in total.')
    expect(out).not.toContain('language-math')
    expect(out).toContain('$5 and then $10')
  })
  it('reads $$ as inline maths, and \\( \\) the same way', () => {
    expect(html('so $$x^2$$ holds')).toContain('language-math math-inline')
    expect(html('so \\(x^2\\) holds')).toContain('language-math math-inline')
  })
  it('reads a $$ block and \\[ \\] as display maths', () => {
    expect(html('$$\nx^2 + y^2\n$$')).toContain('language-math math-display')
    expect(html('\\[x = \\frac{a}{b}\\]')).toContain('language-math math-display')
  })
})
