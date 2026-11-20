import { describe, expect, it } from 'vitest'
import { displayName } from './labels'

describe('displayName', () => {
  it('keeps product names and sentence-cases the rest', () => {
    expect(displayName('litellm')).toBe('LiteLLM')
    expect(displayName('litellm-web')).toBe('LiteLLM web')
    expect(displayName('mcp:google-sheets')).toBe('MCP Google Sheets')
    expect(displayName('kera-web')).toBe('Kera web')
    expect(displayName('opensearch')).toBe('OpenSearch')
  })
  it('prefers an explicit name', () => {
    expect(displayName('x', ' Team KB facts ')).toBe('Team KB facts')
  })
})
