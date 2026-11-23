// Admin > Settings > Integrations: a list of scheduled syncs with outside
// systems (GET /api/v1/admin/connectors); each opens a page with its runs,
// Dry run / Sync now (POST .../{name}/run) and its settings.
import { useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, Plug, RefreshCw } from 'lucide-react'
import { Link } from 'react-router'
import { api } from '@/api/client'
import { cx } from '@/lib/format'
import { attempt, Badge, Btn, Card, Loading, tdCls, thCls } from '../ui'
import { SettingsForm, type Draft, type FieldSpec } from './form'
import { IconTile, ProviderIcon } from './serviceIcons'

interface Change {
  action: string
  target: string
  detail?: string
}

interface Run {
  id: number
  connector: string
  trigger: string
  dry_run: boolean
  started_at: string
  finished_at: string | null
  error: string
  report: { counts?: Record<string, number>; changes?: Change[]; warnings?: string[] }
}

interface Connector {
  name: string
  title: string
  enabled: boolean
  interval_minutes: number
  running: boolean
  next_run: string | null
}

export const when = (s: string) => new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/** "in 12 min" / "5 min ago", hours past 90 minutes, days past 48 hours; "now" within a minute. */
export function relative(s: string): string {
  const min = Math.round((new Date(s).getTime() - Date.now()) / 60000)
  if (min === 0) return 'now'
  const n = Math.abs(min)
  const t = n < 90 ? `${n} min` : n < 48 * 60 ? `${Math.round(n / 60)} h` : `${Math.round(n / 1440)} days`
  return min > 0 ? `in ${t}` : `${t} ago`
}

const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

const triggers: Record<string, string> = { admin: 'Admin', schedule: 'Schedule', cli: 'Command line' }

// Past-tense labels for report actions; unknown actions are shown as they come.
const actions: Record<string, string> = {
  create: 'Created',
  link: 'Linked',
  update: 'Updated',
  disable: 'Disabled',
  enable: 'Enabled',
  missing: 'Missing',
  delete: 'Deleted',
  group: 'New group',
  join: 'Joined group',
  leave: 'Left group',
}
const actionLabel = (a: string) => actions[a] ?? sentence(a)

const f = (key: string, kind: FieldSpec['kind'], label: string, hint?: string): FieldSpec => ({ key, kind, label, hint })
const gw = 'connectors.google_workspace.'

/** Per-integration UI: a new integration adds one entry (unknown ones get a plug icon and no settings form). */
const integrations: Record<string, { description: string; icon: ReactNode; fields: FieldSpec[]; warning?: (d: Draft) => ReactNode }> = {
  google_workspace: {
    description: 'Users and departments from Google Workspace. Suspended and deleted users are disabled here.',
    icon: <ProviderIcon p={{ id: 'google' }} />,
    fields: [
      f(gw + 'enabled', 'bool', 'Enabled', 'Run on a schedule.'),
      f(gw + 'interval_minutes', 'int', 'Every (minutes)', 'At least 5.'),
      f(gw + 'credentials', 'secret', 'Service-account key', 'The JSON key of a service account with domain-wide delegation for the scope admin.directory.user.readonly.'),
      f(gw + 'admin_email', 'text', 'Admin to act as', "Email of a Workspace admin user who can read users. Not the service account's own address."),
      f(gw + 'oauth_provider', 'text', 'Login provider id', 'The Google sign-in provider, so synced users are linked to their Google login.'),
      f(gw + 'exclude', 'lines', 'Leave alone', 'Emails the sync never creates, changes, disables or deletes (bots, break-glass admins).'),
      f(gw + 'delete_after_days', 'int', 'Delete after (days)', 'How long a user deleted from Workspace stays disabled here before being deleted.'),
    ],
    warning: (d) =>
      String(d.get(gw + 'admin_email') ?? '')
        .toLowerCase()
        .endsWith('.gserviceaccount.com') && (
        <p role="alert" className="rounded-lg bg-amber-500/10 px-4 py-3 text-sm text-amber-800 dark:text-amber-300">
          Admin to act as is the service account&apos;s own address. Use a Workspace admin user&apos;s email instead; the service account acts on their behalf.
        </p>
      ),
  },
}

const iconOf = (name: string) =>
  integrations[name]?.icon ?? (
    <IconTile>
      <Plug size={18} className="text-accent dark:text-accent-bright" aria-hidden />
    </IconTile>
  )

