import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Pencil, Search, Trash2 } from 'lucide-react'
import { api, type ApiError } from '@/api/client'
import { toast } from '@/stores/toast'
import { useMe } from '@/api/queries'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Tip } from '@/ui/misc'
import { cx } from '@/lib/format'
import { enc, useGroups, useSettings, useSaveSettings, type AdminUser, type RoleName } from './api'
import { ago, errorField, NAME_MAX, validateUser, type FieldErrors, type Perms } from './lib'
import { PermSwitches } from './Perms'
import { Pagination, useUrlPager } from './Pagination'
import { attempt, Badge, Btn, Card, Confirm, Field, inputCls, Loading, ManagedBadge, managedWarning, Page, scrollCls, tdCls, theadCls, thCls } from './ui'
import { Select } from '@/ui/Select'
import { Menu, MenuCheckItem, MenuContent, MenuLabel, MenuTrigger } from '@/ui/Menu'

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

export type UserSort = { key: string; desc: boolean }

/** One page of users matching query (server-side paging and sorting; size omitted = the server default, sort omitted = signup order). */
export function useUserSearch(query: string, page = 1, size?: number, sort?: UserSort) {
  return useQuery({
    queryKey: ['admin', 'users', query, page, size, sort],
    queryFn: () =>
      api<{ users: AdminUser[]; total: number }>(
        `/api/v1/users/?query=${enc(query)}&page=${page}${size ? `&size=${size}` : ''}${sort ? `&sort=${sort.key}&order=${sort.desc ? 'desc' : 'asc'}` : ''}`,
      ),
    placeholderData: (prev) => prev,
  })
}

export function UsersPage() {
  const [q, setQ] = useState('')
  const pager = useUrlPager()
  const query = useDebounced(q.trim())
  const [sort, setSort] = useState<UserSort | undefined>()
  const users = useUserSearch(query, pager.page, pager.size, sort)
  const groups = useGroups().data ?? []
  const me = useMe().data
  const qc = useQueryClient()
  const [editing, setEditing] = useState<AdminUser | null>(null)
  const [deleting, setDeleting] = useState<AdminUser | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'users'] })

  const setRole = (u: AdminUser, role: RoleName) =>
    attempt(async () => {
      await api(`/api/v1/users/${enc(u.id)}/update`, { method: 'POST', json: { role } })
      await refresh()
    }, `${u.name || u.email} is now ${role}`)

  const setMember = (u: AdminUser, g: { id: string; name: string }, on: boolean) =>
    attempt(async () => {
      await api(`/api/v1/groups/id/${enc(g.id)}/users/${on ? 'add' : 'remove'}`, { method: 'POST', json: { user_ids: [u.id] } })
      await Promise.all([refresh(), qc.invalidateQueries({ queryKey: ['admin', 'groups'] })])
    }, `${u.name || u.email} ${on ? 'added to' : 'removed from'} ${g.name}`)

  const head = (key: string, label: string, right = false) => (
    <th className={cx(thCls, right && 'text-right')} aria-sort={sort?.key === key ? (sort.desc ? 'descending' : 'ascending') : undefined}>
      <button type="button" className="hover:text-fg" onClick={() => (setSort({ key, desc: sort?.key === key ? !sort.desc : key === 'last_active' || key === 'api_keys' }), pager.setPage(1, true))}>
        {label}
        {sort?.key === key ? (sort.desc ? ' ▾' : ' ▴') : ''}
      </button>
    </th>
  )

  return (
    <Page
      title="Users"
      actions={
        <label className="relative w-full sm:w-72">
          <Search size={15} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
          <input
            type="search"
            value={q}
            onChange={(e) => (setQ(e.target.value), pager.setPage(1, true))}
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
          <div className={scrollCls}>
            <table className="w-full text-sm" data-testid="users-table">
              <thead className={theadCls}>
                <tr>
                  {head('name', 'User')}
                  {head('role', 'Role')}
                  <th className={thCls}>Groups</th>
                  {head('last_active', 'Last active')}
                  {head('api_keys', 'API keys', true)}
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
                              {u.disabled_at && (
                                <Badge tone="red" title={`Disabled ${new Date(u.disabled_at * 1000).toLocaleString()}${u.origin === 'gworkspace' ? ' by the Google Workspace sync' : ''}`}>
                                  disabled
                                </Badge>
                              )}
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
                        <Menu>
                          <MenuTrigger
                            aria-label={`Groups of ${u.email}`}
                            className="group flex max-w-64 cursor-pointer flex-wrap items-center gap-1 rounded-lg p-1 outline-none hover:bg-surface-2 focus-visible:ring-2 focus-visible:ring-accent"
                          >
                            {u.groups.length ? u.groups.map((g) => <Badge key={g.id}>{g.name}</Badge>) : <span className="text-muted">–</span>}
                            <ChevronDown size={13} className="text-muted" />
                          </MenuTrigger>
                          <MenuContent>
                            <MenuLabel>Groups</MenuLabel>
                            {groups.map((g) => (
                              <MenuCheckItem key={g.id} checked={u.groups.some((x) => x.id === g.id)} onChange={(on) => void setMember(u, g, on)}>
                                {g.name}
                              </MenuCheckItem>
                            ))}
                            {!groups.length && <p className="px-2.5 py-1.5 text-muted">No groups yet.</p>}
                          </MenuContent>
                        </Menu>
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
        {users.data && <Pagination pager={pager} total={users.data.total} label="Users pages" />}
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
  const [touched, setTouched] = useState(false)
  const [server, setServer] = useState<FieldErrors>({})
  const errors = validateUser({ name, role }, { role: user.role, self })
  const valid = !Object.keys(errors).length
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return setTouched(true)
    const body: Record<string, string> = { name: name.trim() }
    if (role !== user.role) body.role = role
    try {
      await api(`/api/v1/users/${enc(user.id)}/update`, { method: 'POST', json: body })
    } catch (err) {
      const field = errorField((err as ApiError).code)
      if (field) return setServer({ [field]: (err as Error).message })
      return toast((err as Error).message, 'error')
    }
    toast('User saved')
    onSaved()
    onClose()
  }
  return (
    <Dialog open onClose={onClose} label="Edit user" className="w-[min(28rem,calc(100vw-2rem))]">
      <DialogHeader title={`Edit ${user.email}`} onClose={onClose} />
      <form onSubmit={save} noValidate className="space-y-4 p-4">
        <Field label="Name" error={server.name ?? (touched ? errors.name : undefined)}>
          <input
            value={name}
            onChange={(e) => (setName(e.target.value), setTouched(true), setServer({ ...server, name: undefined }))}
            maxLength={NAME_MAX}
            aria-invalid={!!(server.name || (touched && errors.name))}
            className={inputCls}
            data-testid="user-name"
          />
        </Field>
        <Field
          label="Role"
          error={server.role ?? errors.role}
          hint={self ? 'You cannot change your own role.' : user.role === 'admin' && role !== 'admin' ? 'At least one admin must remain: the last admin cannot be demoted.' : 'At least one admin must remain.'}
        >
          <Select value={role} disabled={self} onChange={(v) => (setRole(v as RoleName), setServer({ ...server, role: undefined }))} options={roleOptions} aria-label="Role" />
        </Field>
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary" disabled={!valid} data-testid="save-user">
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
