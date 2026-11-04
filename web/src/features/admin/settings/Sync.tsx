// Admin > Settings > Open WebUI sync: the nightly import's schedule, a run-now
// button and the last runs (GET/POST /api/v1/admin/sync/openwebui).
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { api } from '@/api/client'
import { attempt, Badge, Btn, Card, Loading, tdCls, thCls } from '../ui'

interface SyncRun {
  id: number
  source: string
  trigger: string
  since?: string
  started_at: number
  finished_at: number | null
  error: string
  totals: { inserted: number; existing: number; skipped: number; pruned: number }
}

interface SyncStatus {
  enabled: boolean
  at: string
  timezone: string
  next_run?: number
  running: boolean
  runs: SyncRun[]
}

const when = (s: number) => new Date(s * 1000).toLocaleString()

function duration(r: SyncRun): string {
  if (!r.finished_at) return ''
  const s = r.finished_at - r.started_at
  return s < 60 ? `${s} s` : `${Math.round(s / 60)} min`
}

export function SyncTab() {
  const qc = useQueryClient()
  // Poll while a run is going so the table fills in without a reload.
  const q = useQuery({
    queryKey: ['admin', 'sync'],
    queryFn: () => api<SyncStatus>('/api/v1/admin/sync/openwebui'),
    refetchInterval: (query) => (query.state.data?.running ? 5000 : false),
  })
  const s = q.data
  const runNow = () =>
    attempt(async () => {
      await api('/api/v1/admin/sync/openwebui', { method: 'POST' })
      await qc.invalidateQueries({ queryKey: ['admin', 'sync'] })
    }, 'Sync started')

  return (
    <Card
      title="Open WebUI sync"
      hint={
        s?.enabled
          ? `Imports Open WebUI every day at ${s.at} (${s.timezone})${s.next_run ? `, next ${when(s.next_run)}` : ''}. Changes since the last run, rows deleted there are removed here.`
          : 'Off. Set KERA_OWUI_SYNC_DSN on the server to import Open WebUI nightly. Runs of kera migrate from-openwebui are listed below.'
      }
      actions={
        <div className="flex items-center gap-2">
          <Btn variant="ghost" onClick={() => void q.refetch()} aria-label="Refresh">
            <RefreshCw size={14} />
          </Btn>
          {s?.enabled && (
            <Btn variant="primary" disabled={s.running} onClick={() => void runNow()} data-testid="sync-run">
              {s.running ? 'Running…' : 'Sync now'}
            </Btn>
          )}
        </div>
      }
    >
      <Loading q={q} />
      {s && s.runs.length === 0 && <p className="p-4 text-sm text-muted">No imports yet.</p>}
      {s && s.runs.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-sm" data-testid="sync-runs">
            <thead>
              <tr>
                <th className={thCls}>Started</th>
                <th className={thCls}>By</th>
                <th className={thCls}>Status</th>
                <th className={thCls}>New</th>
                <th className={thCls}>Updated or kept</th>
                <th className={thCls}>Skipped</th>
                <th className={thCls}>Removed</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {s.runs.map((r) => (
                <tr key={r.id}>
                  <td className={tdCls} title={r.since ? `Changes since ${new Date(r.since).toLocaleString()}` : 'Everything'}>
                    {when(r.started_at)}
                  </td>
                  <td className={tdCls}>{r.trigger || 'cli'}</td>
                  <td className={tdCls}>
                    {r.error ? (
                      <Badge tone="red" title={r.error}>
                        failed
                      </Badge>
                    ) : r.finished_at ? (
                      <Badge tone="green">ok {duration(r)}</Badge>
                    ) : (
                      <Badge tone="amber">running or stopped</Badge>
                    )}
                  </td>
                  <td className={tdCls}>{r.totals.inserted}</td>
                  <td className={tdCls}>{r.totals.existing}</td>
                  <td className={tdCls}>{r.totals.skipped}</td>
                  <td className={tdCls}>{r.totals.pruned}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {s?.runs.find((r) => r.error) && <p className="border-t border-line p-3 text-xs text-muted">Hover a failed run for its error.</p>}
    </Card>
  )
}
