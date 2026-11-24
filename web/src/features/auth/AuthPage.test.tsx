// Render checks for the sign-in methods: static markup only (no browser).
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { SignInForm, SignInMethods, signInMessage } from './AuthPage'

const google = { id: 'google', name: 'Google' }
const none = () => {}
const methods = (providers: { id: string; name: string }[], form: boolean, glass = false) =>
  renderToStaticMarkup(<SignInMethods options={{ providers, login_form: form }} glass={glass} redirect="/c/abc" onSignedIn={none} />)

describe('SignInMethods', () => {
  it('shows only the SSO button when the form is off (an SSO-only install looks as before)', () => {
    const html = methods([google], false)
    expect(html).not.toContain('<form')
    expect(html).not.toContain('or-divider')
    expect(html).toContain('href="/oauth/google/login?next=%2Fc%2Fabc"')
    expect(html).toContain('Continue with Google')
    expect(html).toContain('data-testid="google-login"')
    expect(html).toContain('mt-8') // the old spacing under the heading
  })
  it('shows only the form, with no divider and no dead Google button, on a community install', () => {
    const html = methods([], true)
    expect(html).toContain('data-testid="password-form"')
    expect(html).not.toContain('or-divider')
    expect(html).not.toContain('Continue with')
  })
  it('puts a quiet "or" between the form and the buttons, form first', () => {
    const html = methods([google, { id: 'okta', name: 'Okta' }], true)
    expect(html.indexOf('password-form')).toBeLessThan(html.indexOf('or-divider'))
    expect(html.indexOf('or-divider')).toBeLessThan(html.indexOf('Continue with Google'))
    expect(html).toContain('aria-hidden="true" class="my-5')
    expect(html).toContain('Continue with Okta')
    expect(html).toContain('href="/oauth/okta/login')
  })
  it('says so when nothing is set up', () => {
    const html = methods([], false)
    expect(html).toContain('no-sign-in')
    expect(html).toContain('turn on password sign-in')
  })
  it('keeps the glass-card look: pill buttons that glow', () => {
    const html = methods([google], true, true)
    expect(html).toContain('login-btn')
    expect(html).toContain('rounded-full')
    expect(html).not.toContain('mt-8')
  })
})

describe('SignInForm', () => {
  const html = renderToStaticMarkup(<SignInForm glass={false} onSignedIn={none} />)
  const attr = (tag: RegExp, name: string) => new RegExp(`<${tag.source}[^>]*\\s${name}="([^"]*)"`).exec(html)?.[1]
  it('labels both fields and ties each label to its input', () => {
    const emailId = /<label for="([^"]+)"[^>]*>Email<\/label>/.exec(html)?.[1]
    const passwordId = /<label for="([^"]+)"[^>]*>Password<\/label>/.exec(html)?.[1]
    expect(emailId).toBeTruthy()
    expect(passwordId).toBeTruthy()
    expect(html).toContain(`id="${emailId}"`)
    expect(html).toContain(`id="${passwordId}"`)
  })
  it('uses the autocomplete values password managers look for', () => {
    expect(html).toMatch(/<input[^>]*type="email"[^>]*autoComplete="username"|<input[^>]*autoComplete="username"[^>]*type="email"/)
    expect(html).toMatch(/<input[^>]*type="password"[^>]*autoComplete="current-password"|<input[^>]*autoComplete="current-password"[^>]*type="password"/)
    expect(attr(/input/, 'required')).toBe('')
  })
  it('hides the password until asked, with a button that says what it does', () => {
    expect(html).toContain('type="password"')
    expect(html).toContain('aria-label="Show password"')
    expect(html).toContain('aria-pressed="false"')
  })
  it('is one form with a submit button, so Enter submits, and no error line before an error', () => {
    expect(html.match(/<form/g)).toHaveLength(1)
    expect(html).toContain('type="submit"')
    expect(html).toContain('Sign in')
    expect(html).not.toContain('role="alert"')
  })
})

describe('signInMessage', () => {
  it("repeats the server's words", () => {
    expect(signInMessage({ status: 401, message: 'Invalid email or password' })).toBe('Invalid email or password')
    expect(signInMessage({ status: 429, message: 'Too many attempts. Try again in 30 seconds.' })).toBe('Too many attempts. Try again in 30 seconds.')
  })
  it('falls back when there was no answer', () => {
    expect(signInMessage(new TypeError('Failed to fetch'))).toMatch(/Could not reach the server/)
  })
})
