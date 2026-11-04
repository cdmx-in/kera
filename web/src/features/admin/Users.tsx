import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, Pencil, Search, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useMe } from '@/api/queries'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Tip } from '@/ui/misc'
import { cx } from '@/lib/format'
import { enc, useSettings, useSaveSettings, type AdminUser, type RoleName } from './api'
import { ago, type Perms } from './lib'
import { PermSwitches } from './Perms'
import { attempt, Badge, Btn, Card, Confirm, Field, inputCls, Loading, ManagedBadge, managedWarning, Page, tdCls, thCls } from './ui'
import { Select } from '@/ui/Select'

const PER_PAGE = 30
const roles: RoleName[] = ['admin', 'user', 'pending']
const roleOptions = roles.map((r) => ({ value: r, label: r[0].toUpperCase() + r.slice(1) }))

export function avatarURL(id: string) {
  return `/api/v1/users/${enc(id)}/profile/image`
}

export function UserAvatar({ id, size = 28 }: { id: string; size?: number }) {
  return <img src={avatarURL(id)} alt="" loading="lazy" width={size} height={size} className="shrink-0 rounded-full bg-surface-2 object-cover" style={{ width: size, height: size }} />
}

/** Debounced value. */
export function useDebounced<T>(v: T, ms = 250): T {
  const [d, set] = useState(v)
  useEffect(() => {
    const t = setTimeout(() => set(v), ms)
    return () => clearTimeout(t)
  }, [v, ms])
  return d
}

export function useUserSearch(query: string, page = 1) {
  return useQuery({
    queryKey: ['admin', 'users', query, page],
    queryFn: () => api<{ users: AdminUser[]; total: number }>(`/api/v1/users/?query=${enc(query)}&page=${page}`),
    placeholderData: (prev) => prev,
  })
}

