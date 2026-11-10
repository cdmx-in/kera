// Admin > Settings > Integrations: scheduled syncs with outside systems
// (GET /api/v1/admin/connectors, POST .../{name}/run) and their settings.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { api } from '@/api/client'
import { cx } from '@/lib/format'
import { attempt, Badge, Btn, Card, Loading, tdCls, thCls } from '../ui'
import { SettingsForm, type FieldSpec } from './form'

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

const when = (s: string) => new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/** "in 12 min", "in 2 h", or "now" for a time in the past. */
function fromNow(s: string): string {
  const min = Math.round((new Date(s).getTime() - Date.now()) / 60000)
  if (min <= 0) return 'now'
  return min < 90 ? `in ${min} min` : `in ${Math.round(min / 60)} h`
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

export function IntegrationsTab() {
  return (
    <div className="space-y-4">
      <Runs />
      <SettingsForm
        single
        children={(d) =>
          String(d.get(gw + 'admin_email') ?? '')
            .toLowerCase()
            .endsWith('.gserviceaccount.com') && (
            <p role="alert" className="rounded-lg bg-amber-500/10 px-4 py-3 text-sm text-amber-800 dark:text-amber-300">
              Admin to act as is the service account&apos;s own address. Use a Workspace admin user&apos;s email instead; the service account acts on their behalf.
            </p>
          )
        }
        sections={[
          {
            title: 'Google Workspace',
            hint: 'Creates every active Workspace user, puts them in a group named after their Department, disables suspended and archived users, and disables users deleted from Workspace, then deletes them (with their chats and files) after the grace period. Run a dry run before enabling.',
            fields: [
              f(gw + 'enabled', 'bool', 'Enabled', 'Run on a schedule.'),
              f(gw + 'interval_minutes', 'int', 'Every (minutes)', 'At least 5.'),
              f(gw + 'credentials', 'secret', 'Service-account key', 'The JSON key of a service account with domain-wide delegation for the scope admin.directory.user.readonly.'),
              f(gw + 'admin_email', 'text', 'Admin to act as', "Email of a Workspace admin user who can read users. Not the service account's own address."),
              f(gw + 'oauth_provider', 'text', 'Login provider id', 'The Google sign-in provider, so synced users are linked to their Google login.'),
              f(gw + 'exclude', 'lines', 'Leave alone', 'Emails the sync never creates, changes, disables or deletes (bots, break-glass admins).'),
              f(gw + 'delete_after_days', 'int', 'Delete after (days)', 'How long a user deleted from Workspace stays disabled here before being deleted.'),
            ],
          },
        ]}
      />
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
      {r.error ? <Badge tone="red">Failed</Badge> : r.finished_at ? <Badge tone="green">Succeeded</Badge> : <Badge tone="amber">Running</Badge>}
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

function Runs() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['admin', 'connectors'],
    queryFn: () => api<{ connectors: Connector[]; runs: Run[] }>('/api/v1/admin/connectors'),
    refetchInterval: (query) => (query.state.data?.connectors.some((c) => c.running) ? 3000 : false),
  })
  const start = (c: Connector, dry: boolean) =>
    attempt(async () => {
      await api(`/api/v1/admin/connectors/${encodeURIComponent(c.name)}/run${dry ? '?dry_run=1' : ''}`, { method: 'POST' })
      await qc.invalidateQueries({ queryKey: ['admin', 'connectors'] })
    }, dry ? 'Dry run started' : 'Sync started')
  const titles = new Map(q.data?.connectors.map((c) => [c.name, c.title]))
  const many = (q.data?.connectors.length ?? 0) > 1

  return (
    <Card
      title="Integrations"
      hint="Enabled integrations run on their interval. A dry run shows what would change without changing anything."
      actions={
        <Btn variant="ghost" onClick={() => void q.refetch()} aria-label="Refresh">
          <RefreshCw size={14} />
        </Btn>
      }
    >
      <Loading q={q} />
      {q.data && (
        <div className="divide-y divide-line">
          {q.data.connectors.map((c) => (
            <div key={c.name} className="flex flex-wrap items-center gap-3 px-5 py-4" data-testid={`connector-${c.name}`}>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-sm font-medium">
                  {c.title} {c.enabled ? <Badge tone="green">Every {c.interval_minutes} min</Badge> : <Badge>Off</Badge>}
                </div>
                <p className="mt-0.5 text-[13px] text-muted">
                  {c.running ? 'Running now…' : c.next_run ? `Next run ${fromNow(c.next_run)} (${when(c.next_run)})` : 'Not scheduled. Switch it on below.'}
                </p>
              </div>
              <Btn disabled={c.running} onClick={() => void start(c, true)} data-testid="connector-dry-run">
                Dry run
              </Btn>
              <Btn variant="primary" disabled={c.running} onClick={() => void start(c, false)} data-testid="connector-run">
                {c.running ? 'Running…' : 'Sync now'}
              </Btn>
            </div>
          ))}
        </div>
      )}
      {q.data && q.data.runs.length === 0 && <p className="border-t border-line px-5 py-4 text-sm text-muted">No runs yet.</p>}
      {q.data && q.data.runs.length > 0 && (
        <div className="overflow-x-auto border-t border-line">
          <table className="w-full text-sm" data-testid="connector-runs">
            <thead>
              <tr>
                <th className={thCls}>Started</th>
                {many && <th className={thCls}>Integration</th>}
                <th className={thCls}>Started by</th>
                <th className={thCls}>Status</th>
                <th className={thCls}>Result</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {q.data.runs.map((r) => (
                <tr key={r.id} className="align-top">
                  <td className={cx(tdCls, 'whitespace-nowrap')}>{when(r.started_at)}</td>
                  {many && <td className={tdCls}>{titles.get(r.connector) ?? r.connector}</td>}
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
