import { describe, expect, it } from 'vitest'
import { idError, urlError, validateConnection, validateHook, validateProvider, validateToolServer } from './validate'

const add = { isNew: true, taken: ['kb'] }
const edit = { isNew: false, taken: ['kb'] }

describe('field rules', () => {
  it('checks ids on add only', () => {
    expect(idError('mcp:google-sheets', true, [])).toBeUndefined()
    expect(idError('a.b_c-1', true, [])).toBeUndefined()
    expect(idError('', true, [])).toBe('ID is required')
    expect(idError('', true, [], true)).toBeUndefined()
    expect(idError('My Server', true, [])).toMatch(/lowercase/)
    expect(idError('a/b', true, [])).toMatch(/lowercase/)
    expect(idError('kb', true, ['kb'])).toMatch(/already/)
    expect(idError('Legacy Id', false, [])).toBeUndefined()
  })
  it('checks URLs', () => {
    expect(urlError('http://h:8080/x')).toBeUndefined()
    expect(urlError('')).toBe('URL is required')
    expect(urlError('', false)).toBeUndefined()
    expect(urlError('http://h ')).toMatch(/spaces/)
    expect(urlError('ftp://h')).toMatch(/http/)
    expect(urlError('localhost:8080')).toMatch(/http/)
    expect(urlError('http://')).toMatch(/http/)
  })
})

describe('connections', () => {
  const c = { id: '', kind: 'openai' as const, base_url: 'http://litellm:8000/v1', prefix: '', sort: 0 }
  it('accepts /v1 for OpenAI-compatible but not Ollama', () => {
    expect(validateConnection(c, add)).toEqual({})
    expect(validateConnection({ ...c, kind: 'ollama' }, add).base_url).toMatch(/no \/v1/)
    expect(validateConnection({ ...c, kind: 'ollama', base_url: 'http://h:11434' }, add)).toEqual({})
  })
  it('flags prefix, sort and duplicate ids', () => {
    const e = validateConnection({ ...c, id: 'kb', prefix: 'a.b', sort: 1.5 }, add)
    expect(Object.keys(e).sort()).toEqual(['id', 'prefix', 'sort'])
  })
})

describe('tool servers', () => {
  const t = { id: 'new', kind: 'openapi' as const, url: 'http://t:8781', spec_path: 'openapi.json', max_result_chars: 0, auth: { type: 'none' as const, header: '', provider: '', has_key: false } }
  const ctx = { ...add, providers: ['mcp:google-sheets'] }
  it('passes a plain server', () => expect(validateToolServer(t, null, ctx)).toEqual({}))
  it('needs a relative spec path and a non-negative cap', () => {
    const e = validateToolServer({ ...t, spec_path: 'https://x/openapi.json', max_result_chars: -1 }, null, ctx)
    expect(Object.keys(e).sort()).toEqual(['max_result_chars', 'spec_path'])
  })
  it('needs a key for bearer and header auth unless one is stored', () => {
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'bearer' } }, null, ctx)['auth.key']).toBeTruthy()
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'bearer' } }, 'k', ctx)).toEqual({})
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'bearer', has_key: true } }, null, { ...ctx, isNew: false })).toEqual({})
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'bearer', has_key: true } }, '', { ...ctx, isNew: false })['auth.key']).toBeTruthy()
    const e = validateToolServer({ ...t, auth: { ...t.auth, type: 'header', header: 'X Key' } }, 'k', ctx)
    expect(e['auth.header']).toMatch(/header name/)
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'header', header: '' } }, 'k', ctx)['auth.header']).toMatch(/required/)
  })
  it('needs an existing provider for user_oauth', () => {
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'user_oauth' } }, null, ctx)['auth.provider']).toMatch(/Choose/)
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'user_oauth', provider: 'ghost' } }, null, ctx)['auth.provider']).toMatch(/Unknown/)
    expect(validateToolServer({ ...t, auth: { ...t.auth, type: 'user_oauth', provider: 'mcp:google-sheets' } }, null, ctx)).toEqual({})
  })
  it('keeps old ids on edit', () => expect(validateToolServer({ ...t, id: 'Old One' }, null, { ...ctx, isNew: false })).toEqual({}))
})

describe('hook servers', () => {
  const h = { id: 'h', url: 'http://h', events: ['inlet'], priority: 0, timeout_ms: 10000, scope: 'global' as const, model_ids: [] }
  it('passes a global hook', () => expect(validateHook(h, '{}', add)).toEqual({}))
  it('flags events, priority, timeout, models and config', () => {
    const e = validateHook({ ...h, events: [], priority: 1.5, timeout_ms: 500, scope: 'models' }, '[1]', add)
    expect(Object.keys(e).sort()).toEqual(['config', 'events', 'model_ids', 'priority', 'timeout_ms'])
    expect(validateHook({ ...h, timeout_ms: 120001 }, '', add).timeout_ms).toBeTruthy()
    expect(validateHook({ ...h, timeout_ms: Number.NaN }, '', add).timeout_ms).toBeTruthy()
    expect(validateHook(h, '{bad', add).config).toMatch(/JSON/)
    expect(validateHook({ ...h, scope: 'models', model_ids: ['m'] }, '', add)).toEqual({})
  })
})

describe('OAuth providers', () => {
  const p = { id: 'tool', login: false, issuer: '', authorization_endpoint: 'https://a/auth', token_endpoint: 'https://a/token', userinfo_endpoint: '', revocation_endpoint: '', client_id: 'c', scope: 'read write' }
  it('passes a tool provider', () => expect(validateProvider(p, add)).toEqual({}))
  it('needs a client id and both endpoints', () => {
    const e = validateProvider({ ...p, client_id: ' ', token_endpoint: '' }, add)
    expect(Object.keys(e).sort()).toEqual(['client_id', 'token_endpoint'])
    expect(validateProvider({ ...p, authorization_endpoint: '', token_endpoint: '' }, add).authorization_endpoint).toMatch(/Required/)
  })
  it('lets sign-in providers leave both endpoints empty (Google)', () => {
    expect(validateProvider({ ...p, login: true, authorization_endpoint: '', token_endpoint: '', scope: '' }, add)).toEqual({})
    expect(validateProvider({ ...p, login: true, token_endpoint: '', scope: '' }, add).token_endpoint).toMatch(/neither/)
  })
  it('checks scopes', () => {
    expect(validateProvider({ ...p, scope: 'openid,email' }, add).scope).toMatch(/commas/)
    expect(validateProvider({ ...p, login: true, scope: 'profile' }, add).scope).toMatch(/openid/)
    expect(validateProvider({ ...p, login: true, scope: 'read:user user:email' }, add)).toEqual({})
    expect(validateProvider({ ...p, issuer: 'accounts.google.com' }, add).issuer).toBeTruthy()
  })
  it('checks id format and uniqueness on add', () => {
    expect(validateProvider({ ...p, id: 'kb' }, add).id).toMatch(/already/)
    expect(validateProvider({ ...p, id: 'kb' }, edit)).toEqual({})
  })
})