export function UsersPage() {
  const [q, setQ] = useState('')
  const [page, setPage] = useState(1)
  const query = useDebounced(q.trim())
  const users = useUserSearch(query, page)
  const me = useMe().data
  const qc = useQueryClient()
  const [editing, setEditing] = useState<AdminUser | null>(null)
  const [deleting, setDeleting] = useState<AdminUser | null>(null)
  const total = users.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / PER_PAGE))
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'users'] })

  const setRole = (u: AdminUser, role: RoleName) =>
    attempt(async () => {
      await api(`/api/v1/users/${enc(u.id)}/update`, { method: 'POST', json: { role } })
      await refresh()
    }, `${u.name || u.email} is now ${role}`)

  return (
    <Page
      title="Users"
      actions={
        <label className="relative w-full sm:w-72">
          <Search size={15} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
          <input
            type="search"
            value={q}
            onChange={(e) => (setQ(e.target.value), setPage(1))}
            placeholder="Search name or email"
            aria-label="Search users"
            className={cx(inputCls, 'pl-8')}
            data-testid="user-search"
          />
        </label>
      }
    >
      <Card>
        <Loading q={users} />
        {users.data && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm" data-testid="users-table">
              <thead className="border-b border-line">
                <tr>
                  <th className={thCls}>User</th>
                  <th className={thCls}>Role</th>
                  <th className={thCls}>Groups</th>
                  <th className={thCls}>Last active</th>
                  <th className={cx(thCls, 'text-right')}>API keys</th>
                  <th className={thCls}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {users.data.users.map((u) => {
                  const self = u.id === me?.id
                  return (
                    <tr key={u.id} data-testid="user-row">
                      <td className={tdCls}>
                        <div className="flex min-w-48 items-center gap-2.5">
                          <UserAvatar id={u.id} />
                          <div className="min-w-0">
                            <div className="truncate font-medium">
                              {u.name || '(no name)'} {self && <Badge>you</Badge>}
                            </div>
                            <div className="truncate text-xs text-muted">{u.email}</div>
                          </div>
                        </div>
                      </td>
                      <td className={tdCls}>
                        {self ? (
                          <Tip text="You cannot change your own role">
                            <span>
                              <Badge tone="accent"><span className="capitalize">{u.role}</span></Badge>
                            </span>
                          </Tip>
                        ) : (
                          <Select
                            aria-label={`Role of ${u.email}`}
                            value={u.role}
                            onChange={(v) => void setRole(u, v as RoleName)}
                            data-role={u.role}
                            options={roleOptions}
                            variant="pill"
                            size="sm"
                            tone={u.role === 'admin' ? 'accent' : 'grey'}
                          />
                        )}
                      </td>
                      <td className={tdCls}>
                        <div className="flex max-w-64 flex-wrap gap-1">
                          {u.groups.length ? u.groups.map((g) => <Badge key={g.id}>{g.name}</Badge>) : <span className="text-muted">–</span>}
                        </div>
                      </td>
                      <td className={cx(tdCls, 'whitespace-nowrap text-muted')} title={new Date(u.last_active_at * 1000).toLocaleString()}>
                        {ago(u.last_active_at)}
                      </td>
                      <td className={cx(tdCls, 'text-right tabular-nums')}>{u.api_key_count}</td>
                      <td className={cx(tdCls, 'whitespace-nowrap text-right')}>
                        <IconButton label={`Edit ${u.email}`} onClick={() => setEditing(u)}>
                          <Pencil size={15} />
                        </IconButton>
                        <IconButton label={self ? 'You cannot delete yourself' : `Delete ${u.email}`} disabled={self} onClick={() => setDeleting(u)} className="hover:text-red-600">
                          <Trash2 size={15} />
                        </IconButton>
                      </td>
                    </tr>
                  )
                })}
                {!users.data.users.length && (
                  <tr>
                    <td colSpan={6} className="p-6 text-center text-muted">
                      No users match.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}
        <footer className="flex items-center justify-between gap-2 border-t border-line px-4 py-2 text-xs text-muted">
          <span>
            {total} user{total === 1 ? '' : 's'}
          </span>
          <span className="flex items-center gap-1">
            <IconButton label="Previous page" disabled={page <= 1} onClick={() => setPage(page - 1)}>
              <ChevronLeft size={16} />
            </IconButton>
            Page {page} of {pages}
            <IconButton label="Next page" disabled={page >= pages} onClick={() => setPage(page + 1)}>
              <ChevronRight size={16} />
            </IconButton>
          </span>
        </footer>
      </Card>
      <DefaultPermissions />
      {editing && <EditUser user={editing} self={editing.id === me?.id} onClose={() => setEditing(null)} onSaved={refresh} />}
      <Confirm
        open={!!deleting}
        title="Delete user"
        body={
          <p>
            Delete <b>{deleting?.name || deleting?.email}</b> ({deleting?.email})? Their chats, files, API keys and memories are deleted too. This cannot be undone.
          </p>
        }
        onClose={() => setDeleting(null)}
        onConfirm={async () => {
          await api(`/api/v1/users/${enc(deleting!.id)}`, { method: 'DELETE' })
          await refresh()
        }}
      />
    </Page>
  )
}

function EditUser({ user, self, onClose, onSaved }: { user: AdminUser; self: boolean; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(user.name)
  const [role, setRole] = useState(user.role)
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    const body: Record<string, string> = { name }
    if (role !== user.role) body.role = role
    if (await attempt(() => api(`/api/v1/users/${enc(user.id)}/update`, { method: 'POST', json: body }), 'User saved')) {
      onSaved()
      onClose()
    }
  }
  return (
    <Dialog open onClose={onClose} label="Edit user" className="w-[min(28rem,calc(100vw-2rem))]">
      <DialogHeader title={`Edit ${user.email}`} onClose={onClose} />
      <form onSubmit={save} className="space-y-4 p-4">
        <Field label="Name">
          <input value={name} onChange={(e) => setName(e.target.value)} required className={inputCls} />
        </Field>
        <Field label="Role" hint={self ? 'You cannot change your own role.' : 'At least one admin must remain.'}>
          <Select value={role} disabled={self} onChange={(v) => setRole(v as RoleName)} options={roleOptions} aria-label="Role" />
        </Field>
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary">
            Save
          </Btn>
        </div>
      </form>
    </Dialog>
  )
}

/** The known permission tree: the built-in default of user.permissions. */
export function useKnownPerms(): Perms {
  const s = useSettings().data?.get('user.permissions')
  return (s?.default as Perms) ?? {}
}

function DefaultPermissions() {
  const known = useKnownPerms()
  const setting = useSettings().data?.get('user.permissions')
  const saveSettings = useSaveSettings()
  const current = useQuery({ queryKey: ['admin', 'default-perms'], queryFn: () => api<Perms>('/api/v1/users/default/permissions') })
  const [draft, setDraft] = useState<Perms | null>(null)
  const value = draft ?? current.data ?? {}
  const save = async () => {
    if (await attempt(() => saveSettings({ 'user.permissions': value }), 'Default permissions saved')) {
      setDraft(null)
      await current.refetch()
    }
  }
  return (
    <Card
      title={<span className="flex items-center gap-2">Default permissions {setting?.managed && <ManagedBadge />}</span>}
      hint="Every non-admin user has these. Groups can add permissions, never remove them."
      actions={
        <>
          {draft && <Btn onClick={() => setDraft(null)}>Discard</Btn>}
          <Btn variant="primary" disabled={!draft} onClick={save} data-testid="save-default-perms">
            Save
          </Btn>
        </>
      }
    >
      <div className="space-y-3 p-4">
        <Loading q={current} />
        {draft && setting?.managed && <p className="text-xs text-amber-700 dark:text-amber-400">{managedWarning}</p>}
        <PermSwitches known={known} value={value} onChange={setDraft} idPrefix="default-perm" />
      </div>
    </Card>
  )
}
