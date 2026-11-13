import { Link, useNavigate } from 'react-router'
import { EyeOff, LogOut, PanelLeft, Settings, ShieldCheck, SquarePen, LayoutGrid, ListChecks } from 'lucide-react'
import { api, setToken } from '@/api/client'
import { can, useMe } from '@/api/queries'
import { useChat } from '@/stores/chat'
import { useUI } from '@/stores/ui'
import { cx } from '@/lib/format'
import { Tip } from '@/ui/misc'
import { IconButton } from '@/ui/Dialog'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/ui/Menu'
import { IntegrationSlots } from '@/features/integrations/registry'
import { canCowork } from '@/features/cowork/lib'
import { ModelPicker } from './ModelPicker'

export function Avatar({ name, src, size = 28 }: { name?: string; src?: string; size?: number }) {
  if (src) return <img src={src} alt="" width={size} height={size} className="rounded-full object-cover" style={{ width: size, height: size }} />
  return (
    <span className="grid place-items-center rounded-full bg-accent text-xs font-semibold text-white" style={{ width: size, height: size }}>
      {(name ?? '?').slice(0, 1).toUpperCase()}
    </span>
  )
}

export async function signOut() {
  await api('/api/v1/auths/signout', { method: 'POST' }).catch(() => undefined)
  setToken('')
  location.assign('/auth')
}

export function UserMenu() {
  const { data: me } = useMe()
  const openSettings = useUI((s) => s.openSettings)
  const navigate = useNavigate()
  if (!me) return null
  const workspace = me.role === 'admin' || Object.values(me.permissions?.workspace ?? {}).some(Boolean)
  return (
    <Menu>
      <MenuTrigger aria-label="Account menu" data-testid="user-menu" className="rounded-full">
        <Avatar name={me.name} src={me.profile_image_url} />
      </MenuTrigger>
      <MenuContent align="end">
        <div className="px-2.5 py-1.5">
          <p className="truncate text-sm font-medium">{me.name}</p>
          <p className="truncate text-xs text-muted">{me.email}</p>
        </div>
        <MenuSeparator />
        <MenuItem onSelect={() => openSettings()} testId="open-settings">
          <Settings size={15} /> Settings
        </MenuItem>
        {canCowork(me) && (
          <MenuItem onSelect={() => navigate('/cowork')}>
            <ListChecks size={15} /> Cowork
          </MenuItem>
        )}
        {workspace && (
          <MenuItem onSelect={() => navigate('/workspace/models')}>
            <LayoutGrid size={15} /> Workspace
          </MenuItem>
        )}
        {me.role === 'admin' && (
          <MenuItem onSelect={() => navigate('/admin/users')}>
            <ShieldCheck size={15} /> Admin
          </MenuItem>
        )}
        <MenuSeparator />
        <MenuItem onSelect={() => void signOut()}>
          <LogOut size={15} /> Sign out
        </MenuItem>
      </MenuContent>
    </Menu>
  )
}

export function SidebarToggle() {
  const open = useUI((s) => s.sidebarOpen)
  const setSidebar = useUI((s) => s.setSidebar)
  if (open) return null
  return (
    <>
      <IconButton label="Open sidebar" onClick={() => setSidebar(true)}>
        <PanelLeft size={18} />
      </IconButton>
      <Link to="/" aria-label="New chat" title="New chat" onClick={() => useChat.getState().reset(null)} className="hidden size-8 place-items-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg sm:grid">
        <SquarePen size={17} />
      </Link>
    </>
  )
}

export function Header() {
  const temporary = useChat((s) => s.temporary)
  const hasMessages = useChat((s) => s.currentId !== null)
  const navigate = useNavigate()
  const { data: me } = useMe()
  const allowTemporary = can(me, 'features', 'temporary_chat')
  const toggleTemporary = () => {
    const next = !temporary
    useChat.getState().reset(null, next)
    navigate(next ? '/?temporary-chat=true' : '/')
  }
  return (
    <header className="flex h-14 shrink-0 items-center gap-1 px-2 sm:px-3">
      <SidebarToggle />
      <div className="min-w-0 flex-1">
        <ModelPicker />
      </div>
      <IntegrationSlots slot="HeaderButton" />
      {allowTemporary && (
        <Tip text={temporary ? 'Temporary chat: not saved' : hasMessages ? 'Start a temporary chat' : 'Make this chat temporary'}>
          <button
            type="button"
            data-testid="temporary-chat"
            aria-pressed={temporary}
            aria-label="Temporary chat"
            onClick={toggleTemporary}
            className={cx('inline-flex h-8 items-center gap-1.5 rounded-lg px-2 text-sm', temporary ? 'bg-accent-soft text-fg' : 'text-muted hover:bg-surface-2 hover:text-fg')}
          >
            <EyeOff size={16} />
            {temporary && <span className="hidden sm:inline">Temporary</span>}
          </button>
        </Tip>
      )}
      <UserMenu />
    </header>
  )
}
