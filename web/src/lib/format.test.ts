import { expect, test, vi } from 'vitest'
import { prettyFileName } from './format'

test('prettyFileName', () => {
  expect(prettyFileName('domain--portals-and-modules.md')).toBe('Domain – Portals and modules')
  expect(prettyFileName('reference--management-portal-user-manual.md')).toBe('Reference – Management portal user manual')
  expect(prettyFileName('HR_Policy v2.pdf')).toBe('HR Policy v2')
  expect(prettyFileName('.md')).toBe('.md')
})

test('copyText reports a blocked clipboard instead of claiming success', async () => {
  const { copyText } = await import('./format')
  const { useToasts } = await import('@/stores/toast')
  vi.stubGlobal('navigator', { clipboard: { writeText: () => Promise.reject(new Error('denied')) } })
  expect(await copyText('x', 'blocked')).toBe(false)
  expect(useToasts.getState().toasts.at(-1)).toMatchObject({ text: 'blocked', kind: 'error' })
  vi.stubGlobal('navigator', { clipboard: { writeText: () => Promise.resolve() } })
  expect(await copyText('x')).toBe(true)
  vi.stubGlobal('navigator', {}) // no clipboard API at all (plain http)
  expect(await copyText('x')).toBe(false)
  vi.unstubAllGlobals()
})
