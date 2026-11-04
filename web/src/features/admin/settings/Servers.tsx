// Tool servers, hook servers and OAuth providers: list + editor dialogs.
// Secrets are write-only (has_key / has_secret); saving makes a row UI-owned.
import { useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Copy, Pencil, Plus, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useModels } from '@/api/queries'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Switch } from '@/ui/misc'
import { toast } from '@/stores/toast'
import { AccessEditor } from '../Access'
import { enc, useHookServers, useProviders, useToolServers, type HookServer, type OAuthProvider, type ToolServer } from '../api'
import { parseList } from '../lib'
import { attempt, Badge, Btn, Card, Check, Confirm, Field, inputCls, Loading, ManagedBadge, managedWarning, SecretInput } from '../ui'

/** List shell shared by the three tabs. */
function ServerList<T extends { id: string; managed: boolean }>(props: {
  q: { data?: T[]; isLoading: boolean; error: unknown }
  noun: string
  addLabel: string
  empty: string
  summary: (x: T) => ReactNode
  badges: (x: T) => ReactNode
  toggle?: (x: T) => ReactNode
  path: string
  queryKey: string
  editor: (x: T | null, close: () => void) => ReactNode
  deleteBody: (x: T) => ReactNode
}) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState<T | null | 'new'>(null)
  const [deleting, setDeleting] = useState<T | null>(null)
  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Btn variant="primary" onClick={() => setEditing('new')} data-testid={`new-${props.queryKey}`}>
          <Plus size={15} /> {props.addLabel}
        </Btn>
      </div>
      <Loading q={props.q} />
      {props.q.data && !props.q.data.length && <p className="text-sm text-muted">{props.empty}</p>}
      {props.q.data?.map((x) => (
        <Card key={x.id} className="p-4">
          <div className="flex flex-wrap items-center gap-3" data-testid={`${props.queryKey}-row`}>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-semibold">{x.id}</span>
                {props.badges(x)}
                {x.managed && <ManagedBadge />}
              </div>
              <p className="truncate text-sm text-muted">{props.summary(x)}</p>
            </div>
            {props.toggle?.(x)}
            <IconButton label={`Edit ${x.id}`} onClick={() => setEditing(x)}>
              <Pencil size={15} />
            </IconButton>
            <IconButton label={`Delete ${x.id}`} onClick={() => setDeleting(x)} className="hover:text-red-600">
              <Trash2 size={15} />
            </IconButton>
          </div>
        </Card>
      ))}
      {editing && props.editor(editing === 'new' ? null : editing, () => setEditing(null))}
      <Confirm
        open={!!deleting}
        title={`Delete ${props.noun}`}
        body={deleting && props.deleteBody(deleting)}
        onClose={() => setDeleting(null)}
        onConfirm={async () => {
          await api(`${props.path}/${enc(deleting!.id)}`, { method: 'DELETE' })
          await qc.invalidateQueries({ queryKey: ['admin', props.queryKey] })
        }}
      />
    </div>
  )
}

function useSaver<T>(path: string, queryKey: string) {
  const qc = useQueryClient()
  return (body: unknown) =>
    attempt(async () => {
      await api<T>(path, { method: 'POST', json: body })
      await qc.invalidateQueries({ queryKey: ['admin', queryKey] })
      if (queryKey === 'tool_servers') await qc.invalidateQueries({ queryKey: ['tools'] })
    }, 'Saved')
}

function EditorDialog({ title, managed, onClose, onSubmit, children, wide }: { title: string; managed?: boolean; onClose: () => void; onSubmit: () => Promise<void>; children: ReactNode; wide?: boolean }) {
  return (
    <Dialog open onClose={onClose} label={title} className={wide ? 'w-[min(48rem,calc(100vw-2rem))]' : 'w-[min(40rem,calc(100vw-2rem))]'}>
      <DialogHeader title={title} onClose={onClose} />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void onSubmit()
        }}
        className="max-h-[calc(100dvh-8rem)] space-y-3 overflow-y-auto p-4"
      >
        {managed && <p className="text-xs text-amber-700 dark:text-amber-400">{managedWarning}</p>}
        {children}
        <div className="flex justify-end gap-2 border-t border-line pt-3">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary" data-testid="save-server">
            Save
          </Btn>
        </div>
      </form>
    </Dialog>
  )
}

// ---- tool servers ----------------------------------------------------------------

const TOOLS = '/api/v1/configs/tool_servers'

