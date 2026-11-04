// Pure helpers for the Branding tab: the stored ui.brand value and the CSS
// variables its live preview needs.
import type { UIConfig } from '@/lib/ui-config'

export type Brand = UIConfig['brand'] & { customCss?: string }

/** Lightness and relative chroma of --gray-50..950; must match greyLadder in internal/ui/index.go. */
const ladder: [string, number, number][] = [
  ['50', 0.985, 0.25],
  ['100', 0.967, 0.5],
  ['200', 0.925, 0.75],
  ['300', 0.87, 1],
  ['400', 0.71, 1],
  ['500', 0.555, 1],
  ['600', 0.45, 1],
  ['700', 0.375, 1],
  ['800', 0.28, 1],
  ['900', 0.215, 0.833],
  ['950', 0.16, 0.667],
]

/** The grey ladder for a hue and chroma, as the server writes it. */
export function greyScale(hue: number, chroma: number): string[] {
  return ladder.map(([, l, rel]) => `oklch(${l} ${Math.round(chroma * rel * 1e4) / 1e4} ${hue})`)
}

/** Mirrors apply.CSSColor: the colour forms the server accepts. */
export const isCssColor = (s: string) => /^(#[0-9a-fA-F]{3,8}|[a-zA-Z]+|(rgb|rgba|hsl|hsla|oklch|oklab)\([0-9a-z.,%/ +-]*\))$/.test(s)

/** A value for <input type="color"> (#rrggbb only). */
export function hexColor(s: string | undefined, fallback = '#000000'): string {
  if (!s) return fallback
  if (/^#[0-9a-f]{6}$/i.test(s)) return s.toLowerCase()
  const m = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(s)
  return m ? `#${m[1]}${m[1]}${m[2]}${m[2]}${m[3]}${m[3]}`.toLowerCase() : fallback
}

/**
 * CSS variables for a preview subtree: the brand tokens plus the surface
 * tokens derived from them, which resolve at :root and so must be declared
 * again where the greys change.
 */
export function previewVars(b: Brand, dark: boolean): Record<string, string> {
  const v: Record<string, string> = {}
  if (b.accent?.base && isCssColor(b.accent.base)) v['--accent'] = b.accent.base
  if (b.accent?.bright && isCssColor(b.accent.bright)) v['--accent-bright'] = b.accent.bright
  const g = b.grey
  const scale = g?.scale?.length === ladder.length && g.scale.every(isCssColor) ? g.scale : g && (g.chroma || g.hue) ? greyScale(g.hue ?? 0, g.chroma ?? 0) : null
  scale?.forEach((c, i) => (v[`--gray-${ladder[i][0]}`] = c))
  const gray = (s: string) => `var(--gray-${s})`
  Object.assign(
    v,
    dark
      ? { '--bg': gray('950'), '--bg-soft': gray('900'), '--surface': gray('900'), '--surface-2': gray('800'), '--border': gray('800'), '--fg': gray('100'), '--muted': gray('400'), '--accent-soft': 'color-mix(in oklch, var(--accent-bright) 18%, transparent)', colorScheme: 'dark' }
      : { '--bg': '#ffffff', '--bg-soft': gray('50'), '--surface': '#ffffff', '--surface-2': gray('100'), '--border': gray('200'), '--fg': gray('900'), '--muted': gray('500'), '--accent-soft': 'color-mix(in oklch, var(--accent) 11%, transparent)', colorScheme: 'light' },
  )
  return v
}

/** Drops empty strings, empty lists and empty objects, so unset fields fall back (mark ?? favicon). */
export function pruneBrand<T>(v: T): T | undefined {
  if (v === '' || v == null) return undefined
  if (Array.isArray(v)) return (v.length ? v : undefined) as T | undefined
  if (typeof v === 'object') {
    const out = Object.fromEntries(Object.entries(v).flatMap(([k, x]) => (pruneBrand(x) === undefined ? [] : [[k, pruneBrand(x)]])))
    return (Object.keys(out).length ? out : undefined) as T | undefined
  }
  return v
}

/** The /brand/ file name of a URL, without ?v=. */
export const brandFile = (u?: string) => (u?.startsWith('/brand/') ? u.slice(7).split('?')[0] : '')
