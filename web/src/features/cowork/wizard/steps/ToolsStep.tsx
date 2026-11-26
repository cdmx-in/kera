// Step 3: the apps and tools the task may use. Search, a count of what is on, a connection check, and cards that say
// plainly why one cannot be used (not connected, not answering, needs a local assistant).
import { useMemo, useState } from 'react'
import { useLocation } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, CircleAlert, Search, Wrench } from 'lucide-react'
import { useModels, useTools } from '@/api/queries'
import { api } from '@/api/client'
import type { ToolServer } from '@/api/types'
import { cx } from '@/lib/format'
import { useComposer } from '@/stores/composer'
import { Spinner } from '@/ui/misc'
import { connectHref } from '@/features/integrations/oauth'
import { defaultToolIds } from '@/features/chat/model-select'
import { uiConfig } from '@/lib/ui-config'
import { toolKey } from '@/features/tools/ids'
import { input } from '@/features/workspace/ui'
import { ToolCard, appLook, backHere } from '../ToolCard'
import { checkConnections, groupTools, leftOut, pickable, searchTools, selectedApps, toolStatus, type CheckResult } from '../tools'
import { Block, Pill, RemovableChip, StepHeader } from '../ui'

export function ToolsStep({ schedulable }: { schedulable: boolean }) {
  const tools = useTools()
  const models = useModels()
  const qc = useQueryClient()
  const { pathname, search } = useLocation()
  const model = useComposer((s) => s.model)
  const toolIds = useComposer((s) => s.toolIds)
  const toggle = useComposer((s) => s.toggleTool)
  const setToolIds = useComposer((s) => s.setToolIds)
  const [query, setQuery] = useState('')
  const [checked, setChecked] = useState<CheckResult[] | null>(null)
  const [checking, setChecking] = useState(false)
  const m = models.data?.find((x) => x.id === model)
  const local = !!m?.local
  const list = pickable(tools.data)
  const groups = useMemo(() => groupTools(searchTools(list, query, (s) => appLook(s).label), local), [list, query, local])
  const selected = selectedApps(toolIds, tools.data, local)
  const dropped = leftOut(toolIds, tools.data, local)
  const connectedIds = list.filter((s) => toolStatus(s, local).selectable && s.auth_type === 'user_oauth').map((s) => s.id)

  // A fresh listing asks every server, so it shows an expired sign-in as well as a missing one.
  const check = async () => {
    setChecking(true)
    try {
      const fresh = await api<ToolServer[]>('/api/v1/tools/')
      qc.setQueryData(['tools'], fresh)
      setChecked(checkConnections(toolIds, fresh, local))
    } catch {
      setChecked([{ id: '', name: 'Apps', ok: false, text: 'Could not reach the server. Try again.' }])
    } finally {
      setChecking(false)
    }
  }

  return (
    <div className="space-y-6">
      <StepHeader title="Which apps can it use?">Switch on what the task needs. It can only use the apps you pick here.{schedulable && ' A scheduled task fails, and says why, if one of them is disconnected when it runs.'}</StepHeader>

      {tools.isPending ? (
        <div className="grid place-items-center py-10 text-muted">
          <Spinner size={20} />
        </div>
      ) : tools.isError ? (
        <p role="alert" className="rounded-xl border border-line px-4 py-6 text-center text-sm text-muted">
          Could not load the apps. Reload the page to try again.
        </p>
      ) : !list.length ? (
        <p data-testid="no-apps" className="rounded-xl border border-dashed border-line px-4 py-8 text-center text-sm text-muted">
          No apps or tools are set up for you yet. You can carry on: the assistant still works with its own abilities and any knowledge you picked.
        </p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative min-w-52 flex-1 sm:max-w-sm">
              <Search size={15} aria-hidden className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted" />
              <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search apps" aria-label="Search apps" className={cx(input, 'pl-9')} data-testid="app-search" />
            </div>
            <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Quick choices">
              <button type="button" data-testid="apps-defaults" onClick={() => m && setToolIds(defaultToolIds(m, tools.data, uiConfig().integrations))} className="rounded-lg border border-line px-2.5 py-1.5 text-xs font-medium hover:bg-surface-2">
                Assistant defaults
              </button>
              <button type="button" data-testid="apps-connected" disabled={!connectedIds.length} onClick={() => setToolIds([...toolIds, ...connectedIds])} className="rounded-lg border border-line px-2.5 py-1.5 text-xs font-medium hover:bg-surface-2 disabled:opacity-50">
                All connected
              </button>
              <button type="button" data-testid="apps-none" disabled={!selected.length} onClick={() => setToolIds(toolIds.filter((id) => !list.some((s) => s.id === toolKey(id))))} className="rounded-lg border border-line px-2.5 py-1.5 text-xs font-medium hover:bg-surface-2 disabled:opacity-50">
                None
              </button>
            </div>
            <button type="button" data-testid="check-connections" onClick={() => void check()} disabled={checking} className="ml-auto inline-flex items-center gap-1.5 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium hover:bg-surface-2 disabled:opacity-60">
              {checking ? <Spinner size={13} /> : <CheckCircle2 size={14} aria-hidden />} Check connections
            </button>
          </div>

          <div data-testid="selected-apps" className="rounded-xl border border-line bg-surface-2/40 px-3.5 py-3">
            <h3 className="flex items-center gap-2 text-sm font-semibold">
              <Wrench size={14} aria-hidden className="text-muted" /> Selected <span className="rounded-full bg-accent-soft px-2 py-px text-xs font-medium tabular-nums text-accent-ink">{selected.length}</span>
            </h3>
            {selected.length ? (
              <ul className="mt-2 flex flex-wrap gap-2" aria-label="Selected apps">
                {selected.map((s) => (
                  <RemovableChip key={s.id} id={s.id} testId="selected-app" label={`Remove ${appLook(s).label}`} onRemove={() => toggle(s.id, false)}>
                    {appLook(s).label}
                  </RemovableChip>
                ))}
              </ul>
            ) : (
              <p className="mt-1 text-xs text-muted">Nothing switched on. The assistant works with its own abilities and the knowledge you picked.</p>
            )}
            {dropped.length > 0 && (
              <p role="status" data-testid="left-out" className="mt-2 text-xs text-warn-ink">
                Left out of this task: {dropped.map((s) => appLook(s).label).join(', ')}. {dropped.length === 1 ? 'It is' : 'They are'} switched on but cannot be used with this assistant right now.
              </p>
            )}
          </div>

          {checked && (
            <section aria-label="Connection check" data-testid="connection-check" className="rounded-xl border border-line bg-surface px-3.5 py-3">
              <h3 className="text-sm font-semibold">Connection check</h3>
              {checked.length ? (
                <ul className="mt-2 space-y-1.5">
                  {checked.map((r) => (
                    <li key={r.id || r.text} data-testid="check-row" data-ok={r.ok} className="flex flex-wrap items-center gap-2 text-sm">
                      {r.ok ? <CheckCircle2 size={15} aria-hidden className="text-ok" /> : <CircleAlert size={15} aria-hidden className="text-bad" />}
                      <span className="font-medium">{r.name}</span>
                      <Pill tone={r.ok ? 'ok' : 'bad'}>{r.ok ? 'OK' : 'Needs attention'}</Pill>
                      <span className="text-xs text-muted">{r.text}</span>
                      {r.connect && r.id && (
                        <a href={connectHref(list.find((s) => s.id === r.id)?.provider || r.id, backHere(pathname, search, r.id))} className="ml-auto text-xs font-medium text-link underline">
                          {r.connect}
                        </a>
                      )}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="mt-1 text-xs text-muted">Nothing is switched on, so there is nothing to check.</p>
              )}
            </section>
          )}

          {groups.length ? (
            groups.map((g) => (
              <Block key={g.id} title={g.title} hint={g.hint}>
                <ul className="grid grid-cols-1 items-start gap-2.5 lg:grid-cols-2 2xl:grid-cols-3" data-testid={`apps-${g.id}`}>
                  {g.items.map((s) => (
                    <ToolCard key={s.id} server={s} assistantLocal={local} on={toolIds.map(toolKey).includes(s.id)} onToggle={(on) => toggle(s.id, on)} />
                  ))}
                </ul>
              </Block>
            ))
          ) : (
            <p className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-sm text-muted">No app matches “{query}”.</p>
          )}
        </>
      )}
    </div>
  )
}
