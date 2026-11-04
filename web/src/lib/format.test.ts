import { expect, test } from 'vitest'
import { prettyFileName } from './format'

test('prettyFileName', () => {
  expect(prettyFileName('domain--portals-and-modules.md')).toBe('Domain – Portals and modules')
  expect(prettyFileName('reference--management-portal-user-manual.md')).toBe('Reference – Management portal user manual')
  expect(prettyFileName('HR_Policy v2.pdf')).toBe('HR Policy v2')
  expect(prettyFileName('.md')).toBe('.md')
})
