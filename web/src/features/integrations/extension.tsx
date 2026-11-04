// extensionInstall: a header button offering a browser extension download.
// Hidden when the extension's marker attribute on <html> reports a version at
// least as new; "Update" when older; "Install" when absent. Desktop Chromium only.
import { lazy, Suspense, useEffect, useState } from 'react'
import { Download } from 'lucide-react'
import type { ExtensionInstallIntegration } from '@/lib/ui-config'
import { compareVersions } from '@/lib/semver'

const InstallDialog = lazy(() => import('./ExtensionDialog'))

interface UAData {
  mobile?: boolean
  platform?: string
  brands?: { brand: string }[]
}

export function uaData(): UAData | undefined {
  return (navigator as Navigator & { userAgentData?: UAData }).userAgentData
}

export function isDesktopChromium(): boolean {
  const ua = uaData()
  return !!ua && !ua.mobile && !!ua.brands?.some((b) => /chromium/i.test(b.brand))
}

function useMarker(attr: string): string | null {
  const read = () => document.documentElement.getAttribute(attr)
  const [v, setV] = useState(read)
  useEffect(() => {
    const ob = new MutationObserver(() => setV(read()))
    ob.observe(document.documentElement, { attributes: true, attributeFilter: [attr] })
    setV(read())
    return () => ob.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attr])
  return v
}

export function installState(installed: string | null, latest: string): 'install' | 'update' | 'current' {
  if (installed == null) return 'install'
  // a bare marker (attribute present, empty) predates versioned markers
  if (installed === '' || compareVersions(installed, latest) < 0) return 'update'
  return 'current'
}

export function ExtensionHeaderButton({ integration: i }: { integration: ExtensionInstallIntegration }) {
  const installed = useMarker(i.markerAttr)
  const [open, setOpen] = useState(false)
  const platformOk = !i.platforms?.length || !i.platforms.includes('chromium-desktop') || isDesktopChromium()
  const state = installState(installed, i.version)
  if (!platformOk || state === 'current') return null
  return (
    <>
      <button type="button" data-testid={`integration-${i.id}`} onClick={() => setOpen(true)} className="hidden items-center gap-1.5 rounded-full border border-line px-3 py-1 text-sm hover:bg-surface-2 sm:inline-flex">
        <Download size={14} className="text-accent" />
        {state === 'update' ? `Update ${i.label}` : `Install ${i.label}`}
      </button>
      {open && (
        <Suspense fallback={null}>
          <InstallDialog integration={i} mode={state} onClose={() => setOpen(false)} />
        </Suspense>
      )}
    </>
  )
}
