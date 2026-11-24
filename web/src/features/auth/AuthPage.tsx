import { useEffect, useId, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { KeyRound } from 'lucide-react'
import { api, setToken, type ApiError } from '@/api/client'
import { useAuthOptions } from '@/api/queries'
import type { AuthOptions, Me } from '@/api/types'
import { uiConfig } from '@/lib/ui-config'
import { cx } from '@/lib/format'
import { safeRedirect } from '@/app/search-params'
import { Spinner } from '@/ui/misc'
import { LField, PasswordInput } from '@/ui/PasswordInput'
import { Aurora, brandMark } from '@/features/brand/Splash'

export default function AuthPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const [checking, setChecking] = useState(true)
  const redirect = safeRedirect(params.get('redirect'))
  const brand = uiConfig().brand
  const options = useAuthOptions()

  useEffect(() => {
    const hash = new URLSearchParams(location.hash.slice(1))
    const token = hash.get('token')
    if (token) {
      setToken(token)
      history.replaceState(null, '', location.pathname + location.search)
      // the OAuth callback returns the login's ?next= in the hash
      navigate(safeRedirect(hash.get('next') ?? redirect), { replace: true })
      return
    }
    // Already signed in (cookie or stored token)? Skip the sign-in methods.
    api<Me>('/api/v1/auths/')
      .then(() => navigate(redirect, { replace: true }))
      .catch(() => setChecking(false))
  }, [navigate, redirect])

  const error = params.get('error')
  const glass = brand.login?.style === 'glass-card'
  const mark = brandMark()
  const alert = error && (
    <p role="alert" className="mt-4 rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-600">
      {error}
    </p>
  )
  const body = checking || options.isPending ? (
    <div className="mt-8 flex justify-center text-muted">
      <Spinner size={20} />
    </div>
  ) : options.isError ? (
    <p role="alert" className="mt-6 text-sm text-red-600">
      Could not load the sign-in options.{' '}
      <button type="button" className="underline" onClick={() => location.reload()}>
        Reload
      </button>
    </p>
  ) : (
    <SignInMethods options={options.data} glass={glass} redirect={redirect} onSignedIn={(to) => navigate(to, { replace: true })} />
  )

  if (glass)
    return (
      <main className="relative isolate grid min-h-dvh place-items-center overflow-clip bg-bg px-4 py-20">
        <Aurora variant="login" />
        <div className="login-card" data-testid="login-card">
          {mark && (
            <div className="login-mark">
              <img src={mark} alt="" />
            </div>
          )}
          <h1 className="text-2xl tracking-tight">Sign in to {brand.name}</h1>
          {brand.tagline && <p className="mt-1.5 text-sm text-muted">{brand.tagline}</p>}
          {alert}
          <hr className="mx-auto mb-4 mt-10 w-3/5 border-line" />
          {body}
        </div>
        {brand.login?.wordmark && <div className="brand-wordmark absolute bottom-8 left-1/2 -translate-x-1/2 whitespace-nowrap">{brand.login.wordmark}</div>}
      </main>
    )

  return (
    <main className="grid min-h-dvh place-items-center bg-bg-soft px-4">
      <div className="w-full max-w-sm rounded-2xl border border-line bg-surface p-8 text-center shadow-pop">
        {mark && <img src={mark} alt="" className="mx-auto mb-4 size-12 object-contain" />}
        <h1 className="text-2xl font-semibold tracking-tight">{brand.name}</h1>
        {brand.tagline && <p className="mt-1 text-sm text-muted">{brand.tagline}</p>}
        {alert}
        {body}
      </div>
    </main>
  )
}

/** The form (when on), a quiet "or", and one button per SSO provider. */
export function SignInMethods({ options, glass, redirect, onSignedIn }: { options: AuthOptions; glass: boolean; redirect: string; onSignedIn: (to: string) => void }) {
  const { providers, login_form: form } = options
  if (!form && !providers.length)
    return (
      <p className="mt-6 text-sm text-muted" data-testid="no-sign-in">
        No sign-in method is on. An administrator can turn on password sign-in or add a provider under Admin &gt; Settings, or run kera user login-link on the server.
      </p>
    )
  return (
    <div className={cx(!glass && 'mt-8')}>
      {form && <SignInForm glass={glass} autoFocus={!providers.length} onSignedIn={() => onSignedIn(redirect)} />}
      {form && providers.length > 0 && (
        <div aria-hidden className="my-5 flex items-center gap-3 text-xs text-muted" data-testid="or-divider">
          <span className="h-px flex-1 bg-line" />
          or
          <span className="h-px flex-1 bg-line" />
        </div>
      )}
      {providers.length > 0 && (
        <div className="space-y-3">
          {providers.map((p) => (
            <a
              key={p.id}
              href={`/oauth/${encodeURIComponent(p.id)}/login?next=${encodeURIComponent(redirect)}`}
              data-testid={`${p.id}-login`}
              className={
                glass
                  ? 'login-btn flex w-full items-center justify-center gap-3 rounded-full bg-surface-2 px-4 py-3 text-sm font-medium'
                  : 'flex w-full items-center justify-center gap-3 rounded-xl border border-line bg-surface px-4 py-2.5 text-sm font-medium hover:bg-surface-2'
              }
            >
              {p.id === 'google' ? <GoogleIcon /> : <KeyRound size={16} aria-hidden />}
              Continue with {p.name}
            </a>
          ))}
        </div>
      )}
    </div>
  )
}

