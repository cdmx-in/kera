// Reusable access editor: who may read or write one resource
// (GET/PUT /api/v1/grants/{type}/{id}). Used by the model and tool-server
// admin screens; exported for the knowledge screens.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Globe, Users as GroupIcon, X } from 'lucide-react'
import { api } from '@/api/client'
import { IconButton } from '@/ui/Dialog'
import { enc, useGroups, type AdminUser, type Grant } from './api'
import { UserPicker } from './UserPicker'
import { UserAvatar } from './Users'
import { attempt, Btn, Loading } from './ui'
import { collapse } from './lib'
import { Select } from '@/ui/Select'

export type ResourceType = 'model' | 'tool_server' | 'knowledge' | 'prompt' | 'skill'

const permissionOptions = [
  { value: 'read', label: 'can use' },
  { value: 'write', label: 'can edit' },
]

export function AccessEditor({ type, id, hint }: { type: ResourceType; id: string; hint?: string }) {
  const path = `/api/v1/grants/${type}/${enc(id)}`
  const qc = useQueryClient()
  const stored = useQuery({ queryKey: ['admin', 'grants', type, id], queryFn: async () => collapse(await api<Grant[]>(path)) })
  const groups = useGroups()
  const [draft, setDraft] = useState<Grant[] | null>(null)
  const grants = draft ?? stored.data ?? []
  const has = (p: string) => grants.some((g) => g.principal === p)
  const add = (principal: string) => !has(principal) && setDraft([...grants, { principal, permission: 'read' }])
  const save = () =>
    attempt(async () => {
      const out = collapse(await api<Grant[]>(path, { method: 'PUT', json: grants }))
      qc.setQueryData(['admin', 'grants', type, id], out)
      setDraft(null)
    }, 'Access saved')
  const freeGroups = (groups.data ?? []).filter((g) => !has(`group:${g.id}`))

  return (
    <div className="space-y-3" data-testid="access-editor">
      {hint && <p className="text-xs text-muted">{hint}</p>}
      <Loading q={stored} />
      <ul className="divide-y divide-line rounded-lg border border-line">
        {grants.map((g, i) => (
          <li key={g.principal} className="flex items-center gap-2 px-3 py-1.5 text-sm" data-testid="grant-row">
            <Principal p={g.principal} groupName={(gid) => groups.data?.find((x) => x.id === gid)?.name} />
            <Select
              aria-label={`Permission for ${g.principal}`}
              value={g.permission}
              onChange={(v) => setDraft(grants.map((x, j) => (j === i ? { ...x, permission: v as Grant['permission'] } : x)))}
              options={permissionOptions}
              size="sm"
              className="w-auto"
            />
            <IconButton label={`Remove ${g.principal}`} onClick={() => setDraft(grants.filter((_, j) => j !== i))}>
              <X size={14} />
            </IconButton>
          </li>
        ))}
        {stored.isSuccess && !grants.length && <li className="p-3 text-sm text-muted">Only admins have access.</li>}
      </ul>
      <div className="grid gap-2 sm:grid-cols-[auto_1fr_1fr]">
        <Btn disabled={has('user:*')} onClick={() => add('user:*')}>
          <Globe size={14} /> Everyone
        </Btn>
        <Select aria-label="Add a group" value="" placeholder="Add a group…" onChange={(v) => v && add(`group:${v}`)} options={freeGroups.map((g) => ({ value: g.id, label: g.name }))} disabled={!freeGroups.length} />
        <UserPicker placeholder="Add a user…" exclude={grants.filter((g) => g.principal.startsWith('user:')).map((g) => g.principal.slice(5))} onPick={(u) => add(`user:${u.id}`)} />
      </div>
      <div className="flex justify-end gap-2">
        {draft && <Btn onClick={() => setDraft(null)}>Discard</Btn>}
        <Btn variant="primary" disabled={!draft} onClick={() => void save()} data-testid="save-access">
          Save access
        </Btn>
      </div>
    </div>
  )
}

function Principal({ p, groupName }: { p: string; groupName: (id: string) => string | undefined }) {
  if (p === 'user:*')
    return (
      <span className="flex min-w-0 flex-1 items-center gap-2">
        <Globe size={16} className="text-muted" /> Everyone
      </span>
    )
  if (p.startsWith('group:'))
    return (
      <span className="flex min-w-0 flex-1 items-center gap-2">
        <GroupIcon size={16} className="text-muted" />
        <span className="truncate">{groupName(p.slice(6)) ?? p.slice(6)}</span>
      </span>
    )
  return <UserLabel id={p.slice(5)} />
}

function UserLabel({ id }: { id: string }) {
  const u = useQuery({ queryKey: ['admin', 'user', id], queryFn: () => api<AdminUser>(`/api/v1/users/${enc(id)}`), staleTime: 60_000, retry: false })
  return (
    <span className="flex min-w-0 flex-1 items-center gap-2">
      <UserAvatar id={id} size={20} />
      <span className="truncate">{u.data ? `${u.data.name} (${u.data.email})` : u.isError ? `Deleted user ${id}` : id}</span>
    </span>
  )
}
