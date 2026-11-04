// Page frame (header + section tabs), shared with the workspace chunk; kept
// apart from AdminPage so importing it does not pull the admin pages.
import { NavLink } from 'react-router'
import { SidebarToggle, UserMenu } from '@/features/header/Header'
import { cx } from '@/lib/format'

export const tabCls = ({ isActive }: { isActive: boolean }) => cx('whitespace-nowrap border-b-2 px-3 py-2 text-sm', isActive ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg')

export function AdminFrame({ title, base, sections, children }: { title: string; base: string; sections: [string, string][]; children: React.ReactNode }) {
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 px-3">
        <SidebarToggle />
        <h1 className="flex-1 text-[15px] font-semibold">{title}</h1>
        <UserMenu />
      </header>
      <nav aria-label={`${title} sections`} className="flex gap-1 overflow-x-auto border-b border-line px-3">
        {sections.map(([k, l]) => (
          <NavLink key={k} to={`${base}/${k}`} className={tabCls}>
            {l}
          </NavLink>
        ))}
      </nav>
      <div className="relative min-h-0 flex-1 overflow-y-auto" data-testid="admin-content">
        {children}
      </div>
    </div>
  )
}

/** Frame with a "later release" note, for sections not built yet (workspace). */
export function Placeholder({ title, base, sections, current }: { title: string; base: string; sections: string[][]; current: string }) {
  const label = sections.find(([k]) => k === current)?.[1] ?? current
  return (
    <AdminFrame title={title} base={base} sections={sections as [string, string][]}>
      <div className="grid h-full place-items-center p-6 text-center text-muted">
        <p>{label} is coming in a later release.</p>
      </div>
    </AdminFrame>
  )
}
