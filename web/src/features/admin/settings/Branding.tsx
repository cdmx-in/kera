// Admin > Settings > Branding: edits ui.brand (name, images, colours, splash,
// sign-in) with a live preview, saved through the settings API.
import { useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUp, FileLock2, Images, ImageUp, LogIn, Monitor, Moon, Palette, RotateCcw, SquarePen, Sun, SwatchBook, Trash2, Type, Upload, X } from 'lucide-react'
import { api } from '@/api/client'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Switch } from '@/ui/misc'
import { cx } from '@/lib/format'
import { Aurora } from '@/features/brand/Splash'
import { enc, uploadBrandAsset, useBrandAssets, useSaveSettings, useSettings, type BrandAsset } from '../api'
import { attempt, Btn, Card, Confirm, inputCls, Loading, ManagedBadge } from '../ui'
import { SaveBar } from './form'
import { IconTile } from './serviceIcons'
import { brandFile, greyScale, hexColor, isCssColor, previewVars, pruneBrand, type Brand } from './brand'

const tile = (icon: ReactNode) => <IconTile>{icon}</IconTile>
const iconCls = 'text-accent dark:text-accent-bright'

interface Slot {
  slot: string
  label: string
  hint: string
  dark?: boolean
  get: (b: Brand) => string | undefined
  put: (b: Brand, url: string) => Partial<Brand>
}

const slots: Slot[] = [
  { slot: 'mark', label: 'Mark', hint: 'Small square logo: sidebar, sign-in card, app icon.', get: (b) => b.mark, put: (_, u) => ({ mark: u }) },
  { slot: 'favicon', label: 'Favicon', hint: 'Browser tab icon; the mark falls back to it.', get: (b) => b.favicon, put: (_, u) => ({ favicon: u }) },
  { slot: 'logo', label: 'Logo, light', hint: 'Wide logo for light backgrounds.', get: (b) => b.logo?.light, put: (b, u) => ({ logo: { ...b.logo, light: u } }) },
  { slot: 'logo-dark', label: 'Logo, dark', hint: 'Wide logo for dark backgrounds.', dark: true, get: (b) => b.logo?.dark, put: (b, u) => ({ logo: { ...b.logo, dark: u } }) },
  { slot: 'splash', label: 'Splash logo, light', hint: 'Shown while the app loads.', get: (b) => b.splash?.logo, put: (b, u) => ({ splash: { ...b.splash, logo: u } }) },
  { slot: 'splash-dark', label: 'Splash logo, dark', hint: 'Dark mode splash; falls back to the light one.', dark: true, get: (b) => b.splash?.logoDark, put: (b, u) => ({ splash: { ...b.splash, logoDark: u } }) },
]

const DEFAULT_ACCENT = '#1e7f8c'
const DEFAULT_BRIGHT = '#3dafc4'
const DEFAULT_HUE = 215
const DEFAULT_CHROMA = 0.012

