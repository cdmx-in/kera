// Lazy chunk: the workspace (models, knowledge, prompts).
import { NavLink, Navigate, Route, Routes } from 'react-router'
import { useMe } from '@/api/queries'
import { SidebarToggle, UserMenu } from '@/features/header/Header'
import { cx } from '@/lib/format'
import { ModelsList, ModelEditor } from './Models'
import { KnowledgeList, KnowledgeDetail } from './Knowledge'
import { PromptsPage } from './Prompts'

const sections = [
  ['models', 'Models'],
  ['knowledge', 'Knowledge'],
  ['prompts', 'Prompts'],
] as const

export default function WorkspacePage() {
  const { data: me } = useMe()
  const can = (k: string) => me?.role === 'admin' || !!me?.permissions?.workspace?.[k]
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 px-3">
        <SidebarToggle />
        <h1 className="flex-1 text-[15px] font-semibold">Workspace</h1>
        <UserMenu />
      </header>
      <nav aria-label="Workspace sections" className="flex gap-1 overflow-x-auto border-b border-line px-3">
        {sections.map(([k, l]) => (
          <NavLink key={k} to={`/workspace/${k}`} className={({ isActive }) => cx('whitespace-nowrap border-b-2 px-3 py-2 text-sm', isActive ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg')}>
            {l}
          </NavLink>
        ))}
      </nav>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-5xl p-4 sm:p-6">
          <Routes>
            <Route index element={<Navigate to="models" replace />} />
            <Route path="models" element={<ModelsList canCreate={can('models')} />} />
            <Route path="models/edit" element={<ModelEditor />} />
            <Route path="models/new" element={<ModelEditor create />} />
            <Route path="knowledge" element={<KnowledgeList canCreate={can('knowledge')} />} />
            <Route path="knowledge/:id" element={<KnowledgeDetail />} />
            <Route path="prompts" element={<PromptsPage canCreate={can('prompts')} />} />
            <Route path="*" element={<p className="text-muted">Page not found.</p>} />
          </Routes>
        </div>
      </div>
    </div>
  )
}
