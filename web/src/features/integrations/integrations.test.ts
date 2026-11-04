import { describe, expect, it } from 'vitest'
import { installState } from './extension'
import { fillPlaceholders } from './ExtensionDialog'
import { authorizeUrl } from './oauth'

describe('integrations', () => {
  it('derives the install button state from the marker', () => {
    expect(installState(null, '1.3.5')).toBe('install')
    expect(installState('', '1.3.5')).toBe('update')
    expect(installState('1.3.4', '1.3.5')).toBe('update')
    expect(installState('1.3.5', '1.3.5')).toBe('current')
    expect(installState('1.4.0', '1.3.5')).toBe('current')
  })
  it('fills step placeholders and keeps unknown ones', () => {
    expect(fillPlaceholders('v{{version}} {{ folder }} {{nope}}', { version: '1.2', folder: 'ext-1.2' })).toBe('v1.2 ext-1.2 {{nope}}')
  })
  it('builds the authorize URL with a return to ?tools=', () => {
    expect(authorizeUrl({ id: 'sheets', kind: 'oauthToolServer', toolServerId: 'mcp:sheets', label: 'Sheets' })).toBe('/oauth/clients/mcp:sheets/authorize?next=%2F%3Ftools%3Dmcp%3Asheets')
  })
})