export function BrandingTab() {
  const q = useSettings()
  const saveSettings = useSaveSettings()
  const setting = q.data?.get('ui.brand')
  const stored = (setting?.value ?? {}) as Brand
  const [draft, setDraft] = useState<Brand | null>(null)
  const [picking, setPicking] = useState<Slot | null>(null)
  const fromFile = useQuery({
    queryKey: ['admin', 'brand-config'],
    queryFn: () => api<{ brand: Brand }>('/api/v1/admin/brand/config').then((r) => r.brand, () => null),
    retry: false,
  })
  if (q.isLoading || q.error) return <Loading q={q} />

  const b = draft ?? stored
  const set = (patch: Partial<Brand>) => setDraft({ ...b, ...patch })
  const save = async () => {
    if (await attempt(() => saveSettings({ 'ui.brand': pruneBrand(b) ?? {} }), 'Brand saved. Reload to see it everywhere.')) setDraft(null)
  }

  const resetButton = fromFile.data && (
    <Btn onClick={() => setDraft(fromFile.data!)} title="Load the brand section of the config file into the editor; save to apply it" data-testid="brand-reset-file">
      <RotateCcw size={14} /> Reset to config file
    </Btn>
  )

  return (
    <div className="space-y-4" data-testid="branding-tab">
      {setting?.managed && (
        <div className="flex flex-wrap items-start gap-3 rounded-2xl border border-amber-300/70 bg-amber-50 p-4 text-sm dark:border-amber-900/70 dark:bg-amber-950/30" data-testid="brand-managed">
          <FileLock2 size={18} className="mt-0.5 shrink-0 text-amber-700 dark:text-amber-400" />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2 font-medium">
              The brand comes from the config file <ManagedBadge />
            </div>
            <p className="mt-1 text-[13px] leading-snug text-muted">
              Changes saved here apply right away, but the next <code className="font-mono">kera apply</code> overwrites them. To manage the brand here for good, remove the{' '}
              <code className="font-mono">brand</code> section from the config file.
            </p>
          </div>
          {resetButton}
        </div>
      )}
      {!setting?.managed && fromFile.data && <div className="flex justify-end">{resetButton}</div>}
      <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_24rem] 2xl:grid-cols-[minmax(0,1fr)_28rem]">
        <div className="grid items-start gap-4 2xl:grid-cols-2">
          <Card
            title={<TitleRow icon={<Type size={18} className={iconCls} />} title="Identity" />}
            hint="The product name in the tab title, sidebar and sign-in page."
          >
            <div className="divide-y divide-line">
              <Row label="Name">
                <input value={b.name ?? ''} onChange={(e) => set({ name: e.target.value })} placeholder="Kera" aria-label="Name" className={inputCls} data-testid="brand-name" />
              </Row>
              <Row label="Tagline" hint="Under the name on the sign-in page; the page description.">
                <input value={b.tagline ?? ''} onChange={(e) => set({ tagline: e.target.value })} aria-label="Tagline" className={inputCls} data-testid="brand-tagline" />
              </Row>
            </div>
          </Card>

          <Card title={<TitleRow icon={<Monitor size={18} className={iconCls} />} title="Appearance" />} hint="What a new visitor sees before choosing a theme.">
            <div className="divide-y divide-line">
              <Row label="Default theme" hint="Users can still pick their own.">
                <Segmented
                  value={b.defaultTheme ?? 'system'}
                  onChange={(v) => set({ defaultTheme: v })}
                  options={[
                    ['light', 'Light', <Sun size={14} key="l" />],
                    ['dark', 'Dark', <Moon size={14} key="d" />],
                    ['system', 'System', <Monitor size={14} key="s" />],
                  ]}
                />
              </Row>
              <Row label="Aurora background" hint="Slow accent clouds behind the chat.">
                <Switch checked={b.background === 'aurora'} onChange={(x) => set({ background: x ? 'aurora' : 'none' })} label="Aurora background" testId="brand-aurora" />
              </Row>
            </div>
          </Card>

          <Card
            className="2xl:col-span-2"
            title={<TitleRow icon={<ImageUp size={18} className={iconCls} />} title="Images" />}
            hint="PNG, JPEG, WebP, SVG or ICO up to 2 MB. Drop a file on a row, upload one, or choose an image already on the server."
          >
            <div className="@container">
              <div className="grid divide-y divide-line @5xl:grid-cols-2 @5xl:divide-y-0">
                {slots.map((s) => (
                  <ImageSlot key={s.slot} s={s} value={s.get(b) ?? ''} onChange={(u) => set(s.put(b, u))} onPick={() => setPicking(s)} />
                ))}
              </div>
            </div>
          </Card>

          <Card title={<TitleRow icon={<Palette size={18} className={iconCls} />} title="Colours" />} hint="The accent drives buttons, links and highlights; the greys tint every surface.">
            <div className="divide-y divide-line">
              <ColorRow label="Accent" hint="Buttons, links, focus rings." value={b.accent?.base} fallback={DEFAULT_ACCENT} onChange={(v) => set({ accent: { ...b.accent, base: v } })} testId="brand-accent" />
              <ColorRow label="Accent, bright" hint="Glows and dark mode links." value={b.accent?.bright} fallback={DEFAULT_BRIGHT} onChange={(v) => set({ accent: { ...b.accent, bright: v } })} testId="brand-accent-bright" />
              <GreyRows b={b} set={set} />
            </div>
          </Card>

          <Card title={<TitleRow icon={<LogIn size={18} className={iconCls} />} title="Splash and sign-in" />} hint="The loading screen and the sign-in page.">
            <div className="divide-y divide-line">
              <Row label="Splash wordmark" hint="Spaced letters under the splash logo.">
                <input value={b.splash?.wordmark ?? ''} onChange={(e) => set({ splash: { ...b.splash, wordmark: e.target.value } })} aria-label="Splash wordmark" className={inputCls} />
              </Row>
              <Row label="Splash style">
                <Segmented value={b.splash?.style ?? 'orbit'} onChange={(v) => set({ splash: { ...b.splash, style: v } })} options={[['orbit', 'Orbit'], ['plain', 'Plain']]} />
              </Row>
              <Row label="Sign-in wordmark" hint="At the bottom of the sign-in page.">
                <input value={b.login?.wordmark ?? ''} onChange={(e) => set({ login: { ...b.login, wordmark: e.target.value } })} aria-label="Sign-in wordmark" className={inputCls} data-testid="brand-login-wordmark" />
              </Row>
              <Row label="Sign-in style">
                <Segmented value={b.login?.style ?? 'plain'} onChange={(v) => set({ login: { ...b.login, style: v } })} options={[['glass-card', 'Glass card'], ['plain', 'Plain']]} />
              </Row>
            </div>
          </Card>
        </div>
        <Preview b={b} />
      </div>
      <SaveBar d={{ dirty: draft !== null, discard: () => setDraft(null), save }} />
      {picking && (
        <AssetPicker
          title={`Choose: ${picking.label}`}
          current={brandFile(picking.get(b))}
          onClose={() => setPicking(null)}
          onPick={(u) => {
            set(picking.put(b, u))
            setPicking(null)
          }}
        />
      )}
    </div>
  )
}

