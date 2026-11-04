// Fetch wrapper: Bearer token from localStorage.token (the HttpOnly session
// cookie also works), JSON in and out, 401 -> /auth.

export class ApiError extends Error {
  status: number
  code?: string
  constructor(status: number, message: string, code?: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export function getToken(): string {
  try {
    return localStorage.getItem('token') ?? ''
  } catch {
    return ''
  }
}

export function setToken(token: string) {
  try {
    if (token) localStorage.setItem('token', token)
    else localStorage.removeItem('token')
  } catch {
    // storage blocked: the session cookie still authenticates
  }
}

/** Sends the user to the sign-in page, remembering where they were. */
export function redirectToAuth() {
  if (location.pathname.startsWith('/auth') || location.pathname.startsWith('/s/')) return
  setToken('')
  const back = location.pathname + location.search
  location.assign(`/auth?redirect=${encodeURIComponent(back)}`)
}

export function authHeaders(extra?: HeadersInit): Headers {
  const h = new Headers(extra)
  const t = getToken()
  if (t && !h.has('Authorization')) h.set('Authorization', `Bearer ${t}`)
  return h
}

/** Raw authenticated fetch (streams, uploads). Throws ApiError on non-2xx. */
export async function apiFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const res = await fetch(path, { ...init, headers: authHeaders(init.headers), credentials: 'same-origin' })
  if (res.ok) return res
  if (res.status === 401) redirectToAuth()
  throw await toError(res)
}

async function toError(res: Response): Promise<ApiError> {
  let message = res.statusText || `HTTP ${res.status}`
  let code: string | undefined
  try {
    const b = await res.json()
    const e = b?.error ?? b
    message = (typeof e === 'string' ? e : e?.message ?? e?.detail ?? b?.detail) || message
    code = e?.code ?? b?.code
  } catch {
    // not JSON
  }
  return new ApiError(res.status, message, code)
}

/** JSON request. `json` is serialised as the body. */
export async function api<T>(path: string, init: RequestInit & { json?: unknown } = {}): Promise<T> {
  const { json, ...rest } = init
  const headers = new Headers(rest.headers)
  if (json !== undefined) headers.set('Content-Type', 'application/json')
  const res = await apiFetch(path, { ...rest, headers, body: json !== undefined ? JSON.stringify(json) : rest.body })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}
