import { describe, expect, it } from 'vitest'
import { brandFile, greyScale, hexColor, isCssColor, previewVars, pruneBrand } from './brand'

describe('brand helpers', () => {
  it('builds the grey ladder like the server', () => {
    const s = greyScale(260, 0.02)
    expect(s).toHaveLength(11)
    expect(s[0]).toBe('oklch(0.985 0.005 260)')
    expect(s[3]).toBe('oklch(0.87 0.02 260)')
    expect(s[9]).toBe('oklch(0.215 0.0167 260)')
  })

  it('checks colours and feeds the colour input', () => {
    expect(isCssColor('#1e7f8c')).toBe(true)
    expect(isCssColor('oklch(0.7 0.1 200)')).toBe(true)
    expect(isCssColor('red}</style>')).toBe(false)
    expect(hexColor('#ABC')).toBe('#aabbcc')
    expect(hexColor('oklch(0.7 0.1 200)', '#123456')).toBe('#123456')
  })

  it('previews only valid tokens and redeclares surfaces', () => {
    const v = previewVars({ name: 'x', accent: { base: 'bad;', bright: '#3dafc4' }, grey: { hue: 260, chroma: 0.02 } }, true)
    expect(v['--accent']).toBeUndefined()
    expect(v['--accent-bright']).toBe('#3dafc4')
    expect(v['--gray-950']).toBe('oklch(0.16 0.0133 260)')
    expect(v['--bg']).toBe('var(--gray-950)')
    expect(previewVars({ name: 'x' }, false)['--gray-50']).toBeUndefined()
  })

  it('prunes empty values', () => {
    expect(pruneBrand({ name: 'A', mark: '', logo: { light: '', dark: '' }, faviconAlt: [], splash: { wordmark: 'W', style: undefined }, grey: { hue: 0, chroma: 0.01 } })).toEqual({
      name: 'A',
      splash: { wordmark: 'W' },
      grey: { hue: 0, chroma: 0.01 },
    })
  })

  it('names brand files', () => {
    expect(brandFile('/brand/logo-1a2b3c4d.png?v=abc')).toBe('logo-1a2b3c4d.png')
    expect(brandFile('https://x/y.png')).toBe('')
  })
})