export function ToolServersTab() {
  const q = useToolServers()
  const save = useSaver<ToolServer>(TOOLS, 'tool_servers')
  return (
    <ServerList
      q={q}
      noun="tool server"
      addLabel="Add tool server"
      empty="No tool servers. Add an OpenAPI or MCP server to give models tools."
      path={TOOLS}
      queryKey="tool_servers"
      summary={(t) => `${t.name ? t.name + ' · ' : ''}${t.url}`}
      badges={(t) => (
        <>
          <Badge>{t.kind === 'mcp' ? 'MCP' : 'OpenAPI'}</Badge>
          {t.auth.type !== 'none' && <Badge tone={t.auth.type === 'user_oauth' || t.auth.has_key ? 'green' : 'red'}>{t.auth.type === 'user_oauth' ? `OAuth: ${t.auth.provider}` : `${t.auth.type} auth`}</Badge>}
        </>
      )}
      toggle={(t) => <Switch checked={t.enabled} onChange={(on) => void save({ ...t, auth: { ...t.auth, key: undefined }, enabled: on })} label={`Enable ${t.id}`} />}
      editor={(t, close) => <ToolEditor server={t} onClose={close} save={save} />}
      deleteBody={(t) => <p>Delete {t.id}? Models lose its tools and its access grants are removed.</p>}
    />
  )
}

const blankTool: ToolServer = {
  id: '',
  kind: 'openapi',
  name: '',
  description: '',
  url: '',
  spec_path: 'openapi.json',
  auth: { type: 'none', header: '', provider: '', has_key: false },
  enabled: true,
  name_filter: [],
  disabled_functions: [],
  max_result_chars: 0,
  auto_enable_when_connected: false,
  sort: 0,
  managed: false,
}

function ToolEditor({ server, onClose, save }: { server: ToolServer | null; onClose: () => void; save: (b: unknown) => Promise<boolean> }) {
  const providers = useProviders().data ?? []
  const [t, setT] = useState(server ?? blankTool)
  const [key, setKey] = useState<string | null>(null)
  const [filter, setFilter] = useState(t.name_filter.join('\n'))
  const [disabled, setDisabled] = useState(t.disabled_functions.join('\n'))
  const [savedId, setSavedId] = useState(server?.id ?? '')
  const auth = t.auth
  const submit = async () => {
    const body = { ...t, auth: { type: auth.type, header: auth.header, provider: auth.provider, ...(key !== null ? { key } : {}) }, name_filter: parseList(filter), disabled_functions: parseList(disabled) }
    if (await save(body)) {
      if (server) onClose()
      else {
        setSavedId(t.id) // stay open so access can be set on the new server
        toast('Now choose who can use it')
      }
    }
  }
  return (
    <EditorDialog title={server ? `Edit ${server.id}` : 'Add tool server'} managed={server?.managed} onClose={onClose} onSubmit={submit} wide>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="ID">
          <input value={t.id} required disabled={!!server} onChange={(e) => setT({ ...t, id: e.target.value })} className={inputCls} data-testid="server-id" />
        </Field>
        <Field label="Kind">
          <select value={t.kind} onChange={(e) => setT({ ...t, kind: e.target.value as ToolServer['kind'] })} className={inputCls}>
            <option value="openapi">OpenAPI</option>
            <option value="mcp">MCP (streamable HTTP)</option>
          </select>
        </Field>
        <Field label="Name">
          <input value={t.name} onChange={(e) => setT({ ...t, name: e.target.value })} className={inputCls} />
        </Field>
      </div>
      <Field label="Description">
        <input value={t.description} onChange={(e) => setT({ ...t, description: e.target.value })} className={inputCls} />
      </Field>
      <div className="grid gap-3 sm:grid-cols-[2fr_1fr]">
        <Field label="URL">
          <input value={t.url} required type="url" onChange={(e) => setT({ ...t, url: e.target.value })} placeholder="http://tools:8080" className={inputCls} data-testid="server-url" />
        </Field>
        {t.kind === 'openapi' && (
          <Field label="Spec path">
            <input value={t.spec_path} onChange={(e) => setT({ ...t, spec_path: e.target.value })} className={inputCls} />
          </Field>
        )}
      </div>
      <fieldset className="grid gap-3 rounded-lg border border-line p-3 sm:grid-cols-2">
        <legend className="px-1 text-xs font-medium text-muted">Authentication</legend>
        <Field label="Type">
          <select value={auth.type} onChange={(e) => setT({ ...t, auth: { ...auth, type: e.target.value as ToolServer['auth']['type'] } })} className={inputCls} data-testid="auth-type">
            <option value="none">None</option>
            <option value="bearer">Bearer token</option>
            <option value="header">Custom header</option>
            <option value="user_oauth">Each user signs in (OAuth)</option>
          </select>
        </Field>
        {(auth.type === 'header' || auth.type === 'user_oauth') && (
          <Field label="Header name" hint={auth.type === 'user_oauth' ? 'Optional: a service key sent next to the user\'s token.' : undefined}>
            <input value={auth.header} required={auth.type === 'header'} onChange={(e) => setT({ ...t, auth: { ...auth, header: e.target.value } })} placeholder="X-API-Key" className={inputCls} />
          </Field>
        )}
        {(auth.type === 'bearer' || auth.type === 'header' || (auth.type === 'user_oauth' && auth.header)) && (
          <Field label="Key" hint="Write-only." className="sm:col-span-2">
            <SecretInput has={auth.has_key} value={key} onChange={setKey} label="Tool server key" />
          </Field>
        )}
        {auth.type === 'user_oauth' && (
          <Field label="OAuth provider" hint="Defined under OAuth providers.">
            <select value={auth.provider} required onChange={(e) => setT({ ...t, auth: { ...auth, provider: e.target.value } })} className={inputCls}>
              <option value="">Choose…</option>
              {providers
                .filter((p) => !p.login)
                .map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name || p.id}
                  </option>
                ))}
            </select>
          </Field>
        )}
      </fieldset>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Name filter" hint="Only offer functions matching these names (one per line, * wildcards). Empty = all.">
          <textarea value={filter} rows={3} onChange={(e) => setFilter(e.target.value)} className={inputCls} />
        </Field>
        <Field label="Disabled functions" hint="Never offered (one per line).">
          <textarea value={disabled} rows={3} onChange={(e) => setDisabled(e.target.value)} className={inputCls} />
        </Field>
      </div>
      <div className="grid items-end gap-3 sm:grid-cols-3">
        <Field label="Max result characters" hint="0 = no limit.">
          <input type="number" min={0} value={t.max_result_chars} onChange={(e) => setT({ ...t, max_result_chars: Number(e.target.value) || 0 })} className={inputCls} />
        </Field>
        <Check checked={t.auto_enable_when_connected} onChange={(v) => setT({ ...t, auto_enable_when_connected: v })}>
          Auto-enable once a user connects
        </Check>
        <Check checked={t.enabled} onChange={(v) => setT({ ...t, enabled: v })}>
          Enabled
        </Check>
      </div>
      <section className="space-y-2 border-t border-line pt-3">
        <h3 className="text-sm font-semibold">Who can use it</h3>
        {savedId ? <AccessEditor type="tool_server" id={savedId} /> : <p className="text-xs text-muted">Save the server first, then grant access. Without grants only admins can use it.</p>}
      </section>
    </EditorDialog>
  )
}

