import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, Trash2, Users as UsersIcon, X } from 'lucide-react'
import { api } from '@/api/client'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { enc, useGroups, type AdminUser, type Group } from './api'
import type { Perms } from './lib'
import { PermSwitches } from './Perms'
import { UserPicker } from './UserPicker'
import { UserAvatar, useKnownPerms } from './Users'
import { attempt, Badge, Btn, Card, Confirm, Field, inputCls, Loading, Page } from './ui'

export function GroupsPage() {
  const groups = useGroups()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Group | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Group | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin'] })
  return (
    <Page
      title="Groups"
      actions={
        <Btn variant="primary" onClick={() => setEditing('new')} data-testid="new-group">
          <Plus size={15} /> New group
        </Btn>
      }
    >
      <Loading q={groups} />
      {groups.data && !groups.data.length && <p className="text-sm text-muted">No groups yet. Groups grant extra permissions and access to models, tools and knowledge.</p>}
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {groups.data?.map((g) => {
          const extra = Object.values(g.permissions ?? {}).flatMap((c) => Object.values(c ?? {})).filter(Boolean).length
          return (
            <Card key={g.id} className="flex flex-col p-4" >
              <div className="flex items-start gap-2" data-testid="group-card">
                <div className="min-w-0 flex-1">
                  <h3 className="truncate font-semibold">{g.name}</h3>
                  <p className="line-clamp-2 text-sm text-muted">{g.description || 'No description'}</p>
                </div>
                <IconButton label={`Edit ${g.name}`} onClick={() => setEditing(g)}>
                  <Pencil size={15} />
                </IconButton>
                <IconButton label={`Delete ${g.name}`} onClick={() => setDeleting(g)} className="hover:text-red-600">
                  <Trash2 size={15} />
                </IconButton>
              </div>
              <div className="mt-3 flex flex-wrap gap-1.5">
                <Badge>
                  <UsersIcon size={11} /> {g.member_count} member{g.member_count === 1 ? '' : 's'}
                </Badge>
                {extra > 0 && <Badge tone="accent">+{extra} permission{extra === 1 ? '' : 's'}</Badge>}
              </div>
            </Card>
          )
        })}
      </div>
      {editing && <EditGroup group={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onSaved={refresh} />}
      <Confirm
        open={!!deleting}
        title="Delete group"
        body={
          <p>
            Delete <b>{deleting?.name}</b>? Its {deleting?.member_count} members lose the permissions and access it grants.
          </p>
        }
        onClose={() => setDeleting(null)}
        onConfirm={async () => {
          await api(`/api/v1/groups/id/${enc(deleting!.id)}/delete`, { method: 'DELETE' })
          await refresh()
        }}
      />
    </Page>
  )
}

function EditGroup({ group, onClose, onSaved }: { group: Group | null; onClose: () => void; onSaved: () => void }) {
  const known = useKnownPerms()
  const full = useQuery({
    queryKey: ['admin', 'group', group?.id],
    queryFn: () => api<Group>(`/api/v1/groups/id/${enc(group!.id)}`),
    enabled: !!group,
  })
  const [name, setName] = useState(group?.name ?? '')
  const [description, setDescription] = useState(group?.description ?? '')
  const [perms, setPerms] = useState<Perms>(group?.permissions ?? {})
  const [added, setAdded] = useState<AdminUser[]>([])
  const [removed, setRemoved] = useState<string[]>([])
  const memberIDs = [...(full.data?.user_ids ?? []).filter((id) => !removed.includes(id)), ...added.map((u) => u.id)]

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    const ok = await attempt(async () => {
      const body = { name, description, permissions: perms }
      const g = group
        ? await api<Group>(`/api/v1/groups/id/${enc(group.id)}/update`, { method: 'POST', json: body })
        : await api<Group>('/api/v1/groups/create', { method: 'POST', json: body })
      if (added.length) await api(`/api/v1/groups/id/${enc(g.id)}/users/add`, { method: 'POST', json: { user_ids: added.map((u) => u.id) } })
      if (removed.length) await api(`/api/v1/groups/id/${enc(g.id)}/users/remove`, { method: 'POST', json: { user_ids: removed } })
    }, 'Group saved')
    if (ok) {
      onSaved()
      onClose()
    }
  }

  return (
    <Dialog open onClose={onClose} label={group ? 'Edit group' : 'New group'} className="w-[min(48rem,calc(100vw-2rem))]">
      <DialogHeader title={group ? `Edit ${group.name}` : 'New group'} onClose={onClose} />
      <form onSubmit={save} className="max-h-[calc(100dvh-8rem)] space-y-5 overflow-y-auto p-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Name">
            <input value={name} onChange={(e) => setName(e.target.value)} required className={inputCls} data-testid="group-name" />
          </Field>
          <Field label="Description">
            <input value={description} onChange={(e) => setDescription(e.target.value)} className={inputCls} />
          </Field>
        </div>
        <section className="space-y-2">
          <h3 className="text-sm font-semibold">Permission overrides</h3>
          <p className="text-xs text-muted">Members get these on top of the default permissions.</p>
          <PermSwitches known={known} value={perms} onChange={setPerms} idPrefix="group-perm" />
        </section>
        <section className="space-y-2">
          <h3 className="text-sm font-semibold">Members ({memberIDs.length})</h3>
          <UserPicker exclude={memberIDs} onPick={(u) => (removed.includes(u.id) ? setRemoved(removed.filter((x) => x !== u.id)) : setAdded([...added, u]))} />
          <Loading q={full} />
          <ul className="max-h-64 divide-y divide-line overflow-y-auto rounded-lg border border-line">
            {memberIDs.map((id) => (
              <Member key={id} id={id} fresh={added.find((u) => u.id === id)} onRemove={() => (added.some((u) => u.id === id) ? setAdded(added.filter((u) => u.id !== id)) : setRemoved([...removed, id]))} />
            ))}
            {!memberIDs.length && <li className="p-3 text-sm text-muted">No members.</li>}
          </ul>
        </section>
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary" data-testid="save-group">
            Save
          </Btn>
        </div>
      </form>
    </Dialog>
  )
}

function Member({ id, fresh, onRemove }: { id: string; fresh?: AdminUser; onRemove: () => void }) {
  const u = useQuery({ queryKey: ['admin', 'user', id], queryFn: () => api<AdminUser>(`/api/v1/users/${enc(id)}`), enabled: !fresh, staleTime: 60_000 })
  const user = fresh ?? u.data
  return (
    <li className="flex items-center gap-2 px-3 py-1.5 text-sm">
      <UserAvatar id={id} size={22} />
      <span className="min-w-0 flex-1 truncate">
        {user?.name ?? id} <span className="text-muted">{user?.email}</span>
      </span>
      {fresh && <Badge tone="green">new</Badge>}
      <IconButton label={`Remove ${user?.email ?? id}`} onClick={onRemove}>
        <X size={14} />
      </IconButton>
    </li>
  )
}
