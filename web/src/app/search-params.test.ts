import { describe, expect, it } from 'vitest'
import { parseChatParams, safeRedirect, withoutOneShot } from './search-params'

describe('parseChatParams', () => {
  it('reads every field', () => {
    expect(parseChatParams('?models=kera,local&q=hello%20there&temporary-chat=true&tools=mcp:google-sheets')).toEqual({
      models: ['kera', 'local'],
      q: 'hello there',
      temporary: true,
      tools: ['mcp:google-sheets'],
    })
  })
  it('defaults when empty', () => {
    expect(parseChatParams('')).toEqual({ models: [], q: '', temporary: false, tools: [] })
  })
  it('accepts repeated params and ignores blanks', () => {
    expect(parseChatParams('?models=a&models=b,,&tools=').models).toEqual(['a', 'b'])
  })
  it('treats other temporary values as false', () => {
    expect(parseChatParams('?temporary-chat=false').temporary).toBe(false)
    expect(parseChatParams('?temporary-chat=TRUE').temporary).toBe(true)
  })
  it('strips one-shot params', () => {
    expect(withoutOneShot('?models=a&q=x&tools=t')).toBe('?models=a')
    expect(withoutOneShot('?q=x')).toBe('')
    expect(withoutOneShot('?oauth_error=provider_denied&models=a')).toBe('?models=a')
  })
})

describe('safeRedirect', () => {
  it('keeps same-origin paths', () => {
    expect(safeRedirect('/c/abc?models=x')).toBe('/c/abc?models=x')
    expect(safeRedirect('/authors')).toBe('/authors')
  })
  it('rejects anything that could leave the origin or loop', () => {
    for (const v of [null, '', 'https://evil.example', '//evil.example', '/\\evil.example', '/\t/evil.example', 'javascript:alert(1)', '/auth', '/auth?redirect=/x'])
      expect(safeRedirect(v)).toBe('/')
  })
})