const base = '/admin/settings/integrations'

function useConnectors() {
  return useQuery({
    queryKey: ['admin', 'connectors'],
    queryFn: () => api<{ connectors: Connector[]; runs: Run[] }>('/api/v1/admin/connectors'),
    refetchInterval: (query) => (query.state.data?.connectors.some((c) => c.running) ? 3000 : false),
  })
}

/** The list, or one integration's page when name is set (the URL segment after /integrations/). */
export function IntegrationsTab({ name }: { name?: string }) {
  const q = useConnectors()
  if (!q.data) return <Loading q={q} />
  if (name) {
    const c = q.data.connectors.find((x) => x.name === name)
    if (!c)
      return (
        <p className="text-sm text-muted">
          Unknown integration.{' '}
          <Link to={base} className="text-accent underline">
            All integrations
          </Link>
        </p>
      )
    return <Detail c={c} runs={q.data.runs.filter((r) => r.connector === name)} refetch={() => void q.refetch()} />
  }
  return (
    <Card
      title="Integrations"
      hint="Scheduled syncs with outside systems. Open one to see its runs or change its settings."
      actions={
        <Btn variant="ghost" onClick={() => void q.refetch()} aria-label="Refresh">
          <RefreshCw size={14} />
        </Btn>
      }
    >
      <ul className="divide-y divide-line">
        {q.data.connectors.map((c) => {
          const last = q.data.runs.find((r) => r.connector === c.name && !r.dry_run)
          return (
            <li key={c.name}>
              <Link to={`${base}/${encodeURIComponent(c.name)}`} className="flex items-center gap-4 px-5 py-4 transition-colors hover:bg-surface-2/40" data-testid={`connector-${c.name}`}>
                {iconOf(c.name)}
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
                    {c.title} <Schedule c={c} />
                  </div>
                  {integrations[c.name] && <p className="mt-0.5 text-[13px] text-muted">{integrations[c.name].description}</p>}
                  <p className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
                    {last ? (
                      <>
                        <RunBadge r={last} />
                        <span title={when(last.started_at)}>Last run {relative(last.started_at)}</span>
                      </>
                    ) : (
                      <span>Never run</span>
                    )}
                    <NextRun c={c} />
                  </p>
                </div>
                <ChevronRight size={16} className="shrink-0 text-muted" aria-hidden />
              </Link>
            </li>
          )
        })}
      </ul>
    </Card>
  )
}

function Schedule({ c }: { c: Connector }) {
  if (c.running) return <Badge tone="amber">Running</Badge>
  return c.enabled ? <Badge tone="green">Every {c.interval_minutes} min</Badge> : <Badge>Off</Badge>
}

function NextRun({ c }: { c: Connector }) {
  if (!c.next_run || c.running) return null
  return (
    <span title={when(c.next_run)}>
      <span aria-hidden>· </span>Next run {relative(c.next_run)}
    </span>
  )
}

function RunBadge({ r }: { r: Run }) {
  return r.error ? <Badge tone="red">Failed</Badge> : r.finished_at ? <Badge tone="green">Succeeded</Badge> : <Badge tone="amber">Running</Badge>
}

const detailTabs = [
  ['runs', 'Runs'],
  ['settings', 'Settings'],
] as const

