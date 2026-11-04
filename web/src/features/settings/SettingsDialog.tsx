import { useState } from 'react'
import { Tabs } from 'radix-ui'
import { useQueryClient } from '@tanstack/react-query'
import { KeyRound, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useApiKeys, useMe, useUserSettings, type UserSettings } from '@/api/queries'
import type { ApiKey } from '@/api/types'
import { useUI } from '@/stores/ui'
import { toast } from '@/stores/toast'
import { uiConfig } from '@/lib/ui-config'
import { getTheme, setTheme, type Theme } from '@/lib/theme'
import { cx } from '@/lib/format'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Switch } from '@/ui/misc'
import { Avatar } from '@/features/header/Header'
import { IntegrationSlots, hasSlot } from '@/features/integrations/registry'

const tabCls = 'whitespace-nowrap rounded-lg px-3 py-1.5 text-left text-sm text-muted data-[state=active]:bg-surface-2 data-[state=active]:text-fg hover:text-fg'

export function SettingsDialog() {
  const open = useUI((s) => s.settingsOpen)
  const tab = useUI((s) => s.settingsTab)
  const close = useUI((s) => s.closeSettings)
  const openTab = useUI((s) => s.openSettings)
  const cfg = uiConfig()
  const tabs: [string, string][] = [
    ['general', 'General'],
    ...(cfg.features.memory !== false ? [['personalization', 'Personalization'] as [string, string]] : []),
    ['account', 'Account'],
    ...(hasSlot('SettingsRow') ? [['integrations', 'Integrations'] as [string, string]] : []),
    ['chat', 'Chat'],
  ]
  return (
    <Dialog open={open} onClose={close} label="Settings" testId="settings" className="h-[min(40rem,calc(100dvh-2rem))] w-[min(48rem,calc(100vw-2rem))]">
      <div className="flex h-full flex-col">
        <DialogHeader title="Settings" onClose={close} />
        <Tabs.Root value={tab} onValueChange={(v) => openTab(v)} orientation="vertical" className="flex min-h-0 flex-1 flex-col sm:flex-row">
          <Tabs.List aria-label="Settings sections" className="flex shrink-0 gap-1 overflow-x-auto border-b border-line p-2 sm:w-44 sm:flex-col sm:border-b-0 sm:border-r">
            {tabs.map(([v, l]) => (
              <Tabs.Trigger key={v} value={v} className={tabCls}>
                {l}
              </Tabs.Trigger>
            ))}
          </Tabs.List>
          <div className="min-h-0 flex-1 overflow-y-auto p-5">
            <Tabs.Content value="general">
              <General />
            </Tabs.Content>
            <Tabs.Content value="personalization">
              <Personalization />
            </Tabs.Content>
            <Tabs.Content value="account">
              <Account />
            </Tabs.Content>
            <Tabs.Content value="integrations">
              <ul className="divide-y divide-line">
                <IntegrationSlots slot="SettingsRow" />
              </ul>
            </Tabs.Content>
            <Tabs.Content value="chat">
              <ChatPrefs />
            </Tabs.Content>
          </div>
        </Tabs.Root>
      </div>
    </Dialog>
  )
}

function Row({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 py-3">
      <div>
        <p className="text-sm font-medium">{title}</p>
        {hint && <p className="text-xs text-muted">{hint}</p>}
      </div>
      {children}
    </div>
  )
}

function General() {
  const [theme, set] = useState<Theme>(getTheme)
  return (
    <Row title="Theme">
      <div role="radiogroup" aria-label="Theme" className="flex rounded-lg border border-line p-0.5">
        {(['light', 'dark', 'system'] as Theme[]).map((t) => (
          <button
            key={t}
            type="button"
            role="radio"
            aria-checked={theme === t}
            data-testid={`theme-${t}`}
            onClick={() => (setTheme(t), set(t))}
            className={cx('rounded-md px-3 py-1 text-sm capitalize', theme === t ? 'bg-accent text-white' : 'text-muted hover:text-fg')}
          >
            {t}
          </button>
        ))}
      </div>
    </Row>
  )
}

