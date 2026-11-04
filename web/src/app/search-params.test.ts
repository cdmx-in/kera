import { describe, expect, it } from 'vitest'
import { parseChatParams, withoutOneShot } from './search-params'

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
  })
})
