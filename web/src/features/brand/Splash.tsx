import { uiConfig } from '@/lib/ui-config'

/**
 * Continues the server-rendered splash (same classes, styled by the CSS the
 * server injects) while the first route or the session loads, without
 * replaying its entry animation. Unbranded: a plain background.
 */
export function BrandSplash() {
  const { name, splash } = uiConfig().brand
  if (!splash) return <div className="h-dvh bg-bg" />
  const light = splash.logo
  return (
    <div className={`kera-splash kera-splash-${splash.style ?? 'orbit'} kera-splash-cont`} role="status" aria-label={`Loading ${name}`}>
      <div className="kera-splash-stage">
        {light && (
          <>
            <img className="kera-splash-logo kera-splash-light" src={light} alt="" />
            <img className="kera-splash-logo kera-splash-dark" src={splash.logoDark ?? light} alt="" />
          </>
        )}
      </div>
      {splash.wordmark && <div className="kera-splash-word">{splash.wordmark}</div>}
    </div>
  )
}

/** The brand's small square logo, if any. */
export const brandMark = () => uiConfig().brand.mark ?? uiConfig().brand.favicon

/** Slow accent aurora with a faint dot grid; the parent must be `relative isolate`. */
export function Aurora({ variant = 'app' }: { variant?: 'app' | 'login' }) {
  return <div aria-hidden className={`aurora aurora-${variant}`} />
}
