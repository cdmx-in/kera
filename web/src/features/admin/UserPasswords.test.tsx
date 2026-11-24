// Render checks for the Add user and Set password dialogs, the Change password section and the password box: static markup only.
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { PasswordInput } from '@/ui/PasswordInput'
import { ChangePassword } from '@/features/settings/ChangePassword'
import { AddUserDialog, SetPasswordDialog } from './UserPasswords'
import type { AdminUser } from './api'

const none = () => {}
const user: AdminUser = { id: 'u1', name: 'Ada Lovelace', email: 'ada@example.com', role: 'user', profile_image_url: '', last_active_at: 0, created_at: 0, groups: [], api_key_count: 0, disabled_at: null, origin: 'kera', has_password: false }

describe('PasswordInput', () => {
  const html = renderToStaticMarkup(<PasswordInput id="p" value="" onChange={none} autoComplete="new-password" className="x" />)
  it('is a password field with a show button next to it, not inside its label', () => {
    expect(html).toContain('type="password"')
    expect(html).toContain('id="p"')
    expect(html).toContain('autoComplete="new-password"')
    expect(html).toContain('aria-label="Show password"')
    expect(html).toContain('aria-pressed="false"')
  })
  it('shows the text when revealed (a generated password)', () => {
    const shown = renderToStaticMarkup(<PasswordInput id="p" value="abc" onChange={none} autoComplete="new-password" className="x" show onShowChange={none} />)
    expect(shown).toContain('type="text"')
    expect(shown).toContain('aria-label="Hide password"')
    expect(shown).toContain('aria-pressed="true"')
  })
  it('switches autocorrect off so a phone does not "fix" a password', () => {
    expect(html).toContain('autoCapitalize="none"')
    expect(html).toContain('spellCheck="false"')
  })
})

describe('AddUserDialog', () => {
  it('asks for email, name, role and an initial password, with one line of help each', () => {
    const html = renderToStaticMarkup(<AddUserDialog passwordRequired onClose={none} onAdded={none} />)
    for (const label of ['Email', 'Name (optional)', 'Role', 'Initial password']) expect(html).toContain(`>${label}</label>`)
    expect(html).toContain('They sign in with this address.')
    expect(html).toContain('Defaults to the part of the email before @.')
    expect(html).toContain('Admins manage users and settings.')
    expect(html).toContain('At least 10 characters.')
    expect(html).toContain('autoComplete="new-password"')
    expect(html).toContain('data-testid="generate-password"')
    expect(html).toContain('Add user')
  })
  it('makes the password optional where SSO is the way in', () => {
    const html = renderToStaticMarkup(<AddUserDialog passwordRequired={false} onClose={none} onAdded={none} />)
    expect(html).toContain('Initial password (optional)')
    expect(html).toContain('Leave empty if they sign in with SSO')
  })
})

describe('SetPasswordDialog', () => {
  it('names the user, warns that it signs them out, and never prefills a password', () => {
    const html = renderToStaticMarkup(<SetPasswordDialog user={user} onClose={none} onSaved={none} />)
    expect(html).toContain('Set password for Ada Lovelace')
    expect(html).toContain('ada@example.com is signed out everywhere')
    expect(html).toContain('You cannot read it again')
    expect(html).toContain('type="password"')
    expect(html).toContain('value=""')
  })
  it('says Reset when the user already has one', () => {
    expect(renderToStaticMarkup(<SetPasswordDialog user={{ ...user, has_password: true }} onClose={none} onSaved={none} />)).toContain('Reset password for Ada Lovelace')
  })
})

describe('ChangePassword', () => {
  const html = renderToStaticMarkup(<ChangePassword me={{ id: 'u1', name: 'Ada', email: 'ada@example.com', role: 'user' }} />)
  it('has the current and the new password with the right autocomplete values', () => {
    expect(html).toContain('>Current password</label>')
    expect(html).toContain('>New password</label>')
    expect(html).toContain('autoComplete="current-password"')
    expect(html).toContain('autoComplete="new-password"')
  })
  it('gives password managers the email as the username, out of sight and out of the tab order', () => {
    expect(html).toMatch(/<input[^>]*autoComplete="username"[^>]*value="ada@example.com"/)
    expect(html).toContain('tabindex="-1"')
    expect(html).toContain('sr-only')
  })
  it('says the other devices are signed out', () => {
    expect(html).toContain('Signs you out on your other devices.')
  })
})
