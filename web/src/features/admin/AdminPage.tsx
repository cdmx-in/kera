// Lazy chunk. ponytail: placeholder until the admin milestone; routes are reserved.
import { NavLink, Navigate, useParams } from 'react-router'
import { useMe } from '@/api/queries'
import { SidebarToggle, UserMenu } from '@/features/header/Header'
import { cx } from '@/lib/format'

const sections = [
  ['users', 'Users'],
  ['groups', 'Groups'],
  ['analytics', 'Analytics'],
  ['spend', 'Spend'],
  ['settings', 'Settings'],
]

export default function AdminPage() {
  const { data: me } = useMe()
  const section = useParams()['*']?.split('/')[0] || 'users'
  if (me && me.role !== 'admin') return <Navigate to="/" replace />
  return <Placeholder title="Admin" base="/admin" sections={sections} current={section} />
}

export function Placeholder({ title, base, sections, current }: { title: string; base: string; sections: string[][]; current: string }) {
  const label = sections.find(([k]) => k === current)?.[1] ?? current
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 px-3">
        <SidebarToggle />
        <h1 className="flex-1 text-[15px] font-semibold">{title}</h1>
        <UserMenu />
      </header>
      <nav aria-label={`${title} sections`} className="flex gap-1 overflow-x-auto border-b border-line px-3">
        {sections.map(([k, l]) => (
          <NavLink key={k} to={`${base}/${k}`} className={({ isActive }) => cx('whitespace-nowrap border-b-2 px-3 py-2 text-sm', isActive ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg')}>
            {l}
          </NavLink>
        ))}
      </nav>
      <div className="grid flex-1 place-items-center p-6 text-center text-muted">
        <p>{label} is coming in a later release.</p>
      </div>
    </div>
  )
}
