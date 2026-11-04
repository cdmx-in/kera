import { useMemo, useState } from 'react'
import { Link, NavLink, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Archive, ChevronRight, Folder as FolderIcon, FolderPlus, LayoutGrid, MoreHorizontal, PanelLeftClose, Pencil, Pin, PinOff, Search, Share2, ShieldCheck, SquarePen, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useChats, useFolders, useMe, usePinnedChats } from '@/api/queries'
import type { ChatSummary, Folder } from '@/api/types'
import { useChat } from '@/stores/chat'
import { useUI } from '@/stores/ui'
import { toast } from '@/stores/toast'
import { uiConfig } from '@/lib/ui-config'
import { cx } from '@/lib/format'
import { IconButton } from '@/ui/Dialog'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/ui/Menu'
import { SearchDialog } from './SearchDialog'

const DAY = 86_400_000

function bucket(updatedAt: number, now = Date.now()): string {
  const t = updatedAt < 1e12 ? updatedAt * 1000 : updatedAt
  const start = new Date(now).setHours(0, 0, 0, 0)
  if (t >= start) return 'Today'
  if (t >= start - DAY) return 'Yesterday'
  if (t >= start - 7 * DAY) return 'Previous 7 days'
  if (t >= start - 30 * DAY) return 'Previous 30 days'
  return 'Older'
}