function TitleRow({ icon, title }: { icon: ReactNode; title: string }) {
  return (
    <span className="flex items-center gap-3">
      {tile(icon)}
      {title}
    </span>
  )
}

function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    // Side by side when the card is wide enough, stacked in a narrow column.
    <div className="@container px-5 py-4 transition-colors hover:bg-surface-2/40">
      <div className="flex flex-col gap-2 @lg:flex-row @lg:items-center @lg:justify-between @lg:gap-6">
        <div className="min-w-0 flex-1">
          <span className="text-sm font-medium">{label}</span>
          {hint && <p className="mt-0.5 text-[13px] leading-snug text-muted">{hint}</p>}
        </div>
        <div className="w-full @lg:flex @lg:w-64 @lg:shrink-0 @lg:justify-end">{children}</div>
      </div>
    </div>
  )
}

function Segmented<T extends string>({ value, onChange, options }: { value: T; onChange: (v: T) => void; options: [T, string, ReactNode?][] }) {
  return (
    <div role="radiogroup" className="inline-flex w-full rounded-lg border border-line bg-surface-2/60 p-0.5 sm:w-auto">
      {options.map(([v, label, icon]) => (
        <button
          key={v}
          type="button"
          role="radio"
          aria-checked={value === v}
          aria-label={label || `${v} preview`}
          onClick={() => onChange(v)}
          className={cx('inline-flex flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-md px-3 py-1 text-sm transition-colors', value === v ? 'bg-surface font-medium shadow-sm' : 'text-muted hover:text-fg')}
        >
          {icon}
          {label}
        </button>
      ))}
    </div>
  )
}

function ColorRow({ label, hint, value, fallback, onChange, testId }: { label: string; hint: string; value?: string; fallback: string; onChange: (v: string) => void; testId: string }) {
  const bad = !!value && !isCssColor(value)
  return (
    <Row label={label} hint={bad ? 'Use #hex, a colour name, rgb(), hsl() or oklch().' : hint}>
      <div className="flex w-full items-center gap-2">
        <input type="color" value={hexColor(value, fallback)} onChange={(e) => onChange(e.target.value)} aria-label={`${label} picker`} className="h-8 w-10 shrink-0 cursor-pointer rounded-md border border-line bg-surface p-0.5" />
        <input value={value ?? ''} placeholder={fallback} onChange={(e) => onChange(e.target.value.trim())} aria-label={label} aria-invalid={bad} className={cx(inputCls, 'font-mono', bad && 'border-red-500')} data-testid={testId} />
      </div>
    </Row>
  )
}

