// Lazy chunk: the workspace (models, knowledge, prompts).
import { Navigate, Route, Routes } from 'react-router'
import { useMe } from '@/api/queries'
import { AdminFrame } from '@/features/admin/Frame'
import { ModelsList, ModelEditor } from './Models'
import { KnowledgeList, KnowledgeDetail } from './Knowledge'
import { PromptsPage } from './Prompts'

const sections: [string, string][] = [
  ['models', 'Models'],
  ['knowledge', 'Knowledge'],
  ['prompts', 'Prompts'],
]

export default function WorkspacePage() {
  const { data: me } = useMe()
  const can = (k: string) => me?.role === 'admin' || !!me?.permissions?.workspace?.[k]
  return (
    <AdminFrame title="Workspace" base="/workspace" sections={sections}>
      {/* Same width as the admin console; editors pin their header to the top of this scroll area. */}
      <div className="mx-auto w-full max-w-[100rem] p-4 sm:p-6 lg:px-8">
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
    </AdminFrame>
  )
}
