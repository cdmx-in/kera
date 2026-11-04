import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { api, setToken } from '@/api/client'
import type { Me } from '@/api/types'
import { uiConfig } from '@/lib/ui-config'
import { Spinner } from '@/ui/misc'

function safeRedirect(v: string | null): string {
  return v && v.startsWith('/') && !v.startsWith('//') && !v.startsWith('/auth') ? v : '/'
}

export default function AuthPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const [checking, setChecking] = useState(true)
  const redirect = safeRedirect(params.get('redirect'))
  const brand = uiConfig().brand

  useEffect(() => {
    const hash = new URLSearchParams(location.hash.slice(1))
    const token = hash.get('token')
    if (token) {
      setToken(token)
      history.replaceState(null, '', location.pathname + location.search)
      navigate(redirect, { replace: true })
      return
    }
    // Already signed in (cookie or stored token)? Skip the button.
    api<Me>('/api/v1/auths/')
      .then(() => navigate(redirect, { replace: true }))
      .catch(() => setChecking(false))
  }, [navigate, redirect])

  const error = params.get('error')
  return (
    <main className="grid min-h-dvh place-items-center bg-bg-soft px-4">
      <div className="w-full max-w-sm rounded-2xl border border-line bg-surface p-8 text-center shadow-pop">
        <h1 className="text-2xl font-semibold tracking-tight">{brand.name}</h1>
        {brand.tagline && <p className="mt-1 text-sm text-muted">{brand.tagline}</p>}
        {error && (
          <p role="alert" className="mt-4 rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-600">
            {error}
          </p>
        )}
        {checking ? (
          <div className="mt-8 flex justify-center text-muted">
            <Spinner size={20} />
          </div>
        ) : (
          <a
            href={`/oauth/google/login?next=${encodeURIComponent(redirect)}`}
            data-testid="google-login"
            className="mt-8 flex w-full items-center justify-center gap-3 rounded-xl border border-line bg-surface px-4 py-2.5 text-sm font-medium hover:bg-surface-2"
          >
            <svg aria-hidden width="18" height="18" viewBox="0 0 48 48">
              <path fill="#EA4335" d="M24 9.5c3.5 0 6.6 1.2 9 3.5l6.7-6.7C35.6 2.4 30.2 0 24 0 14.6 0 6.6 5.4 2.6 13.2l7.8 6.1C12.3 13.6 17.7 9.5 24 9.5z" />
              <path fill="#4285F4" d="M46.5 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.7c-.6 3-2.3 5.5-4.8 7.2l7.5 5.8c4.4-4.1 7.1-10.1 7.1-17.5z" />
              <path fill="#FBBC05" d="M10.4 28.7c-.5-1.4-.8-3-.8-4.7s.3-3.2.8-4.7l-7.8-6.1C1 16.5 0 20.1 0 24s1 7.5 2.6 10.8l7.8-6.1z" />
              <path fill="#34A853" d="M24 48c6.5 0 11.9-2.1 15.9-5.8l-7.5-5.8c-2.1 1.4-4.8 2.3-8.4 2.3-6.3 0-11.7-4.1-13.6-9.8l-7.8 6.1C6.6 42.6 14.6 48 24 48z" />
            </svg>
            Continue with Google
          </a>
        )}
      </div>
    </main>
  )
}
