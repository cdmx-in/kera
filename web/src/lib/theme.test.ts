import { afterEach, describe, expect, it, vi } from 'vitest'
import { getTheme } from './theme'
import { resetUIConfig } from './ui-config'

function withStorage(theme: string | null) {
  vi.stubGlobal('localStorage', { getItem: () => theme, setItem: () => undefined })
}

describe('theme', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    resetUIConfig()
  })
  it('prefers the stored choice, mapping legacy dark variants', () => {
    vi.stubGlobal('window', { __UI__: { brand: { name: 'X', defaultTheme: 'dark' } } })
    withStorage('light')
    expect(getTheme()).toBe('light')
    withStorage('system')
    expect(getTheme()).toBe('system')
    withStorage('oled-dark')
    expect(getTheme()).toBe('dark')
  })
  it('falls back to the brand default', () => {
    vi.stubGlobal('window', { __UI__: { brand: { name: 'X', defaultTheme: 'dark' } } })
    withStorage(null)
    expect(getTheme()).toBe('dark')
    resetUIConfig()
    vi.stubGlobal('window', {})
    expect(getTheme()).toBe('system')
  })
})