function useChatMutations() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const { chatId } = useParams()
  const refresh = () => qc.invalidateQueries({ queryKey: ['chats'] })
  const wrap = (fn: () => Promise<unknown>, ok?: string) => async () => {
    try {
      await fn()
      await refresh()
      if (ok) toast(ok)
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return {
    rename: (c: ChatSummary, title: string) => wrap(() => api(`/api/v1/chats/${c.id}`, { method: 'POST', json: { chat: { title } } }))(),
    pin: (c: ChatSummary) => wrap(() => api(`/api/v1/chats/${c.id}/pin`, { method: 'POST' }))(),
    archive: (c: ChatSummary) => wrap(() => api(`/api/v1/chats/${c.id}/archive`, { method: 'POST' }).then(() => c.id === chatId && navigate('/')), 'Chat archived')(),
    remove: (c: ChatSummary) => wrap(() => api(`/api/v1/chats/${c.id}`, { method: 'DELETE' }).then(() => c.id === chatId && navigate('/')), 'Chat deleted')(),
    move: (id: string, folderId: string | null) => wrap(() => api(`/api/v1/chats/${id}/folder`, { method: 'POST', json: { folder_id: folderId } }))(),
    share: async (c: ChatSummary) => {
      try {
        const r = await api<{ share_id?: string; id?: string }>(`/api/v1/chats/${c.id}/share`, { method: 'POST' })
        const url = `${location.origin}/s/${r.share_id ?? r.id}`
        await navigator.clipboard?.writeText(url).catch(() => undefined)
        toast('Share link copied')
      } catch (e) {
        toast((e as Error).message, 'error')
      }
    },
  }
}

export function Sidebar() {
  const open = useUI((s) => s.sidebarOpen)
  const setSidebar = useUI((s) => s.setSidebar)
  const setSearch = useUI((s) => s.setSearch)
  const { data: me } = useMe()
  const chats = useChats()
  const pinned = usePinnedChats()
  const folders = useFolders()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const mut = useChatMutations()

  const closeOnMobile = () => {
    if (!window.matchMedia('(min-width: 768px)').matches) setSidebar(false)
  }
  const newChat = () => {
    useChat.getState().reset(null)
    navigate('/')
    closeOnMobile()
  }
  const groups = useMemo(() => {
    const out: [string, ChatSummary[]][] = []
    for (const c of chats.data ?? []) {
      if (c.folder_id || c.pinned) continue
      const b = bucket(c.updated_at)
      const g = out.find(([k]) => k === b)
      if (g) g[1].push(c)
      else out.push([b, [c]])
    }
    return out
  }, [chats.data])

  const addFolder = async () => {
    const name = prompt('Folder name')?.trim()
    if (!name) return
    try {
      await api('/api/v1/folders/', { method: 'POST', json: { name } })
      await qc.invalidateQueries({ queryKey: ['folders'] })
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }

  const workspace = me && (me.role === 'admin' || Object.values(me.permissions?.workspace ?? {}).some(Boolean))
  return (
    <>
      {open && <div className="fixed inset-0 z-30 bg-black/30 md:hidden" onClick={() => setSidebar(false)} aria-hidden />}
      <nav
        aria-label="Chats"
        onKeyDown={(e) => e.key === 'Escape' && !window.matchMedia('(min-width: 768px)').matches && setSidebar(false)}
        className={cx(
          'z-40 flex h-full w-[17rem] shrink-0 flex-col border-r border-line bg-bg-soft transition-[margin] duration-200 max-md:fixed max-md:inset-y-0 max-md:left-0',
          open ? 'ml-0' : '-ml-[17rem]',
        )}
        {...(!open && { inert: true })}
      >
        <div className="flex h-14 items-center gap-1 px-3">
          <Link to="/" onClick={newChat} className="min-w-0 flex-1 truncate px-1 text-[15px] font-semibold tracking-tight">
            {uiConfig().brand.name}
          </Link>
          <IconButton label="Search chats" onClick={() => setSearch(true)}>
            <Search size={17} />
          </IconButton>
          <IconButton label="Close sidebar" onClick={() => setSidebar(false)}>
            <PanelLeftClose size={18} />
          </IconButton>
        </div>
        <div className="space-y-0.5 px-2">
          <button type="button" data-testid="new-chat" onClick={newChat} className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm font-medium hover:bg-surface-2">
            <SquarePen size={16} /> New chat
          </button>
          {workspace && (
            <NavLink to="/workspace/models" onClick={closeOnMobile} className="flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm hover:bg-surface-2 aria-[current=page]:bg-surface-2 aria-[current=page]:font-medium">
              <LayoutGrid size={16} /> Workspace
            </NavLink>
          )}
          {me?.role === 'admin' && (
            <NavLink to="/admin/users" onClick={closeOnMobile} className="flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm hover:bg-surface-2 aria-[current=page]:bg-surface-2 aria-[current=page]:font-medium">
              <ShieldCheck size={16} /> Admin
            </NavLink>
          )}
        </div>
        <div className="mt-3 min-h-0 flex-1 overflow-y-auto px-2 pb-4">
          {(pinned.data?.length ?? 0) > 0 && (
            <Section title="Pinned">
              {pinned.data!.map((c) => (
                <ChatRow key={c.id} chat={{ ...c, pinned: true }} mut={mut} onOpen={closeOnMobile} />
              ))}
            </Section>
          )}
          <Section
            title="Folders"
            action={
              <IconButton label="New folder" onClick={addFolder} className="size-6">
                <FolderPlus size={14} />
              </IconButton>
            }
          >
            {(folders.data ?? []).map((f) => (
              <FolderRow key={f.id} folder={f} mut={mut} onOpen={closeOnMobile} />
            ))}
          </Section>
          {groups.map(([title, list]) => (
            <Section key={title} title={title} onDropChat={(id) => mut.move(id, null)}>
              {list.map((c) => (
                <ChatRow key={c.id} chat={c} mut={mut} onOpen={closeOnMobile} />
              ))}
            </Section>
          ))}
          {chats.isSuccess && !chats.data.length && <p className="px-2.5 py-2 text-sm text-muted">No chats yet.</p>}
        </div>
      </nav>
      <SearchDialog />
    </>
  )
}

function Section({ title, children, action, onDropChat }: { title: string; children: React.ReactNode; action?: React.ReactNode; onDropChat?: (id: string) => void }) {
  return (
    <section
      className="mb-3"
      onDragOver={(e) => onDropChat && e.dataTransfer.types.includes('text/kera-chat') && e.preventDefault()}
      onDrop={(e) => {
        const id = e.dataTransfer.getData('text/kera-chat')
        if (onDropChat && id) onDropChat(id)
      }}
    >
      <h3 className="flex items-center justify-between px-2.5 pb-1 text-xs font-medium text-muted">
        {title}
        {action}
      </h3>
      <ul>{children}</ul>
    </section>
  )
}

type Mut = ReturnType<typeof useChatMutations>

function FolderRow({ folder, mut, onOpen }: { folder: Folder; mut: Mut; onOpen: () => void }) {
  const [open, setOpen] = useState(false)
  const [over, setOver] = useState(false)
  // A folder lists its own chats (they may be older than the first page of the main list).
  const chats = useQuery({ queryKey: ['chats', 'folder', folder.id], queryFn: () => api<ChatSummary[]>(`/api/v1/chats/folder/${encodeURIComponent(folder.id)}`), enabled: open }).data ?? []
  return (
    <li
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes('text/kera-chat')) {
          e.preventDefault()
          setOver(true)
        }
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.stopPropagation()
        setOver(false)
        const id = e.dataTransfer.getData('text/kera-chat')
        if (id) void mut.move(id, folder.id).then(() => setOpen(true))
      }}
    >
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className={cx('flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-sm hover:bg-surface-2', over && 'bg-accent-soft')}>
        <ChevronRight size={14} className={cx('text-muted transition-transform', open && 'rotate-90')} />
        <FolderIcon size={15} className="text-muted" />
        <span className="truncate">{folder.name}</span>
      </button>
      {open && (
        <ul className="ml-4">
          {chats.length ? chats.map((c) => <ChatRow key={c.id} chat={c} mut={mut} onOpen={onOpen} />) : <li className="px-2.5 py-1 text-xs text-muted">Drag chats here</li>}
        </ul>
      )}
    </li>
  )
}