function Personalization() {
  const settings = useUserSettings()
  const qc = useQueryClient()
  const on = !!settings.data?.ui?.memory
  const toggle = async (v: boolean) => {
    const next: UserSettings = { ...(settings.data ?? {}), ui: { ...(settings.data?.ui ?? {}), memory: v } }
    qc.setQueryData(['user-settings'], next)
    try {
      await api('/api/v1/users/user/settings/update', { method: 'POST', json: next })
    } catch (e) {
      qc.setQueryData(['user-settings'], settings.data)
      toast(`Could not save: ${(e as Error).message}`, 'error')
    }
  }
  return (
    <Row title="Memory" hint="Let the assistant remember useful details across chats.">
      <Switch checked={on} onChange={toggle} label="Memory" testId="memory-toggle" />
    </Row>
  )
}

function Account() {
  const { data: me } = useMe()
  const allowKeys = uiConfig().features.apiKeys !== false && (me?.role === 'admin' || !!me?.permissions?.features?.api_keys)
  if (!me) return null
  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Avatar name={me.name} src={me.profile_image_url} size={44} />
        <div className="min-w-0">
          <p className="truncate font-medium">{me.name}</p>
          <p className="truncate text-sm text-muted">{me.email}</p>
        </div>
      </div>
      {/* no session-token copy: scripts use revocable API keys, never the browser's sign-in token */}
      {allowKeys && <ApiKeys />}
    </div>
  )
}

function ApiKeys() {
  const keys = useApiKeys(true)
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState<ApiKey | null>(null)
  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      const k = await api<ApiKey>('/api/v1/auths/api_keys', { method: 'POST', json: { name: name.trim() || 'API key' } })
      setFresh(k)
      setName('')
      await qc.invalidateQueries({ queryKey: ['api-keys'] })
    } catch (err) {
      toast((err as Error).message, 'error')
    }
  }
  const revoke = async (k: ApiKey) => {
    if (!confirm(`Revoke "${k.name}"? Scripts using it stop working.`)) return
    try {
      await api(`/api/v1/auths/api_keys/${encodeURIComponent(k.id)}`, { method: 'DELETE' })
      await qc.invalidateQueries({ queryKey: ['api-keys'] })
    } catch (err) {
      toast((err as Error).message, 'error')
    }
  }
  return (
    <section>
      <h3 className="mb-2 text-sm font-medium">API keys</h3>
      {fresh?.key && (
        <div className="mb-3 rounded-xl border border-accent/50 bg-accent-soft p-3 text-sm">
          <p className="mb-2">Copy this key now. It will not be shown again.</p>
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded bg-surface px-2 py-1 text-xs">{fresh.key}</code>
            <button
              type="button"
              aria-label="Copy key"
              onClick={async () => {
                await navigator.clipboard?.writeText(fresh.key ?? '').catch(() => undefined)
                toast('Key copied')
              }}
              className="rounded-lg border border-line bg-surface px-2 py-1 text-xs"
            >
              Copy
            </button>
          </div>
        </div>
      )}
      <ul className="divide-y divide-line rounded-xl border border-line">
        {(keys.data ?? []).map((k) => (
          <li key={k.id} className="flex items-center gap-3 px-3 py-2 text-sm">
            <KeyRound size={15} className="text-muted" />
            <span className="min-w-0 flex-1">
              <span className="block truncate font-medium">{k.name}</span>
              <span className="block text-xs text-muted">
                {k.prefix ? `${k.prefix}… · ` : ''}
                {k.last_used_at ? `last used ${new Date(k.last_used_at * 1000).toLocaleDateString()}` : 'never used'}
              </span>
            </span>
            <button type="button" aria-label={`Revoke ${k.name}`} onClick={() => revoke(k)} className="grid size-7 place-items-center rounded-md text-muted hover:bg-surface-2 hover:text-red-600">
              <Trash2 size={14} />
            </button>
          </li>
        ))}
        {keys.isSuccess && !keys.data.length && <li className="px-3 py-2 text-sm text-muted">No API keys yet.</li>}
      </ul>
      <form onSubmit={create} className="mt-2 flex gap-2">
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Key name" aria-label="New key name" className="min-w-0 flex-1 rounded-lg border border-line bg-transparent px-3 py-1.5 text-sm outline-none focus:border-accent" />
        <button type="submit" className="rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white">
          Create key
        </button>
      </form>
    </section>
  )
}

function ChatPrefs() {
  const show = useUI((s) => s.showCompaction)
  const set = useUI((s) => s.setShowCompaction)
  return (
    <Row title="Show compaction markers" hint="Mark where earlier messages were summarised to save context.">
      <Switch checked={show} onChange={set} label="Show compaction markers" />
    </Row>
  )
}
