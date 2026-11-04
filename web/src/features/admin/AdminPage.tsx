// Lazy chunk: the admin console.
import { NavLink, Navigate, useParams } from 'react-router'
import { useMe } from '@/api/queries'
import { uiConfig } from '@/lib/ui-config'
import { AdminFrame, tabCls } from './Frame'
import { AnalyticsPage } from './Analytics'
import { GroupsPage } from './Groups'
import { SkillsPage } from './Skills'
import { SpendPage } from './Spend'
import { UsersPage } from './Users'
import { ConnectionsTab } from './settings/Connections'
import { ModelsTab } from './settings/Models'
import { HookServersTab, OAuthTab, ToolServersTab } from './settings/Servers'
import { ChatTab, CodeTab, InterfaceTab, RetrievalTab, TasksTab, WebTab } from './settings/tabs'

const settingsTabs: [string, string, () => React.ReactNode][] = [
  ['connections', 'Connections', () => <ConnectionsTab />],
  ['models', 'Models', () => <ModelsTab />],
  ['tools', 'Tool servers', () => <ToolServersTab />],
  ['hooks', 'Hook servers', () => <HookServersTab />],
  ['oauth', 'OAuth providers', () => <OAuthTab />],
  ['interface', 'Interface', () => <InterfaceTab />],
  ['tasks', 'Tasks', () => <TasksTab />],
  ['chat', 'Chat', () => <ChatTab />],
  ['web', 'Web search', () => <WebTab />],
  ['code', 'Code interpreter', () => <CodeTab />],
  ['retrieval', 'Retrieval', () => <RetrievalTab />],
]

function sections(): [string, string][] {
  return [
    ['users', 'Users'],
    ['groups', 'Groups'],
    ['skills', 'Skills'],
    ['analytics', 'Analytics'],
    ...(uiConfig().features.spend !== false ? [['spend', 'Spend'] as [string, string]] : []),
    ['settings', 'Settings'],
  ]
}

export default function AdminPage() {
  const { data: me } = useMe()
  const [section = 'users', sub] = (useParams()['*'] ?? '').split('/').filter(Boolean)
  if (me && me.role !== 'admin') return <Navigate to="/" replace />
  let body: React.ReactNode
  if (section === 'users') body = <UsersPage />
  else if (section === 'groups') body = <GroupsPage />
  else if (section === 'skills') body = <SkillsPage />
  else if (section === 'analytics') body = <AnalyticsPage />
  else if (section === 'spend') body = <SpendPage />
  else if (section === 'settings') {
    const tab = settingsTabs.find(([k]) => k === sub)
    if (!tab) return <Navigate to="/admin/settings/connections" replace />
    body = (
      <>
        <nav aria-label="Settings sections" className="flex gap-1 overflow-x-auto border-b border-line px-3">
          {settingsTabs.map(([k, l]) => (
            <NavLink key={k} to={`/admin/settings/${k}`} className={tabCls}>
              {l}
            </NavLink>
          ))}
        </nav>
        <div className="mx-auto w-full max-w-[100rem] p-4 sm:p-6 lg:px-8">{tab[2]()}</div>
      </>
    )
  } else return <Navigate to="/admin/users" replace />
  return (
    <AdminFrame title="Admin" base="/admin" sections={sections()}>
      {body}
    </AdminFrame>
  )
}