function ChatRow({ chat, mut, onOpen }: { chat: ChatSummary; mut: Mut; onOpen: () => void }) {
  const { chatId } = useParams()
  const liveTitle = useChat((s) => (s.chatId === chat.id ? s.title : ''))
  const [editing, setEditing] = useState(false)
  const active = chatId === chat.id
  const title = liveTitle || chat.title || 'New Chat'
  if (editing)
    return (
      <li>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            const v = new FormData(e.currentTarget).get('title')?.toString().trim()
            setEditing(false)
            if (v && v !== title) {
              if (active) useChat.getState().setTitle(v)
              void mut.rename(chat, v)
            }
          }}
        >
          <input name="title" defaultValue={title} autoFocus aria-label="Chat title" onBlur={(e) => e.currentTarget.form?.requestSubmit()} className="w-full rounded-lg border border-accent bg-surface px-2.5 py-1.5 text-sm outline-none" />
        </form>
      </li>
    )
  return (
    <li className="group relative" draggable onDragStart={(e) => e.dataTransfer.setData('text/kera-chat', chat.id)}>
      <Link
        to={`/c/${chat.id}`}
        data-testid="sidebar-chat"
        data-chat-id={chat.id}
        aria-current={active ? 'page' : undefined}
        onClick={onOpen}
        className={cx('block truncate rounded-lg py-1.5 pl-2.5 pr-8 text-sm', active ? 'bg-surface-2 font-medium shadow-[inset_3px_0_0_var(--accent)]' : 'hover:bg-surface-2')}
      >
        {title}
      </Link>
      <Menu>
        <MenuTrigger aria-label={`Options for ${title}`} className="absolute right-1 top-1/2 grid size-6 -translate-y-1/2 place-items-center rounded-md text-muted opacity-100 hover:bg-surface hover:text-fg data-[state=open]:opacity-100 md:opacity-0 md:group-hover:opacity-100 md:focus:opacity-100">
          <MoreHorizontal size={15} />
        </MenuTrigger>
        <MenuContent align="start">
          <MenuItem onSelect={() => setEditing(true)}>
            <Pencil size={14} /> Rename
          </MenuItem>
          <MenuItem onSelect={() => void mut.pin(chat)}>
            {chat.pinned ? <PinOff size={14} /> : <Pin size={14} />} {chat.pinned ? 'Unpin' : 'Pin'}
          </MenuItem>
          <MenuItem onSelect={() => void mut.share(chat)}>
            <Share2 size={14} /> Share
          </MenuItem>
          {chat.folder_id && (
            <MenuItem onSelect={() => void mut.move(chat.id, null)}>
              <FolderIcon size={14} /> Remove from folder
            </MenuItem>
          )}
          <MenuItem onSelect={() => void mut.archive(chat)}>
            <Archive size={14} /> Archive
          </MenuItem>
          <MenuSeparator />
          <MenuItem danger onSelect={() => confirm(`Delete "${title}"?`) && void mut.remove(chat)}>
            <Trash2 size={14} /> Delete
          </MenuItem>
        </MenuContent>
      </Menu>
    </li>
  )
}