function GreyRows({ b, set }: { b: Brand; set: (p: Partial<Brand>) => void }) {
  const g = b.grey
  const custom = (g?.scale?.length ?? 0) > 0
  const hue = g?.hue ?? DEFAULT_HUE
  const chroma = g?.chroma ?? (g ? 0 : DEFAULT_CHROMA)
  const put = (p: { hue?: number; chroma?: number }) => set({ grey: { hue, chroma, ...p } }) // a slider replaces a custom scale
  const swatches = custom && g?.scale?.length === 11 ? g.scale : greyScale(hue, chroma)
  return (
    <>
      <Row label="Grey hue" hint={custom ? 'The config file sets a custom grey scale; moving a slider replaces it.' : 'The tint of every grey (0-360).'}>
        <Slider label="Grey hue" min={0} max={360} step={1} value={hue} onChange={(v) => put({ hue: v })} format={(v) => `${v}°`} />
      </Row>
      <Row label="Grey chroma" hint="0 is neutral grey; higher is more colourful.">
        <Slider label="Grey chroma" min={0} max={0.05} step={0.001} value={chroma} onChange={(v) => put({ chroma: v })} format={(v) => v.toFixed(3)} />
      </Row>
      <div className="flex items-center gap-3 px-5 py-3">
        <div className="flex h-6 flex-1 overflow-hidden rounded-md border border-line" aria-label="Grey scale">
          {swatches.map((c, i) => (
            <i key={i} className="flex-1" style={{ background: c }} />
          ))}
        </div>
        {g && (
          <Btn variant="ghost" onClick={() => set({ grey: undefined })} title="Use the built-in greys">
            <RotateCcw size={13} /> Default
          </Btn>
        )}
      </div>
    </>
  )
}

function Slider({ label, min, max, step, value, onChange, format }: { label: string; min: number; max: number; step: number; value: number; onChange: (v: number) => void; format: (v: number) => string }) {
  return (
    <div className="flex w-full items-center gap-3">
      <input type="range" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} aria-label={label} className="min-w-0 flex-1 accent-[var(--accent)]" />
      <span className="w-12 shrink-0 text-right font-mono text-xs tabular-nums text-muted">{format(value)}</span>
    </div>
  )
}

/** A checkerboard so transparent images stay visible; dark slots sit on a dark tile. */
function Thumb({ url, dark, className }: { url?: string; dark?: boolean; className?: string }) {
  const checker: CSSProperties = {
    backgroundColor: dark ? '#14171a' : '#ffffff',
    backgroundImage: `conic-gradient(${dark ? '#1d2125' : '#f1f3f4'} 25%, transparent 0 50%, ${dark ? '#1d2125' : '#f1f3f4'} 0 75%, transparent 0)`,
    backgroundSize: '12px 12px',
  }
  return (
    <span className={cx('grid shrink-0 place-items-center overflow-hidden rounded-xl border border-line', className ?? 'size-14')} style={checker}>
      {url ? <img src={url} alt="" className="max-h-full max-w-full object-contain p-1.5" /> : <ImageUp size={18} className={dark ? 'text-gray-500' : 'text-gray-400'} />}
    </span>
  )
}

function ImageSlot({ s, value, onChange, onPick }: { s: Slot; value: string; onChange: (url: string) => void; onPick: () => void }) {
  const qc = useQueryClient()
  const input = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [over, setOver] = useState(false)
  const upload = async (f?: File) => {
    if (!f) return
    setBusy(true)
    await attempt(async () => {
      const a = await uploadBrandAsset(f, s.slot)
      onChange(a.url)
      void qc.invalidateQueries({ queryKey: ['admin', 'brand-assets'] })
    }, `${s.label}: uploaded`)
    setBusy(false)
  }
  return (
    <div
      className={cx('@container px-5 py-4 transition-colors', over ? 'bg-accent-soft ring-2 ring-inset ring-accent' : 'hover:bg-surface-2/40')}
      onDragOver={(e) => {
        e.preventDefault()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault()
        setOver(false)
        void upload(e.dataTransfer.files[0])
      }}
      data-testid={`brand-slot-${s.slot}`}
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-3 @md:flex-nowrap">
        <Thumb url={value} dark={s.dark} />
        <div className="min-w-0 flex-1">
          <span className="text-sm font-medium">{s.label}</span>
          <p className="mt-0.5 text-[13px] leading-snug text-muted">{s.hint}</p>
          <p className="mt-0.5 truncate font-mono text-[11px] text-muted" title={value}>
            {value ? brandFile(value) || value : 'Not set'}
          </p>
        </div>
        <div className="flex w-full shrink-0 items-center justify-end gap-1 @md:w-auto">
          <Btn onClick={() => input.current?.click()} disabled={busy} data-testid={`brand-upload-${s.slot}`}>
            <Upload size={14} /> {busy ? 'Uploading' : 'Upload'}
          </Btn>
          <IconButton label="Choose an existing image" onClick={onPick}>
            <Images size={16} />
          </IconButton>
          <IconButton label="Clear" onClick={() => onChange('')} disabled={!value}>
            <X size={16} />
          </IconButton>
        </div>
      </div>
      <input
        ref={input}
        type="file"
        hidden
        accept="image/png,image/jpeg,image/webp,image/svg+xml,image/x-icon,.ico"
        onChange={(e) => {
          void upload(e.target.files?.[0])
          e.target.value = ''
        }}
      />
    </div>
  )
}

