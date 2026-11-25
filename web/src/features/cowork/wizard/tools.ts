// Which apps a task may use with which assistant, and how the picker groups and describes them.
// The rules are the ones the engine applies: an app that is not connected is skipped, a failing server cannot join,
// and a local-only app joins only runs of a local (on-server) assistant.
import type { ToolServer } from '@/api/types'
import { toolKey } from '@/features/tools/ids'

export type ToolState = 'ready' | 'connect' | 'error' | 'local'

export interface ToolStatus {
  state: ToolState
  /** can be switched on for this assistant */
  selectable: boolean
  /** short chip text */
  label: string
  /** why it cannot be used, or how to fix it */
  reason?: string
  /** the link to offer: Connect for an app not linked yet, Reconnect for a linked one that fails */
  connect?: 'Connect' | 'Reconnect'
}

/** Apps the picker lists: every tool server except the built-in sets, which ride on the assistant. */
export const pickable = (servers: ToolServer[] | undefined): ToolServer[] => (servers ?? []).filter((s) => s.kind !== 'builtin')

const needsConnect = (s: ToolServer) => s.auth_type === 'user_oauth' && !s.authenticated

/** The precedence of the old list: Connect, then Unavailable, then Local assistants only, then Connected. */
export function toolStatus(s: ToolServer, assistantLocal: boolean): ToolStatus {
  const localOnly = !!s.local_only && !assistantLocal
  if (needsConnect(s)) {
    return { state: 'connect', selectable: false, label: 'Connect needed', connect: 'Connect', reason: localOnly ? 'Link your account to use it. It also needs a local assistant.' : 'Link your account to use it.' }
  }
  if (s.error) {
    const reason = s.error === 'unavailable' ? 'It is not answering right now.' : s.error
    return { state: 'error', selectable: false, label: 'Unavailable', connect: s.auth_type === 'user_oauth' ? 'Reconnect' : undefined, reason }
  }
  if (localOnly) {
    return { state: 'local', selectable: false, label: 'Local assistants only', reason: 'Its data stays on this server, so it works only with a local assistant. With this one the run would fail.' }
  }
  return { state: 'ready', selectable: true, label: s.auth_type === 'user_oauth' ? 'Connected' : 'Ready' }
}

export interface ToolGroup {
  id: 'accounts' | 'shared'
  title: string
  hint: string
  items: ToolServer[]
}

/** Accounts the person links themselves (they run as that person), then tools an admin set up for everyone. Empty groups are left out; inside a group the apps that can be switched on come first. */
export function groupTools(list: ToolServer[], assistantLocal = false): ToolGroup[] {
  const usableFirst = (items: ToolServer[]) => [...items].sort((a, b) => Number(toolStatus(b, assistantLocal).selectable) - Number(toolStatus(a, assistantLocal).selectable))
  const groups: ToolGroup[] = [
    { id: 'accounts', title: 'Your accounts', hint: 'Apps you link with your own sign-in. The task works as you.', items: usableFirst(list.filter((s) => s.auth_type === 'user_oauth')) },
    { id: 'shared', title: 'Shared tools', hint: 'Set up for everyone by an admin.', items: usableFirst(list.filter((s) => s.auth_type !== 'user_oauth')) },
  ]
  return groups.filter((g) => g.items.length > 0)
}

/** Apps whose name, description or function names contain every word of the query. */
export function searchTools(list: ToolServer[], query: string, label: (s: ToolServer) => string = (s) => s.name): ToolServer[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (!words.length) return list
  return list.filter((s) => {
    const hay = [label(s), s.name, s.description ?? '', ...(s.functions ?? [])].join(' ').toLowerCase()
    return words.every((w) => hay.includes(w))
  })
}

/** The selected ids with those that cannot join this run taken out. Ids the picker does not list (built-in sets, assistant-only servers) stay as they are. */
export function effectiveToolIds(toolIds: string[], servers: ToolServer[] | undefined, assistantLocal: boolean): string[] {
  const listed = new Map(pickable(servers).map((s) => [s.id, s]))
  return toolIds.filter((id) => {
    const s = listed.get(toolKey(id))
    return !s || toolStatus(s, assistantLocal).selectable
  })
}

/** The listed apps that are switched on and can join this run. */
export function selectedApps(toolIds: string[], servers: ToolServer[] | undefined, assistantLocal: boolean): ToolServer[] {
  const on = new Set(toolIds.map(toolKey))
  return pickable(servers).filter((s) => on.has(s.id) && toolStatus(s, assistantLocal).selectable)
}

/** Selected ids that the chosen assistant or the app's state rules out (they are left out of the task). */
export function leftOut(toolIds: string[], servers: ToolServer[] | undefined, assistantLocal: boolean): ToolServer[] {
  const on = new Set(toolIds.map(toolKey))
  return pickable(servers).filter((s) => on.has(s.id) && !toolStatus(s, assistantLocal).selectable)
}

export type CheckResult = { id: string; name: string; ok: boolean; text: string; connect?: 'Connect' | 'Reconnect' }

/** One line per app that is switched on, from a fresh listing: does it answer, and is it usable with this assistant. */
export function checkConnections(toolIds: string[], servers: ToolServer[] | undefined, assistantLocal: boolean): CheckResult[] {
  const on = new Set(toolIds.map(toolKey))
  return pickable(servers)
    .filter((s) => on.has(s.id))
    .map((s) => {
      const st = toolStatus(s, assistantLocal)
      const base = { id: s.id, name: s.name, connect: st.connect }
      if (st.state === 'connect') return { ...base, ok: false, text: 'Not connected' }
      if (st.state === 'error') return { ...base, ok: false, text: s.auth_type === 'user_oauth' ? 'Needs reconnecting, or is not answering' : 'Not answering' }
      if (st.state === 'local') return { ...base, ok: false, text: 'Needs a local assistant' }
      if (!(s.functions?.length ?? 0)) return { ...base, ok: false, text: 'Connected, but offers no tools right now' }
      const n = s.functions!.length
      return { ...base, ok: true, text: `OK, ${n} tool${n === 1 ? '' : 's'} available` }
    })
}