/** The line under the form: the server's own words (it says "Invalid email or password" or how long to wait), or a network fallback. */
export function signInMessage(e: { status?: number; message?: string }): string {
  return e.status && e.message ? e.message : 'Could not reach the server. Try again in a moment.'
}

/** Email and password. Every failure is one line under the fields; Enter submits. */
export function SignInForm({ glass, autoFocus, onSignedIn }: { glass: boolean; autoFocus?: boolean; onSignedIn: () => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [wrong, setWrong] = useState(false) // the answer was "invalid", not "wait"
  const emailRef = useRef<HTMLInputElement>(null)
  const passwordRef = useRef<HTMLInputElement>(null)
  const uid = useId()
  const emailId = `${uid}-email`
  const passwordId = `${uid}-password`
  const errorId = `${uid}-error`
  useEffect(() => {
    if (autoFocus) emailRef.current?.focus()
  }, [autoFocus])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    setWrong(false)
    try {
      const me = await api<Me>('/api/v1/auths/signin', { method: 'POST', json: { email: email.trim(), password } })
      if (me.token) setToken(me.token)
      onSignedIn()
    } catch (err) {
      setError(signInMessage(err as ApiError))
      if ((err as ApiError).status === 401) {
        setPassword('') // the email stays; the next try is a new password
        setWrong(true)
      }
      setBusy(false)
      passwordRef.current?.focus()
    }
  }

  const input = cx(
    'w-full rounded-xl border px-3.5 py-2.5 text-sm outline-none transition focus:border-accent focus:ring-2 focus:ring-accent/25',
    glass ? 'bg-surface/70' : 'bg-surface',
    wrong ? 'border-red-500' : 'border-line',
  )
  return (
    <form onSubmit={submit} className="space-y-4 text-left" data-testid="password-form" aria-label="Sign in with email and password">
      <LField id={emailId} label="Email">
        <input
          ref={emailRef}
          id={emailId}
          name="email"
          type="email"
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
          inputMode="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          aria-invalid={wrong || undefined}
          aria-describedby={error ? errorId : undefined}
          className={input}
          data-testid="login-email"
        />
      </LField>
      <LField id={passwordId} label="Password">
        <PasswordInput
          id={passwordId}
          inputRef={passwordRef}
          value={password}
          onChange={setPassword}
          autoComplete="current-password"
          required
          invalid={wrong}
          describedBy={error ? errorId : undefined}
          className={input}
          testId="login-password"
        />
      </LField>
      {error && (
        <p id={errorId} role="alert" className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-600" data-testid="login-error">
          {error}
        </p>
      )}
      <button
        type="submit"
        disabled={busy}
        data-testid="login-submit"
        className={cx(
          'flex w-full items-center justify-center gap-2 bg-accent text-sm font-medium text-white disabled:opacity-70',
          glass ? 'login-btn rounded-full px-4 py-3' : 'rounded-xl px-4 py-2.5 hover:opacity-90',
        )}
      >
        {busy && <Spinner size={14} />}
        {busy ? 'Signing in…' : 'Sign in'}
      </button>
    </form>
  )
}

function GoogleIcon() {
  return (
    <svg aria-hidden width="18" height="18" viewBox="0 0 48 48">
      <path fill="#EA4335" d="M24 9.5c3.5 0 6.6 1.2 9 3.5l6.7-6.7C35.6 2.4 30.2 0 24 0 14.6 0 6.6 5.4 2.6 13.2l7.8 6.1C12.3 13.6 17.7 9.5 24 9.5z" />
      <path fill="#4285F4" d="M46.5 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.7c-.6 3-2.3 5.5-4.8 7.2l7.5 5.8c4.4-4.1 7.1-10.1 7.1-17.5z" />
      <path fill="#FBBC05" d="M10.4 28.7c-.5-1.4-.8-3-.8-4.7s.3-3.2.8-4.7l-7.8-6.1C1 16.5 0 20.1 0 24s1 7.5 2.6 10.8l7.8-6.1z" />
      <path fill="#34A853" d="M24 48c6.5 0 11.9-2.1 15.9-5.8l-7.5-5.8c-2.1 1.4-4.8 2.3-8.4 2.3-6.3 0-11.7-4.1-13.6-9.8l-7.8 6.1C6.6 42.6 14.6 48 24 48z" />
    </svg>
  )
}
