import { createBrowserRouter, Link } from 'react-router'
import { Shell } from './Shell'
import ChatPage from '@/features/chat/ChatPage'

const page = (load: () => Promise<{ default: React.ComponentType }>) => async () => ({ Component: (await load()).default })

function NotFound() {
  return (
    <div className="grid h-full place-items-center text-muted">
      <p>
        Page not found. <Link to="/" className="text-link underline">Go home</Link>
      </p>
    </div>
  )
}

// Shown while a lazy route loads on first paint.
const Splash = () => <div className="h-dvh bg-bg" />

export const router = createBrowserRouter([
  { path: '/auth', lazy: page(() => import('@/features/auth/AuthPage')), HydrateFallback: Splash },
  { path: '/s/:shareId', lazy: page(() => import('@/features/share/SharePage')), HydrateFallback: Splash },
  {
    Component: Shell,
    HydrateFallback: Splash,
    children: [
      { index: true, Component: ChatPage },
      { path: 'c/:chatId', Component: ChatPage },
      { path: 'admin/*', lazy: page(() => import('@/features/admin/AdminPage')) },
      { path: 'workspace/*', lazy: page(() => import('@/features/workspace/WorkspacePage')) },
      { path: '*', Component: NotFound },
    ],
  },
])
