// One app in the picker: icon, name, a status word, one line of description (all of it on request), a switch.
import { useState } from 'react'
import { useLocation } from 'react-router'
import { ChevronDown, Wrench } from 'lucide-react'
import type { ToolServer } from '@/api/types'
import { cx } from '@/lib/format'
import { iconFor } from '@/lib/icons'
import { uiConfig } from '@/lib/ui-config'
import { connectHref } from '@/features/integrations/oauth'
import { toolStatus } from './tools'
import { Pill } from './ui'

const tone = { ready: 'ok', connect: 'warn', error: 'bad', local: 'warn' } as const

/** The icon and label an integration gives its tool server in the config, else the server's own name. */
export function appLook(s: ToolServer) {
  const i = uiConfig().integrations.flatMap((x) => (x.kind === 'oauthToolServer' && x.toolServerId === s.id ? [x] : []))[0]
  return { label: i?.label ?? s.name, Icon: i ? iconFor(i.icon) : Wrench }
}

/** Where Connect returns to: this step of the wizard, with the app ticked (?tools=). */
export const backHere = (pathname: string, search: string, id: string) => {
  const p = new URLSearchParams(search)
  p.set('step', 'tools')
  p.set('tools', id)
  return `${pathname}?${p}`
}

export function ToolCard({ server, on, onToggle, assistantLocal }: { server: ToolServer; on: boolean; onToggle: (on: boolean) => void; assistantLocal: boolean }) {
  const [open, setOpen] = useState(false)
  const { pathname, search } = useLocation()
  const st = toolStatus(server, assistantLocal)
  const { label, Icon } = appLook(server)
  const fns = server.functions ?? []
  const detailId = `app-detail-${server.id}`
  return (
    <li data-testid="app-row" data-id={server.id} data-state={st.state} className={cx('min-w-0 rounded-xl border transition-colors motion-reduce:transition-none', on && st.selectable ? 'border-accent bg-accent-soft' : st.selectable ? 'border-line bg-surface' : 'border-line bg-surface-2/50')}>
      <label className={cx('flex items-center gap-3 rounded-xl p-3 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-accent', st.selectable ? 'cursor-pointer' : 'cursor-not-allowed')}>
        <input type="checkbox" role="switch" className="peer sr-only" checked={on && st.selectable} disabled={!st.selectable} onChange={(e) => onToggle(e.target.checked)} data-testid={`app-${server.id}`} aria-describedby={st.reason ? `${detailId}-why` : undefined} />
        <span className={cx('grid size-9 shrink-0 place-items-center rounded-lg', st.selectable ? 'bg-accent-soft text-accent-ink' : 'bg-surface-2 text-muted')}>
          <Icon size={17} aria-hidden />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className="min-w-0 truncate text-sm font-medium">{label}</span>
            <Pill tone={tone[st.state]} testId={`app-status-${server.id}`}>
              {st.label}
            </Pill>
          </span>
          {server.description && (
            <span className="mt-0.5 block truncate text-xs text-muted" title={server.description}>
              {server.description}
            </span>
          )}
        </span>
        <span aria-hidden className={cx('relative h-5 w-9 shrink-0 rounded-full bg-gray-300 transition-colors motion-reduce:transition-none dark:bg-gray-600', 'peer-checked:bg-accent peer-disabled:opacity-40', 'after:absolute after:left-0.5 after:top-0.5 after:size-4 after:rounded-full after:bg-white after:shadow after:transition-transform after:motion-reduce:transition-none peer-checked:after:translate-x-4')} />
      </label>
      {!st.selectable && st.reason && (
        <p id={`${detailId}-why`} className="px-3 pb-2.5 pl-15 text-xs leading-snug text-muted">
          {st.reason}
        </p>
      )}
      <div className="flex items-center gap-2 px-3 pb-2.5 pl-15">
        {(server.description || fns.length > 0) && (
          <button type="button" aria-expanded={open} aria-controls={detailId} onClick={() => setOpen(!open)} className="inline-flex items-center gap-1 rounded-md py-0.5 text-xs text-muted hover:text-fg">
            <ChevronDown size={13} aria-hidden className={cx('transition-transform motion-reduce:transition-none', open && 'rotate-180')} />
            {open ? 'Hide details' : 'Details'}
          </button>
        )}
        {st.connect && (
          <a href={connectHref(server.provider || server.id, backHere(pathname, search, server.id))} data-testid={`connect-${server.id}`} className="ml-auto rounded-lg bg-accent px-3 py-1 text-xs font-medium text-white hover:opacity-90">
            {st.connect}
          </a>
        )}
      </div>
      {open && (
        <div id={detailId} className="space-y-2 border-t border-line px-3 py-3 pl-15 text-xs leading-relaxed">
          {server.description && <p className="text-fg">{server.description}</p>}
          {fns.length > 0 && (
            <p className="text-muted">
              <span className="font-medium text-fg">{fns.length === 1 ? '1 tool' : `${fns.length} tools`}:</span> {fns.join(', ')}
            </p>
          )}
        </div>
      )}
    </li>
  )
}
