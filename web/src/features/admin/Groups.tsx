import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { BarChart3, Briefcase, Code2, Crown, FlaskConical, HeartHandshake, LifeBuoy, Megaphone, Palette, Pencil, Plus, Search, Server, ShieldCheck, Trash2, Users as UsersIcon, Wallet, X } from 'lucide-react'
import { api, type ApiError } from '@/api/client'
import { cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { enc, useGroups, type AdminUser, type Group } from './api'
import { DESCRIPTION_MAX, errorField, groupIcon, NAME_MAX, pageSlice, permChips, validateGroup, type FieldErrors, type Perms } from './lib'
import { Pagination, useLocalPager, useUrlPager } from './Pagination'
import { PermSwitches } from './Perms'
import { UserPicker } from './UserPicker'
import { UserAvatar, useKnownPerms } from './Users'
import { Badge, Btn, Card, Confirm, Field, inputCls, Loading, Page, scrollCls, tdCls, theadCls, thCls } from './ui'

const icons: Record<string, typeof UsersIcon> = {
  shield: ShieldCheck,
  code: Code2,
  flask: FlaskConical,
  palette: Palette,
  briefcase: Briefcase,
  lifebuoy: LifeBuoy,
  wallet: Wallet,
  heart: HeartHandshake,
  megaphone: Megaphone,
  server: Server,
  chart: BarChart3,
  crown: Crown,
  users: UsersIcon,
}

// ponytail: the icon is guessed from the name (lib.groupIcon); add a stored per-group icon if admins want to pick one.
function GroupIcon({ name }: { name: string }) {
  const Icon = icons[groupIcon(name)] ?? UsersIcon
  return (
    <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-accent-soft text-accent dark:text-accent-bright" aria-hidden>
      <Icon size={17} />
    </span>
  )
}

export function GroupsPage() {
  const groups = useGroups()
  const pager = useUrlPager()
  const qc = useQueryClient()
  const [q, setQ] = useState('')
  const [editing, setEditing] = useState<Group | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Group | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin'] })
  // ponytail: client-side search, groups are few; move it server-side next to /users/?query= if lists reach thousands.
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return (groups.data ?? []).filter((g) => !needle || `${g.name} ${g.description}`.toLowerCase().includes(needle))
  }, [groups.data, q])
  return (
    <Page
      title="Groups"
      actions={
        <>
          <label className="relative w-full sm:w-72">
            <Search size={15} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
            <input
              type="search"
              value={q}
              onChange={(e) => (setQ(e.target.value), pager.setPage(1, true))}
              placeholder="Search name or description"
              aria-label="Search groups"
              className={cx(inputCls, 'pl-8')}
              data-testid="group-search"
            />
          </label>
          <Btn variant="primary" onClick={() => setEditing('new')} data-testid="new-group">
            <Plus size={15} /> New group
          </Btn>
        </>
      }
    >
      <Card>
        <Loading q={groups} />
        {groups.data && !groups.data.length && <p className="p-6 text-center text-sm text-muted">No groups yet. Groups grant extra permissions and access to models, tools and knowledge.</p>}
        {!!groups.data?.length && (
          <>
            <div className={scrollCls}>
              <table className="w-full text-sm" data-testid="groups-table">
                <thead className={theadCls}>
                  <tr>
                    <th className={thCls}>Group</th>
                    <th className={cx(thCls, 'hidden md:table-cell')}>Description</th>
                    <th className={thCls}>Members</th>
                    <th className={cx(thCls, 'hidden lg:table-cell')}>Permissions</th>
                    <th className={thCls}>
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {pageSlice(rows, pager.page, pager.size).map((g) => {
                    const chips = permChips(g.permissions)
                    return (
                      <tr key={g.id} data-testid="group-card">
                        <td className={tdCls}>
                          <div className="flex items-center gap-2.5 sm:min-w-40">
                            <GroupIcon name={g.name} />
                            <div className="min-w-0 max-w-28 sm:max-w-none">
                              <div className="truncate font-medium">{g.name}</div>
                              <div className="truncate text-xs text-muted md:hidden">{g.description || '—'}</div>
                            </div>
                          </div>
                        </td>
                        <td className={cx(tdCls, 'hidden max-w-sm text-muted md:table-cell')}>
                          <span className="line-clamp-2">{g.description || '—'}</span>
                        </td>
                        <td className={tdCls}>
                          <div className="flex items-center gap-2 whitespace-nowrap">
                            {!!g.member_preview?.length && (
                              <span className="hidden -space-x-2 sm:flex" aria-hidden>
                                {g.member_preview.map((id) => (
                                  <span key={id} className="shrink-0 rounded-full ring-2 ring-surface">
                                    <UserAvatar id={id} size={22} />
                                  </span>
                                ))}
                              </span>
                            )}
                            <UsersIcon size={13} className="text-muted sm:hidden" aria-hidden />
                            <span className="tabular-nums text-muted">
                              {g.member_count}
                              <span className="sr-only sm:not-sr-only"> member{g.member_count === 1 ? '' : 's'}</span>
                            </span>
                          </div>
                        </td>
                        <td className={cx(tdCls, 'hidden lg:table-cell')}>
                          <div className="flex max-w-md flex-wrap gap-1">
                            {chips.slice(0, 3).map((c) => (
                              <Badge key={c} tone="accent">
                                {c}
                              </Badge>
                            ))}
                            {chips.length > 3 && <Badge title={chips.slice(3).join(', ')}>+{chips.length - 3} more</Badge>}
                            {!chips.length && <span className="text-muted">Defaults only</span>}
                          </div>
                        </td>
                        <td className={cx(tdCls, 'whitespace-nowrap text-right')}>
                          <IconButton label={`Edit ${g.name}`} onClick={() => setEditing(g)}>
                            <Pencil size={15} />
                          </IconButton>
                          <IconButton label={`Delete ${g.name}`} onClick={() => setDeleting(g)} className="hover:text-red-600">
                            <Trash2 size={15} />
                          </IconButton>
                        </td>
                      </tr>
                    )
                  })}
                  {!rows.length && (
                    <tr>
                      <td colSpan={5} className="p-6 text-center text-muted">
                        No groups match.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
            <Pagination pager={pager} total={rows.length} label="Groups pages" />
          </>
        )}
      </Card>
      {editing && <EditGroup group={editing === 'new' ? null : editing} groups={groups.data ?? []} onClose={() => setEditing(null)} onSaved={refresh} />}
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

function EditGroup({ group, groups, onClose, onSaved }: { group: Group | null; groups: Group[]; onClose: () => void; onSaved: () => void }) {
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
  const [touched, setTouched] = useState(false)
  const [server, setServer] = useState<FieldErrors>({})
  const [busy, setBusy] = useState(false)
  // Newly added members first, so they show on page 1.
  const memberIDs = [...added.map((u) => u.id), ...(full.data?.user_ids ?? []).filter((id) => !removed.includes(id))]
  const pager = useLocalPager()
  const errors = validateGroup({ name, description }, groups, group?.id)
  const valid = !Object.keys(errors).length
  const shown = (f: keyof FieldErrors) => server[f] ?? (touched || f !== 'name' ? errors[f] : undefined)

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return setTouched(true)
    setBusy(true)
    try {
      const body = { name: name.trim(), description, permissions: perms }
      const g = group
        ? await api<Group>(`/api/v1/groups/id/${enc(group.id)}/update`, { method: 'POST', json: body })
        : await api<Group>('/api/v1/groups/create', { method: 'POST', json: body })
      if (added.length) await api(`/api/v1/groups/id/${enc(g.id)}/users/add`, { method: 'POST', json: { user_ids: added.map((u) => u.id) } })
      if (removed.length) await api(`/api/v1/groups/id/${enc(g.id)}/users/remove`, { method: 'POST', json: { user_ids: removed } })
    } catch (err) {
      const field = errorField((err as ApiError).code)
      if (field) setServer({ [field]: (err as Error).message })
      else toast((err as Error).message, 'error')
      return
    } finally {
      setBusy(false)
    }
    toast('Group saved')
    onSaved()
    onClose()
  }

  return (
    <Dialog open onClose={onClose} label={group ? 'Edit group' : 'New group'} className="w-[min(48rem,calc(100vw-2rem))]">
      <DialogHeader title={group ? `Edit ${group.name}` : 'New group'} onClose={onClose} />
      <form onSubmit={save} noValidate className="max-h-[calc(100dvh-8rem)] space-y-5 overflow-y-auto p-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Name" error={shown('name')}>
            <input
              value={name}
              onChange={(e) => (setName(e.target.value), setTouched(true), setServer({ ...server, name: undefined }))}
              maxLength={NAME_MAX}
              aria-invalid={!!shown('name')}
              className={inputCls}
              data-testid="group-name"
            />
          </Field>
          <Field label="Description" error={shown('description')}>
            <input
              value={description}
              onChange={(e) => (setDescription(e.target.value), setServer({ ...server, description: undefined }))}
              maxLength={DESCRIPTION_MAX}
              aria-invalid={!!shown('description')}
              className={inputCls}
            />
          </Field>
        </div>
        <section className="space-y-2">
          <h3 className="text-sm font-semibold">Permission overrides</h3>
          <p className="text-xs text-muted">Members get these on top of the default permissions.</p>
          {server.permissions && (
            <p role="alert" className="text-xs text-red-600 dark:text-red-400">
              {server.permissions}
            </p>
          )}
          <PermSwitches known={known} value={perms} onChange={(p) => (setPerms(p), setServer({ ...server, permissions: undefined }))} idPrefix="group-perm" />
        </section>
        <section className="space-y-2">
          <h3 className="text-sm font-semibold">Members ({memberIDs.length})</h3>
          <UserPicker exclude={memberIDs} onPick={(u) => (pager.setPage(1), removed.includes(u.id) ? setRemoved(removed.filter((x) => x !== u.id)) : setAdded([u, ...added]))} />
          <Loading q={full} />
          <div className="overflow-hidden rounded-lg border border-line">
            <ul className="max-h-64 divide-y divide-line overflow-y-auto">
              {pageSlice(memberIDs, pager.page, pager.size).map((id) => (
                <Member key={id} id={id} fresh={added.find((u) => u.id === id)} onRemove={() => (added.some((u) => u.id === id) ? setAdded(added.filter((u) => u.id !== id)) : setRemoved([...removed, id]))} />
              ))}
              {!memberIDs.length && <li className="p-3 text-sm text-muted">No members.</li>}
            </ul>
            {memberIDs.length > 0 && <Pagination pager={pager} total={memberIDs.length} label="Members pages" />}
          </div>
        </section>
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary" disabled={!valid || busy} data-testid="save-group">
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
