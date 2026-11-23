// Apps and tools a task may use, in the open: every tool server the user can see, checkable, with Connect for the
// apps not linked yet. The same choice lives in the goal box's + menu (composer store toolIds).
import { Wrench } from 'lucide-react'
import { useModels, useTools } from '@/api/queries'
import { iconFor } from '@/lib/icons'
import { uiConfig } from '@/lib/ui-config'
import { useComposer } from '@/stores/composer'
import { connectHref } from '@/features/integrations/oauth'

/** Where Connect returns to: this form, with the app ticked (?tools=). */
const backHere = (id: string) => {
  const u = new URL(location.href)
  u.searchParams.set('tools', id)
  return u.pathname + u.search
}

export function AppsPicker() {
  const servers = useTools()
  const models = useModels()
  const model = useComposer((s) => s.model)
  const toolIds = useComposer((s) => s.toolIds)
  const toggle = useComposer((s) => s.toggleTool)
  const local = !!models.data?.find((m) => m.id === model)?.local
  const labels = new Map(uiConfig().integrations.flatMap((i) => (i.kind === 'oauthToolServer' ? [[i.toolServerId, i] as const] : [])))
  const list = (servers.data ?? []).filter((s) => s.kind !== 'builtin')
  if (!list.length) return null
  return (
    <section data-testid="apps-picker">
      <h2 className="pb-1 text-sm font-medium">Apps and tools</h2>
      <ul className="divide-y divide-line rounded-xl border border-line">
        {list.map((s) => {
          const i = labels.get(s.id)
          const Icon = i ? iconFor(i.icon) : Wrench
          const needsConnect = s.auth_type === 'user_oauth' && !s.authenticated
          const localOnly = !!s.local_only && !local
          return (
            <li key={s.id} data-testid="app-row" data-id={s.id} className="flex items-center gap-3 px-3 py-2">
              <Icon size={16} className="shrink-0 text-muted" />
              <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-2.5">
                <input type="checkbox" checked={!needsConnect && toolIds.includes(s.id)} disabled={needsConnect || !!s.error} onChange={(e) => toggle(s.id, e.target.checked)} data-testid={`app-${s.id}`} className="size-4 shrink-0 accent-[var(--accent)] disabled:opacity-40" />
                <span className="min-w-0">
                  <span className="block truncate text-sm">{i?.label ?? s.name}</span>
                  {s.description && <span className="block truncate text-xs text-muted">{s.description}</span>}
                </span>
              </label>
              {needsConnect ? (
                <a href={connectHref(s.provider || s.id, backHere(s.id))} data-testid={`connect-${s.id}`} className="shrink-0 rounded-lg bg-accent px-3 py-1 text-xs font-medium text-white hover:opacity-90">
                  Connect
                </a>
              ) : s.error ? (
                <span className="shrink-0 text-xs text-red-600 dark:text-red-400" title={s.error}>
                  Unavailable
                </span>
              ) : localOnly ? (
                <span className="shrink-0 text-xs text-amber-700 dark:text-amber-400" title="This app only works with a local assistant; with this one the run would fail">
                  Local assistants only
                </span>
              ) : (
                s.auth_type === 'user_oauth' && <span className="shrink-0 text-xs text-muted">Connected</span>
              )}
            </li>
          )
        })}
      </ul>
      <p className="mt-1.5 text-xs text-muted">Ticked apps are available to this task. A scheduled task fails, and says why, if one of them is disconnected when it runs.</p>
    </section>
  )
}
