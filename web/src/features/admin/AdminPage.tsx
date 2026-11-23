// Lazy chunk: the admin console.
import { NavLink, Navigate, useParams } from 'react-router'
import { useMe } from '@/api/queries'
import { uiConfig } from '@/lib/ui-config'
import { AdminFrame, pillCls } from './Frame'
import { AnalyticsPage } from './Analytics'
import { GroupsPage } from './Groups'
import { PipelineDetail } from './PipelineRuns'
import { PipelinesPage } from './Pipelines'
import { SkillsPage } from './Skills'
import { SpendPage } from './Spend'
import { UsersPage } from './Users'
import { BrandingTab } from './settings/Branding'
import { ConnectionsTab } from './settings/Connections'
import { ModelsTab } from './settings/Models'
import { HookServersTab, OAuthTab, ToolServersTab } from './settings/Servers'
import { GuardrailsTab } from './settings/Guardrails'
import { ChatTab, CodeTab, InterfaceTab, RetrievalTab, TasksTab, WebTab } from './settings/tabs'
import { IntegrationsTab } from './settings/Integrations'

const settingsTabs: [string, string, (item?: string) => React.ReactNode][] = [
  ['connections', 'Connections', () => <ConnectionsTab />],
  ['models', 'Models', () => <ModelsTab />],
  ['tools', 'Tool servers', () => <ToolServersTab />],
  ['hooks', 'Hook servers', () => <HookServersTab />],
  ['guardrails', 'Guardrails', () => <GuardrailsTab />],
  ['oauth', 'OAuth providers', () => <OAuthTab />],
  ['branding', 'Branding', () => <BrandingTab />],
  ['interface', 'Interface', () => <InterfaceTab />],
  ['tasks', 'Tasks', () => <TasksTab />],
  ['chat', 'Chat', () => <ChatTab />],
  ['web', 'Web search', () => <WebTab />],
  ['code', 'Code interpreter', () => <CodeTab />],
  ['retrieval', 'Retrieval', () => <RetrievalTab />],
  ['integrations', 'Integrations', (item) => <IntegrationsTab name={item} />],
]

function sections(): [string, string][] {
  return [
    ['users', 'Users'],
    ['groups', 'Groups'],
    ['skills', 'Skills'],
    ['analytics', 'Analytics'],
    ...(uiConfig().features.spend !== false ? [['spend', 'Spend'] as [string, string]] : []),
    ['pipelines', 'Pipelines'],
    ['settings', 'Settings'],
  ]
}

export default function AdminPage() {
  const { data: me } = useMe()
  const [section = 'users', sub, item] = (useParams()['*'] ?? '').split('/').filter(Boolean)
  if (me && me.role !== 'admin') return <Navigate to="/" replace />
  let body: React.ReactNode
  if (section === 'users') body = <UsersPage />
  else if (section === 'groups') body = <GroupsPage />
  else if (section === 'skills') body = <SkillsPage />
  else if (section === 'analytics') body = <AnalyticsPage />
  else if (section === 'spend') body = <SpendPage />
  else if (section === 'pipelines') body = sub ? <PipelineDetail name={sub} run={item} /> : <PipelinesPage />
  else if (section === 'settings') {
    const tab = settingsTabs.find(([k]) => k === sub)
    if (!tab) return <Navigate to="/admin/settings/connections" replace />
    body = (
      <>
        <nav aria-label="Settings sections" className="flex flex-wrap gap-1 border-b border-line px-3 py-2">
          {settingsTabs.map(([k, l]) => (
            <NavLink key={k} to={`/admin/settings/${k}`} className={pillCls}>
              {l}
            </NavLink>
          ))}
        </nav>
        <div className="mx-auto w-full max-w-[100rem] p-4 sm:p-6 lg:px-8">{tab[2](item)}</div>
      </>
    )
  } else return <Navigate to="/admin/users" replace />
  return (
    <AdminFrame title="Admin" base="/admin" sections={sections()}>
      {body}
    </AdminFrame>
  )
}
