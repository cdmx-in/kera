import { describe, expect, it } from 'vitest'
import { ruleLabel } from './Guardrails'

describe('ruleLabel', () => {
  it('names rules for people', () => {
    expect(ruleLabel('pii.card')).toBe('Card number')
    expect(ruleLabel('output.pii.email')).toBe('Email address (reply)')
    expect(ruleLabel('moderation.violence/graphic')).toBe('Moderation: violence graphic')
    expect(ruleLabel('blocklist')).toBe('Blocklist')
    expect(ruleLabel('custom')).toBe('custom')
  })
})
