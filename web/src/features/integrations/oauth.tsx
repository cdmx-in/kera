// oauthToolServer: a tool server that needs the user's own OAuth grant
// (e.g. a spreadsheet tool). Connect goes through the server's authorize
// route and comes back to /?tools=<toolServerId>.
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '@/api/client'
import { useTools } from '@/api/queries'
import type { OAuthToolServerIntegration } from '@/lib/ui-config'
import { iconFor } from '@/lib/icons'
import { useComposer } from '@/stores/composer'
import { toast } from '@/stores/toast'
import { MenuCheckItem, MenuItem } from '@/ui/Menu'

// Tool server ids may contain ":" (e.g. "mcp:sheets"); keep it literal in paths.
const seg = (id: string) => encodeURIComponent(id).replace(/%3A/gi, ':')

// The OAuth routes take the server's provider id (GET /api/v1/tools/), which
// usually equals the tool server id.
export function authorizeUrl(i: OAuthToolServerIntegration, provider = i.toolServerId): string {
  return `/oauth/clients/${seg(provider)}/authorize?next=${encodeURIComponent(`/?tools=${i.toolServerId}`)}`
}

export function useConnected(i: OAuthToolServerIntegration) {
  const tools = useTools()
  const server = tools.data?.find((t) => t.id === i.toolServerId)
  return { connected: !!server?.authenticated, server, provider: server?.provider || i.toolServerId, loading: tools.isPending }
}

export function OAuthPlusItem({ integration: i }: { integration: OAuthToolServerIntegration }) {
  const { connected, provider } = useConnected(i)
  const selected = useComposer((s) => s.toolIds.includes(i.toolServerId))
  const toggle = useComposer((s) => s.toggleTool)
  const Icon = iconFor(i.icon)
  if (connected)
    return (
      <MenuCheckItem checked={selected} onChange={(v) => toggle(i.toolServerId, v)} testId={`integration-${i.id}`}>
        <Icon size={15} /> {i.label} <span className="ml-auto pl-3 text-xs text-muted">Connected</span>
      </MenuCheckItem>
    )
  return (
    <MenuItem onSelect={() => location.assign(authorizeUrl(i, provider))} testId={`integration-${i.id}`}>
      <Icon size={15} /> {i.label} <span className="ml-auto pl-3 text-xs text-accent">Connect</span>
    </MenuItem>
  )
}

export function OAuthSettingsRow({ integration: i }: { integration: OAuthToolServerIntegration }) {
  const { connected, server, provider, loading } = useConnected(i)
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const Icon = iconFor(i.icon)
  const disconnect = async () => {
    setBusy(true)
    try {
      await api(`/api/v1/oauth/clients/${seg(provider)}/session`, { method: 'DELETE' })
      useComposer.getState().toggleTool(i.toolServerId, false)
      await qc.invalidateQueries({ queryKey: ['tools'] })
      toast(`${i.label} disconnected`)
    } catch (e) {
      toast(`Could not disconnect: ${(e as Error).message}`, 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="flex items-center gap-3 py-3">
      <Icon size={18} className="text-muted" />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{i.label}</p>
        <p className="text-xs text-muted">
          {loading ? 'Checking…' : connected ? (server?.connected_at ? `Connected since ${new Date(server.connected_at * 1000).toLocaleDateString()}` : 'Connected') : 'Not connected'}
        </p>
      </div>
      {connected ? (
        <button type="button" disabled={busy} onClick={disconnect} className="rounded-lg border border-line px-3 py-1.5 text-sm hover:bg-surface-2 disabled:opacity-50">
          Disconnect
        </button>
      ) : (
        <a href={authorizeUrl(i, provider)} className="rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white">
          Connect
        </a>
      )}
    </li>
  )
}