function AssetPicker({ title, current, onPick, onClose }: { title: string; current: string; onPick: (url: string) => void; onClose: () => void }) {
  const q = useBrandAssets()
  const qc = useQueryClient()
  const [deleting, setDeleting] = useState<BrandAsset | null>(null)
  const groups: [BrandAsset['dir'], string][] = [
    ['uploads', 'Uploaded here'],
    ['config', 'Config file brand directory (read-only)'],
  ]
  return (
    <Dialog open onClose={onClose} label={title} testId="brand-picker" className="w-[min(46rem,calc(100vw-2rem))]">
      <DialogHeader title={title} onClose={onClose} />
      <div className="max-h-[65vh] space-y-5 overflow-y-auto p-4">
        <Loading q={q} />
        {q.data && !q.data.length && <p className="text-sm text-muted">No images yet. Upload one from its row.</p>}
        {groups.map(([dir, label]) => {
          const list = q.data?.filter((a) => a.dir === dir) ?? []
          if (!list.length) return null
          return (
            <section key={dir}>
              <h3 className="mb-2 text-xs font-medium text-muted">{label}</h3>
              <div className="grid grid-cols-3 gap-2 sm:grid-cols-5">
                {list.map((a) => (
                  <div key={a.dir + a.file} className="group relative">
                    <button
                      type="button"
                      onClick={() => onPick(a.url)}
                      title={a.file}
                      className={cx('flex w-full flex-col items-center gap-1 rounded-xl border p-1.5 text-left hover:border-accent', a.file === current ? 'border-accent ring-2 ring-accent/30' : 'border-line')}
                    >
                      <Thumb url={a.url} className="aspect-square w-full" />
                      <span className="w-full truncate font-mono text-[10px] text-muted">{a.file}</span>
                      <span className="text-[10px] text-muted">{Math.max(1, Math.round(a.size / 1024))} KB</span>
                    </button>
                    {dir === 'uploads' && (
                      <IconButton label={`Delete ${a.file}`} onClick={() => setDeleting(a)} className="absolute right-2 top-2 bg-surface/90 opacity-0 shadow-sm hover:text-red-600 focus-visible:opacity-100 group-hover:opacity-100">
                        <Trash2 size={14} />
                      </IconButton>
                    )}
                  </div>
                ))}
              </div>
            </section>
          )
        })}
      </div>
      <Confirm
        open={!!deleting}
        title="Delete image"
        body={
          <>
            Delete <span className="font-mono">{deleting?.file}</span>? A brand field still pointing at it shows a broken image.
          </>
        }
        onClose={() => setDeleting(null)}
        onConfirm={async () => {
          await api(`/api/v1/admin/brand/assets/${enc(deleting!.file)}`, { method: 'DELETE' })
          await qc.invalidateQueries({ queryKey: ['admin', 'brand-assets'] })
        }}
      />
    </Dialog>
  )
}

