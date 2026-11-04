import { useEffect, useState } from 'react'
import { queryClient } from '@/app/query'
import { uiConfig } from '@/lib/ui-config'
import { useOutage } from '@/stores/outage'
import { brandMark } from './Splash'

const POLL_MS = 5000

/** Full-screen "back shortly" page while the server is unreachable; polls /health and lifts itself. */
export function Unavailable() {
  const down = useOutage((s) => s.down)
  const [checking, setChecking] = useState(false)
  useEffect(() => {
    if (!down) return
    let stop = false
    const probe = async () => {
      setChecking(true)
      const ok = await fetch('/health', { cache: 'no-store' }).then((r) => r.ok, () => false)
      setChecking(false)
      if (stop) return
      if (ok) {
        useOutage.getState().setDown(false)
        void queryClient.invalidateQueries()
      } else t = setTimeout(probe, POLL_MS)
    }
    let t = setTimeout(probe, POLL_MS)
    return () => {
      stop = true
      clearTimeout(t)
    }
  }, [down])
  if (!down) return null
  const mark = brandMark()
  const name = uiConfig().brand.name || 'The service'
  return (
    <div role="alertdialog" aria-live="assertive" aria-labelledby="outage-title" className="fixed inset-0 z-[100] grid place-items-center bg-bg/95 p-6 backdrop-blur-sm" data-testid="outage">
      <div className="max-w-md text-center">
        {mark && <img src={mark} alt="" className="brand-glow mx-auto mb-6 size-16 object-contain" />}
        <h1 id="outage-title" className="text-2xl font-semibold">{name} is temporarily unavailable</h1>
        <p className="mt-3 text-muted">We'll be back shortly. Nothing you were working on is lost: this page reconnects by itself as soon as the service is back.</p>
        <p className="mt-6 inline-flex items-center gap-2 text-sm text-muted">
          <span className={`size-2 rounded-full bg-accent ${checking ? 'animate-ping' : 'animate-pulse'}`} />
          {checking ? 'Checking…' : 'Retrying every few seconds'}
        </p>
        <div className="mt-6">
          <button type="button" onClick={() => location.reload()} className="rounded-lg border border-line px-4 py-2 text-sm hover:bg-surface-2">
            Reload now
          </button>
        </div>
      </div>
    </div>
  )
}
