// Admin > Settings > Integrations: scheduled syncs with outside systems
// (GET /api/v1/admin/connectors, POST .../{name}/run) and their settings.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { api } from '@/api/client'
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

const when = (s: string) => new Date(s).toLocaleString()

const f = (key: string, kind: FieldSpec['kind'], label: string, hint?: string): FieldSpec => ({ key, kind, label, hint })
const gw = 'connectors.google_workspace.'

export function IntegrationsTab() {
  return (
    <div className="space-y-4">
      <Runs />
      <SettingsForm
        single
        sections={[
          {
            title: 'Google Workspace',
            hint: 'Creates every active Workspace user, puts them in a group named after their Department, disables suspended and archived users, and disables users deleted from Workspace, then deletes them (with their chats and files) after the grace period. Run a dry run before enabling.',
            fields: [
              f(gw + 'enabled', 'bool', 'Enabled', 'Run on a schedule.'),
              f(gw + 'interval_minutes', 'int', 'Every (minutes)', 'At least 5.'),
              f(gw + 'credentials', 'secret', 'Service-account key', 'The JSON key of a service account with domain-wide delegation for the scope admin.directory.user.readonly.'),
              f(gw + 'admin_email', 'text', 'Admin to act as', 'A Workspace admin who can read users.'),
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
  return c.length ? c.map(([k, n]) => `${k} ${n}`).join(' · ') : 'no changes'
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

  return (
    <Card
      title="Integrations"
      hint="Each enabled integration runs on its interval. A dry run shows what would change without changing anything."
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
                  {c.title} {c.enabled ? <Badge tone="green">every {c.interval_minutes} min</Badge> : <Badge>off</Badge>}
                </div>
                {c.next_run && <p className="text-[13px] text-muted">Next run {when(c.next_run)}</p>}
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
      {q.data && q.data.runs.length > 0 && (
        <div className="overflow-x-auto border-t border-line">
          <table className="w-full text-sm" data-testid="connector-runs">
            <thead>
              <tr>
                <th className={thCls}>Started</th>
                <th className={thCls}>Integration</th>
                <th className={thCls}>By</th>
                <th className={thCls}>Status</th>
                <th className={thCls}>Changes</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {q.data.runs.map((r) => (
                <tr key={r.id} className="align-top">
                  <td className={tdCls}>{when(r.started_at)}</td>
                  <td className={tdCls}>{titles.get(r.connector) ?? r.connector}</td>
                  <td className={tdCls}>{r.trigger}</td>
                  <td className={tdCls}>
                    <div className="flex flex-wrap gap-1">
                      {r.dry_run && <Badge tone="accent">dry run</Badge>}
                      {r.error ? <Badge tone="red">failed</Badge> : r.finished_at ? <Badge tone="green">ok</Badge> : <Badge tone="amber">running or stopped</Badge>}
                      {!!r.report.warnings?.length && <Badge tone="amber">{r.report.warnings.length} warnings</Badge>}
                    </div>
                  </td>
                  <td className={tdCls}>
                    {r.error && <p className="mb-1 text-red-600">{r.error}</p>}
                    {r.report.changes?.length || r.report.warnings?.length ? (
                      <details>
                        <summary className="cursor-pointer">{summary(r)}</summary>
                        <ul className="mt-2 space-y-0.5 text-xs">
                          {r.report.warnings?.map((w, i) => (
                            <li key={`w${i}`} className="text-amber-700 dark:text-amber-400">
                              {w}
                            </li>
                          ))}
                          {r.report.changes?.map((c, i) => (
                            <li key={i}>
                              <span className="font-medium">{c.action}</span> {c.target}
                              {c.detail && <span className="text-muted"> ({c.detail})</span>}
                            </li>
                          ))}
                        </ul>
                      </details>
                    ) : (
                      <span className="text-muted">{summary(r)}</span>
                    )}
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