// ---- hook servers ------------------------------------------------------------------

const HOOKS = '/api/v1/configs/hook_servers'

export function HookServersTab() {
  const q = useHookServers()
  const save = useSaver<HookServer>(HOOKS, 'hook_servers')
  return (
    <ServerList
      q={q}
      noun="hook server"
      addLabel="Add hook server"
      empty="No hook servers. Hooks see and may change requests (inlet) and replies (outlet)."
      path={HOOKS}
      queryKey="hook_servers"
      summary={(h) => `${h.name ? h.name + ' · ' : ''}${h.url}`}
      badges={(h) => (
        <>
          {h.events.map((e) => (
            <Badge key={e}>{e}</Badge>
          ))}
          <Badge tone={h.scope === 'global' ? 'amber' : 'grey'}>{h.scope === 'global' ? 'all models' : `${h.model_ids.length} models`}</Badge>
        </>
      )}
      toggle={(h) => <Switch checked={h.enabled} onChange={(on) => void save({ ...h, enabled: on })} label={`Enable ${h.id}`} />}
      editor={(h, close) => <HookEditor hook={h} onClose={close} save={save} />}
      deleteBody={(h) => <p>Delete hook server {h.id}?</p>}
    />
  )
}

const blankHook: HookServer = { id: '', name: '', url: '', has_key: false, events: ['inlet'], priority: 0, scope: 'models', model_ids: [], config: {}, forward_oauth: [], timeout_ms: 10000, on_error: 'skip', enabled: true, managed: false }

