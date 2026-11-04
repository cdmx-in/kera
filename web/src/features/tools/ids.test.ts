import { describe, expect, it } from 'vitest'
import { toolKey } from './ids'

describe('toolKey', () => {
  it('drops the server: prefix only', () => {
    expect(toolKey('server:docs')).toBe('docs')
    expect(toolKey('mcp:sheets')).toBe('mcp:sheets')
    expect(toolKey('builtin:ask_user')).toBe('builtin:ask_user')
  })
})
