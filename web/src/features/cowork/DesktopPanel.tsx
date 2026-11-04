// Live view of the task's Linux desktop: watch, take over, end. The noVNC client is bundled here (lazy
// chunk) and only its websocket goes through Kera to the desktop sidecar, so nothing the sidecar serves
// runs in Kera's origin.
import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type RFB from '@novnc/novnc'
import { api } from '@/api/client'
import { toast } from '@/stores/toast'

/** `mock` is set only by the dev mock (a placeholder instead of a VNC connection). */
type Desktop = { active: boolean; mock?: boolean }

const base = (chatId: string) => `/api/v1/desktop/${encodeURIComponent(chatId)}`
const box = 'aspect-[16/10] w-full overflow-hidden rounded-lg border border-line bg-black'

/** Shown while the sidecar has a desktop for this chat; polls every 5 s while `live` (an error such as 404 = feature off: hidden, no polling). */
export function DesktopPanel({ chatId, live }: { chatId: string; live: boolean }) {
  const qc = useQueryClient()
  const key = ['desktop', chatId]
  const q = useQuery({ queryKey: key, queryFn: () => api<Desktop>(base(chatId)), refetchInterval: (s) => (live && !s.state.error ? 5000 : false), staleTime: 0 })
  if (!q.data?.active) return null
  const end = async () => {
    try {
      await api(`${base(chatId)}/end`, { method: 'POST' })
      qc.setQueryData<Desktop>(key, { active: false })
    } catch (e) {
      toast(`Could not end the desktop: ${(e as Error).message}`, 'error')
    }
  }
  return (
    <details open data-testid="desktop-panel" className="border-b border-line p-3">
      <summary className="cursor-pointer pb-2 text-xs font-medium text-muted">Desktop</summary>
      {q.data.mock ? (
        <div data-testid="desktop-view" className={`${box} grid place-items-center text-sm text-slate-200`}>
          Mock desktop (noVNC in the real app)
        </div>
      ) : (
        <Screen key={chatId} chatId={chatId} />
      )}
      <p className="mt-2 text-xs text-muted">You can click and type here, for example to sign in. Then tell the assistant to continue.</p>
      <button type="button" data-testid="end-desktop" onClick={() => void end()} className="mt-2 rounded-lg border border-line px-2.5 py-1 text-sm hover:bg-surface-2">
        End desktop
      </button>
    </details>
  )
}

/** The noVNC canvas; connects on mount and on Reconnect, disconnects on unmount (also when the panel goes away after End). */
function Screen({ chatId }: { chatId: string }) {
  const ref = useRef<HTMLDivElement>(null)
  const [state, setState] = useState<'connecting' | 'connected' | 'disconnected'>('connecting')
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let rfb: RFB | undefined
    let gone = false
    import('@novnc/novnc').then(
      ({ default: RFB }) => {
        if (gone || !ref.current) return
        const r = new RFB(ref.current, `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${base(chatId)}/websockify`)
        r.scaleViewport = true
        r.viewOnly = false
        r.addEventListener('connect', () => !gone && setState('connected'))
        r.addEventListener('disconnect', () => {
          rfb = undefined
          if (!gone) setState('disconnected')
        })
        rfb = r
      },
      () => !gone && setState('disconnected'),
    )
    return () => {
      gone = true
      rfb?.disconnect()
    }
  }, [chatId, attempt])
  return (
    <div className="relative">
      <div ref={ref} data-testid="desktop-view" className={box} />
      {state !== 'connected' && (
        <div className="absolute inset-0 grid place-items-center rounded-lg bg-black/70 text-sm text-slate-200">
          {state === 'connecting' ? (
            'Connecting…'
          ) : (
            <div className="grid justify-items-center gap-2">
              Disconnected
              <button
                type="button"
                onClick={() => {
                  setState('connecting')
                  setAttempt((n) => n + 1)
                }}
                className="rounded-lg border border-slate-500 px-2.5 py-1 hover:bg-white/10"
              >
                Reconnect
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