/** Mini sign-in page and chat drawn with the draft's tokens. */
function Preview({ b }: { b: Brand }) {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const vars = previewVars(b, dark) as CSSProperties
  const mark = b.mark || b.favicon
  const name = b.name || 'Kera'
  const glass = b.login?.style === 'glass-card'
  const card: CSSProperties = dark
    ? { background: 'color-mix(in srgb, var(--gray-950) 60%, transparent)', boxShadow: '0 24px 80px -20px #000, 0 0 60px -30px var(--accent-bright)' }
    : { background: 'color-mix(in srgb, #fff 72%, transparent)', boxShadow: '0 24px 70px -24px color-mix(in srgb, var(--accent) 45%, transparent)' }
  return (
    <Card
      className="xl:sticky xl:top-4"
      title={<TitleRow icon={<SwatchBook size={18} className={iconCls} />} title="Preview" />}
      hint="Draft values, before saving."
      actions={
        <Segmented
          value={dark ? 'dark' : 'light'}
          onChange={(v) => setDark(v === 'dark')}
          options={[
            ['light', '', <Sun size={14} key="l" aria-label="Light" />],
            ['dark', '', <Moon size={14} key="d" aria-label="Dark" />],
          ]}
        />
      }
    >
      <div style={{ ...vars, color: 'var(--fg)' }} className="space-y-3 p-4" data-testid="brand-preview">
        <div className="relative isolate grid h-[22rem] place-items-center overflow-clip rounded-xl border border-line bg-bg px-4">
          {glass ? (
            <>
              <Aurora variant="login" />
              <div className="login-card" style={{ ...card, padding: '1.75rem 1.25rem 1.5rem', borderRadius: 22 }} data-testid="preview-login">
                {mark && (
                  <div className="login-mark" style={{ width: 52, height: 52, marginBottom: '1.5rem' }}>
                    <img src={mark} alt="" />
                  </div>
                )}
                <div className="text-xl tracking-tight">Sign in to {name}</div>
                {b.tagline && <p className="mt-1 text-[13px] text-muted">{b.tagline}</p>}
                <hr className="mx-auto mb-3 mt-6 w-3/5 border-line" />
                <div className="flex items-center justify-center gap-2 rounded-full bg-surface-2 px-4 py-2.5 text-[13px] font-medium">Continue with Google</div>
              </div>
              {b.login?.wordmark && <div className="brand-wordmark absolute bottom-4 left-1/2 -translate-x-1/2 whitespace-nowrap">{b.login.wordmark}</div>}
            </>
          ) : (
            <div className="w-full max-w-xs rounded-2xl border border-line bg-surface p-6 text-center shadow-pop" data-testid="preview-login">
              {mark && <img src={mark} alt="" className="mx-auto mb-3 size-10 object-contain" />}
              <div className="text-xl font-semibold tracking-tight">{name}</div>
              {b.tagline && <p className="mt-1 text-[13px] text-muted">{b.tagline}</p>}
              <div className="mt-6 rounded-xl border border-line bg-surface px-4 py-2 text-[13px] font-medium">Continue with Google</div>
            </div>
          )}
        </div>
        <div className="flex h-60 overflow-hidden rounded-xl border border-line bg-bg text-[13px]" data-testid="preview-chat">
          <aside className="flex w-36 shrink-0 flex-col gap-0.5 border-r border-line bg-bg-soft p-2">
            <div className="flex items-center gap-1.5 px-1 pb-2 pt-1 font-semibold tracking-tight">
              {mark && <img src={mark} alt="" className="size-5 shrink-0 object-contain" />}
              <span className="truncate">{name}</span>
            </div>
            <div className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-[12px] font-medium">
              <SquarePen size={12} /> New chat
            </div>
            <div className="mt-2 px-1.5 text-[10px] font-medium text-muted">Today</div>
            <div className="truncate rounded-md bg-surface-2 px-1.5 py-1 text-[12px]" style={{ boxShadow: 'inset 2px 0 0 var(--accent-bright)' }}>
              Quarterly summary
            </div>
            <div className="truncate rounded-md px-1.5 py-1 text-[12px] text-muted">Trip plan</div>
            <div className="truncate rounded-md px-1.5 py-1 text-[12px] text-muted">Release notes</div>
          </aside>
          <div className="relative isolate flex min-w-0 flex-1 flex-col">
            {b.background === 'aurora' && <Aurora />}
            <div className="flex h-9 shrink-0 items-center px-3 text-[12px] font-medium">Assistant</div>
            <div className="flex-1 space-y-2 overflow-hidden px-3">
              <div className="ml-auto w-fit max-w-[85%] rounded-2xl bg-surface-2 px-2.5 py-1.5 text-[12px]">Summarise the quarter</div>
              <p className="text-[12px] leading-snug">
                Revenue grew 12%. See the <span className="text-link underline">full report</span>.
              </p>
            </div>
            <div className="m-2 flex items-center gap-2 rounded-xl border border-line bg-surface px-2.5 py-1.5 text-[12px] text-muted">
              Ask anything
              <span className="ml-auto grid size-5 place-items-center rounded-full bg-accent text-white">
                <ArrowUp size={11} />
              </span>
            </div>
          </div>
        </div>
      </div>
    </Card>
  )
}