function HookEditor({ hook, onClose, save }: { hook: HookServer | null; onClose: () => void; save: (b: unknown) => Promise<boolean> }) {
  const models = useModels().data ?? []
  const [h, setH] = useState(hook ?? blankHook)
  const [key, setKey] = useState<string | null>(null)
  const [config, setConfig] = useState(JSON.stringify(h.config ?? {}, null, 2))
  const [forward, setForward] = useState(h.forward_oauth.join('\n'))
  const toggle = (list: string[], v: string, on: boolean) => (on ? [...list.filter((x) => x !== v), v] : list.filter((x) => x !== v))
  const submit = async () => {
    let cfg: unknown
    try {
      cfg = JSON.parse(config || '{}')
    } catch {
      toast('Config must be valid JSON', 'error')
      return
    }
    if (await save({ ...h, config: cfg, forward_oauth: parseList(forward), ...(key !== null ? { key } : {}) })) onClose()
  }
  return (
    <EditorDialog title={hook ? `Edit ${hook.id}` : 'Add hook server'} managed={hook?.managed} onClose={onClose} onSubmit={submit} wide>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="ID">
          <input value={h.id} required disabled={!!hook} onChange={(e) => setH({ ...h, id: e.target.value })} className={inputCls} />
        </Field>
        <Field label="Name">
          <input value={h.name} onChange={(e) => setH({ ...h, name: e.target.value })} className={inputCls} />
        </Field>
      </div>
      <Field label="URL">
        <input value={h.url} required type="url" onChange={(e) => setH({ ...h, url: e.target.value })} className={inputCls} />
      </Field>
      <Field label="Key" hint="Sent as a bearer token. Write-only.">
        <SecretInput has={h.has_key} value={key} onChange={setKey} label="Hook key" />
      </Field>
      <div className="flex flex-wrap gap-5">
        {['inlet', 'outlet'].map((e) => (
          <Check key={e} checked={h.events.includes(e)} onChange={(on) => setH({ ...h, events: toggle(h.events, e, on) })}>
            {e === 'inlet' ? 'Inlet (before the model)' : 'Outlet (after the reply)'}
          </Check>
        ))}
        <Check checked={h.enabled} onChange={(v) => setH({ ...h, enabled: v })}>
          Enabled
        </Check>
      </div>
      <div className="grid gap-3 sm:grid-cols-4">
        <Field label="Scope">
          <select value={h.scope} onChange={(e) => setH({ ...h, scope: e.target.value as HookServer['scope'] })} className={inputCls}>
            <option value="models">Selected models</option>
            <option value="global">All models</option>
          </select>
        </Field>
        <Field label="Priority" hint="Lower runs first.">
          <input type="number" value={h.priority} onChange={(e) => setH({ ...h, priority: Number(e.target.value) || 0 })} className={inputCls} />
        </Field>
        <Field label="Timeout (ms)">
          <input type="number" min={1} value={h.timeout_ms} onChange={(e) => setH({ ...h, timeout_ms: Number(e.target.value) || 10000 })} className={inputCls} />
        </Field>
        <Field label="On error">
          <select value={h.on_error} onChange={(e) => setH({ ...h, on_error: e.target.value as HookServer['on_error'] })} className={inputCls}>
            <option value="skip">Skip the hook</option>
            <option value="abort">Fail the request</option>
          </select>
        </Field>
      </div>
      {h.scope === 'models' && (
        <Field label="Models">
          <div className="flex max-h-40 flex-wrap gap-x-5 gap-y-2 overflow-y-auto rounded-lg border border-line p-2">
            {models.map((m) => (
              <Check key={m.id} checked={h.model_ids.includes(m.id)} onChange={(on) => setH({ ...h, model_ids: toggle(h.model_ids, m.id, on) })}>
                {m.name || m.id}
              </Check>
            ))}
          </div>
        </Field>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Config (JSON)" hint="Sent to the hook with every call.">
          <textarea value={config} rows={4} onChange={(e) => setConfig(e.target.value)} className={inputCls + ' font-mono text-xs'} />
        </Field>
        <Field label="Forward OAuth tokens" hint="Provider ids whose user token the hook receives (one per line).">
          <textarea value={forward} rows={4} onChange={(e) => setForward(e.target.value)} className={inputCls} />
        </Field>
      </div>
    </EditorDialog>
  )
}

// ---- OAuth providers -------------------------------------------------------------------

const PROVIDERS = '/api/v1/configs/oauth_providers'

export function OAuthTab() {
  const q = useProviders()
  const save = useSaver<OAuthProvider>(PROVIDERS, 'oauth_providers')
  return (
    <ServerList
      q={q}
      noun="OAuth provider"
      addLabel="Add provider"
      empty="No OAuth providers."
      path={PROVIDERS}
      queryKey="oauth_providers"
      summary={(p) => (
        <>
          {p.name ? `${p.name} · ` : ''}Redirect URI <code className="text-xs">{p.redirect_uri}</code>
        </>
      )}
      badges={(p) => (
        <>
          <Badge tone={p.login ? 'accent' : 'grey'}>{p.login ? 'sign-in' : 'tool server'}</Badge>
          {!p.has_secret && <Badge tone="amber">no secret</Badge>}
        </>
      )}
      editor={(p, close) => <ProviderEditor provider={p} onClose={close} save={save} />}
      deleteBody={(p) => <p>Delete {p.id}? {p.login ? 'People can no longer sign in with it.' : 'Users connected through it must reconnect if it comes back.'}</p>}
    />
  )
}

const blankProvider: OAuthProvider = {
  id: '',
  name: '',
  login: false,
  issuer: '',
  authorization_endpoint: '',
  token_endpoint: '',
  revocation_endpoint: '',
  userinfo_endpoint: '',
  client_id: '',
  has_secret: false,
  scope: '',
  pkce: true,
  send_resource: false,
  managed: false,
  redirect_uri: '',
}

/** What the provider console must list; the server's value wins once saved (it uses KERA_URL). */
export function redirectPreview(p: OAuthProvider, origin: string): string {
  if (p.redirect_uri) return p.redirect_uri
  const id = encodeURIComponent(p.id || '<id>').replace(/%3A/gi, ':')
  return p.login ? `${origin}/oauth/${id}/callback` : `${origin}/oauth/clients/${id}/callback`
}

function ProviderEditor({ provider, onClose, save }: { provider: OAuthProvider | null; onClose: () => void; save: (b: unknown) => Promise<boolean> }) {
  const [p, setP] = useState(provider ?? blankProvider)
  const [secret, setSecret] = useState<string | null>(null)
  const uri = redirectPreview(provider && provider.login === p.login ? p : { ...p, redirect_uri: '' }, location.origin)
  const text = (k: keyof OAuthProvider, label: string, hint?: string, type = 'url') => (
    <Field label={label} hint={hint}>
      <input type={type} value={String(p[k] ?? '')} onChange={(e) => setP({ ...p, [k]: e.target.value })} className={inputCls} />
    </Field>
  )
  const submit = async () => {
    if (await save({ ...p, ...(secret !== null ? { client_secret: secret } : {}) })) onClose()
  }
  return (
    <EditorDialog title={provider ? `Edit ${provider.id}` : 'Add OAuth provider'} managed={provider?.managed} onClose={onClose} onSubmit={submit} wide>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="ID" hint="For a tool server's per-user sign-in, use the tool server's id.">
          <input value={p.id} required disabled={!!provider} onChange={(e) => setP({ ...p, id: e.target.value })} className={inputCls} />
        </Field>
        {text('name', 'Name', undefined, 'text')}
      </div>
      <div className="flex flex-wrap gap-5">
        <Check checked={p.login} onChange={(v) => setP({ ...p, login: v })}>
          Offer on the sign-in page
        </Check>
        <Check checked={p.pkce} onChange={(v) => setP({ ...p, pkce: v })}>
          PKCE
        </Check>
        <Check checked={p.send_resource} onChange={(v) => setP({ ...p, send_resource: v })}>
          Send resource indicator
        </Check>
      </div>
      <div className="rounded-lg border border-line bg-bg-soft p-3 text-sm">
        <div className="mb-1 text-xs font-medium text-muted">Redirect URI to register with the provider</div>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 break-all text-xs" data-testid="redirect-uri">
            {uri}
          </code>
          <IconButton label="Copy redirect URI" onClick={() => void navigator.clipboard?.writeText(uri).then(() => toast('Copied'))}>
            <Copy size={14} />
          </IconButton>
        </div>
      </div>
      {text('issuer', 'Issuer', "Sign-in providers with empty endpoints use Google's.")}
      <div className="grid gap-3 sm:grid-cols-2">
        {text('authorization_endpoint', 'Authorization endpoint')}
        {text('token_endpoint', 'Token endpoint')}
        {text('userinfo_endpoint', 'Userinfo endpoint')}
        {text('revocation_endpoint', 'Revocation endpoint', 'Called when a user disconnects.')}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Client ID">
          <input value={p.client_id} required onChange={(e) => setP({ ...p, client_id: e.target.value })} className={inputCls} />
        </Field>
        <Field label="Client secret" hint="Write-only.">
          <SecretInput has={p.has_secret} value={secret} onChange={setSecret} label="Client secret" />
        </Field>
      </div>
      {text('scope', 'Scope', 'Space-separated.', 'text')}
    </EditorDialog>
  )
}