function Detail({ c, runs, refetch }: { c: Connector; runs: Run[]; refetch: () => void }) {
  const qc = useQueryClient()
  const [tab, setTab] = useState<'runs' | 'settings'>('runs')
  const ui = integrations[c.name]
  const start = (dry: boolean) =>
    attempt(
      async () => {
        await api(`/api/v1/admin/connectors/${encodeURIComponent(c.name)}/run${dry ? '?dry_run=1' : ''}`, { method: 'POST' })
        await qc.invalidateQueries({ queryKey: ['admin', 'connectors'] })
      },
      dry ? 'Dry run started' : 'Sync started',
    )
  return (
    <div className="space-y-4">
      <Link to={base} className="inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
        <ChevronLeft size={14} aria-hidden /> Integrations
      </Link>
      <div className="flex flex-wrap items-center gap-3">
        {iconOf(c.name)}
        <div className="min-w-0 flex-1">
          <h2 className="flex flex-wrap items-center gap-2 text-lg font-semibold tracking-tight">
            {c.title} <Schedule c={c} />
          </h2>
          <p className="text-[13px] text-muted">
            {ui?.description} <NextRun c={c} />
          </p>
        </div>
        <div className="flex gap-2">
          <Btn variant="ghost" onClick={refetch} aria-label="Refresh">
            <RefreshCw size={14} />
          </Btn>
          <Btn disabled={c.running} onClick={() => void start(true)} data-testid="connector-dry-run">
            Dry run
          </Btn>
          <Btn variant="primary" disabled={c.running} onClick={() => void start(false)} data-testid="connector-run">
            {c.running ? 'Running…' : 'Sync now'}
          </Btn>
        </div>
      </div>
      <div role="tablist" aria-label={`${c.title} sections`} className="flex gap-1 border-b border-line">
        {detailTabs.map(([k, l]) => (
          <button
            key={k}
            role="tab"
            type="button"
            aria-selected={tab === k}
            onClick={() => setTab(k)}
            className={cx('whitespace-nowrap border-b-2 px-3 py-2 text-sm', tab === k ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg')}
          >
            {l}
          </button>
        ))}
      </div>
      {tab === 'runs' ? (
        <Runs runs={runs} />
      ) : ui ? (
        <SettingsForm single children={ui.warning} sections={[{ title: 'Settings', hint: 'Run a dry run after changing these, before the next scheduled run.', fields: ui.fields }]} />
      ) : (
        <p className="text-sm text-muted">This integration has no settings form here; set connectors.{c.name}.* in the config file.</p>
      )}
    </div>
  )
}

function summary(r: Run): string {
  const c = Object.entries(r.report.counts ?? {})
  return c.length ? c.map(([k, n]) => `${actionLabel(k)} ${n}`).join(' · ') : 'No changes'
}

function Status({ r }: { r: Run }) {
  const warnings = r.report.warnings?.length ?? 0
  return (
    <div className="flex flex-wrap gap-1">
      <RunBadge r={r} />
      {r.dry_run && <Badge tone="accent">Dry run</Badge>}
      {warnings > 0 && <Badge tone="amber">{warnings === 1 ? '1 warning' : `${warnings} warnings`}</Badge>}
    </div>
  )
}

function Details({ r }: { r: Run }) {
  const changes = r.report.changes ?? []
  const warnings = r.report.warnings ?? []
  return (
    <div className="min-w-64 space-y-1">
      {r.error && (
        <p className="line-clamp-3 break-words text-red-600 dark:text-red-400" title={r.error}>
          {sentence(r.error)}
        </p>
      )}
      {changes.length || warnings.length ? (
        <details className="group">
          <summary className="cursor-pointer select-none text-fg marker:text-muted">{summary(r)}</summary>
          <ul className="mt-2 max-h-72 space-y-1 overflow-y-auto rounded-lg bg-surface-2/50 p-3 text-xs">
            {warnings.map((w, i) => (
              <li key={`w${i}`} className="text-amber-700 dark:text-amber-400">
                {sentence(w)}
              </li>
            ))}
            {changes.map((c, i) => (
              <li key={i} className="flex flex-wrap gap-x-1.5">
                <span className="font-medium">{actionLabel(c.action)}</span>
                <span>{c.target}</span>
                {c.detail && <span className="text-muted">· {sentence(c.detail)}</span>}
              </li>
            ))}
          </ul>
        </details>
      ) : (
        !r.error && <span className="text-muted">No changes</span>
      )}
    </div>
  )
}

// ponytail: the API returns the last 20 runs across all integrations; add ?connector= once several run often.
function Runs({ runs }: { runs: Run[] }) {
  return (
    <Card>
      {runs.length === 0 ? (
        <p className="px-5 py-4 text-sm text-muted">No runs yet. Start with a dry run.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm" data-testid="connector-runs">
            <thead>
              <tr>
                <th className={thCls}>Started</th>
                <th className={thCls}>Started by</th>
                <th className={thCls}>Status</th>
                <th className={thCls}>Result</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {runs.map((r) => (
                <tr key={r.id} className="align-top">
                  <td className={cx(tdCls, 'whitespace-nowrap')}>
                    {when(r.started_at)}
                    <div className="text-xs text-muted">{relative(r.started_at)}</div>
                  </td>
                  <td className={cx(tdCls, 'whitespace-nowrap')}>{triggers[r.trigger] ?? sentence(r.trigger)}</td>
                  <td className={tdCls}>
                    <Status r={r} />
                  </td>
                  <td className={cx(tdCls, 'w-full')}>
                    <Details r={r} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}
