import { useEffect } from 'react'
import { Outlet } from 'react-router'
import { Tooltip } from 'radix-ui'
import { useMe } from '@/api/queries'
import { useUI } from '@/stores/ui'
import { storageGet } from '@/lib/format'
import { Toaster } from '@/ui/misc'
import { Sidebar } from '@/features/sidebar/Sidebar'
import { SettingsDialog } from '@/features/settings/SettingsDialog'
import { BrandSplash } from '@/features/brand/Splash'

/** Signed-in frame: sidebar + routed page. A 401 from /api/v1/auths/ redirects to /auth. */
export function Shell() {
  const me = useMe()
  // Crossing the mobile breakpoint closes the overlay sidebar (and restores the desktop choice).
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 768px)')
    const on = () => useUI.getState().setSidebar(mq.matches && storageGet('local', 'kera:sidebar') !== '0')
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  if (!me.data && !me.isError) return <BrandSplash />
  if (!me.data) {
    return (
      <div className="grid h-dvh place-items-center text-muted">
        <p className="px-4 text-center">
          Cannot reach the server.{' '}
          <button type="button" className="text-link underline" onClick={() => void me.refetch()}>
            Retry
          </button>
        </p>
      </div>
    )
  }
  return (
    <Tooltip.Provider delayDuration={400}>
      <div data-testid="app-ready" className="relative flex h-dvh overflow-hidden">
        <Sidebar />
        <main className="relative flex min-h-0 min-w-0 flex-1 flex-col">
          <Outlet />
        </main>
      </div>
      <SettingsDialog />
      <Toaster />
    </Tooltip.Provider>
  )
}
