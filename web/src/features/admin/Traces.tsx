// The run trace: recent traced runs, and one run as a waterfall of model rounds, tool calls, retrieval and compaction.
import { useState } from 'react'
import { Link } from 'react-router'
import { ArrowLeft, Brain, Database, Minimize2, Wrench } from 'lucide-react'
import { cx } from '@/lib/format'
import { enc, useAdmin } from './api'
import { Badge, Card, inputAuto, Kpi, Loading, Page, scrollCls, tdCls, theadCls, thCls } from './ui'
import { ago, compact, num } from './lib'
import { axisLabel, axisTicks } from './runview'
import { rowCols, spine, StepDot } from './StepWaterfall'
import { bar, cachedShare, ms, stepChips, type StepKind, type TraceDetail, type TraceRun, type TraceStep } from './trace'
import type { StepStatus } from './pipelines'

export function TracesPage({ message }: { message?: string }) {
  return message ? <TraceView message={message} /> : <TraceList />
}

function TraceList() {
  const [days, setDays] = useState(7)
  const runs = useAdmin<TraceRun[]>(['traces', String(days)], `/api/v1/admin/runs?days=${days}&limit=200`)
  const list = runs.data ?? []
  return (
    <Page
      title="Traces"
      actions={
        <select aria-label="Period" className={inputAuto} value={days} onChange={(e) => setDays(Number(e.target.value))}>
          <option value={1}>Last day</option>
          <option value={7}>Last 7 days</option>
          <option value={30}>Last 30 days</option>
        </select>
      }
    >
      <p className="-mt-3 text-xs text-muted">Each model round and tool call of a chat or task run: sizes, token counts and timings. No prompt, answer or tool text is stored. Kept as long as the retention setting says (Settings &gt; Chat).</p>
      <Loading q={runs} />
      {runs.data && (
        <Card>
          <div className={scrollCls}>
            <table className="w-full text-sm" data-testid="trace-runs">
              <thead className={theadCls}>
                <tr>
                  <th className={thCls}>Started</th>
                  <th className={thCls}>User</th>
                  <th className={thCls}>Model</th>
                  <th className={cx(thCls, 'text-right')}>Rounds</th>
                  <th className={cx(thCls, 'text-right')}>Tool calls</th>
                  <th className={cx(thCls, 'text-right')}>Tokens in / out</th>
                  <th className={cx(thCls, 'text-right')}>Cached</th>
                  <th className={cx(thCls, 'text-right')}>Time</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {list.map((r) => (
                  <tr key={r.run_id} className="hover:bg-surface-2/50">
                    <td className={tdCls}>
                      <Link className="font-medium text-accent hover:underline dark:text-accent-bright" to={`/admin/traces/${enc(r.message_id)}`}>
                        {ago(Math.floor(new Date(r.started_at).getTime() / 1000))}
                      </Link>
                    </td>
                    <td className={tdCls}>{r.user_name || r.user_email || r.user_id}</td>
                    <td className={tdCls}>{r.model_id}</td>
                    <td className={cx(tdCls, 'text-right tabular-nums')}>{r.rounds}</td>
                    <td className={cx(tdCls, 'text-right tabular-nums')}>
                      {r.tool_calls}
                      {r.tool_errors > 0 && (
                        <>
                          {' '}
                          <Badge tone="red">{r.tool_errors} failed</Badge>
                        </>
                      )}
                    </td>
                    <td className={cx(tdCls, 'text-right tabular-nums')}>
                      {compact(r.prompt_tokens)} / {compact(r.completion_tokens)}
                    </td>
                    <td className={cx(tdCls, 'text-right tabular-nums')}>{cachedShare(r.prompt_tokens, r.cached_tokens)}</td>
                    <td className={cx(tdCls, 'text-right tabular-nums')}>
                      {ms(r.duration_ms)} {r.failed && <Badge tone="red">error</Badge>}
                    </td>
                  </tr>
                ))}
                {list.length === 0 && (
                  <tr>
                    <td colSpan={8} className="px-3 py-8 text-center text-muted">
                      No traced runs in this period.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </Page>
  )
}

const icons: Record<StepKind, typeof Brain> = { model: Brain, tool: Wrench, retrieval: Database, compaction: Minimize2 }
const kindLabel: Record<StepKind, string> = { model: 'Model', tool: 'Tool', retrieval: 'Retrieval', compaction: 'Compaction' }

function statusOf(s: TraceStep): StepStatus {
  if (s.error || s.detail?.is_error === true || s.detail?.known === false) return 'failed'
  if (s.detail?.truncated === true || (s.kind === 'retrieval' && s.detail?.hits === 0)) return 'warning'
  return 'ok'
}

const barLook: Record<StepStatus, string> = { pending: '', skipped: '', ok: 'bg-ok/70 group-hover/row:bg-ok', warning: 'bg-warn', failed: 'bg-bad', running: 'bg-accent' }
const tone = { red: 'bg-bad-soft text-bad-ink', amber: 'bg-warn-soft text-warn-ink', green: 'bg-ok-soft text-ok-ink', accent: 'bg-accent-soft text-accent-ink', grey: 'bg-surface-2 text-muted' }

function TraceView({ message }: { message: string }) {
  const q = useAdmin<TraceDetail>(['trace', message], `/api/v1/admin/runs/${enc(message)}/steps`)
  const d = q.data
  return (
    <Page
      title="Trace"
      actions={
        <Link to="/admin/traces" className="inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
          <ArrowLeft size={14} aria-hidden /> All traces
        </Link>
      }
    >
      <Loading q={q} />
      {d && <TraceBody d={d} />}
    </Page>
  )
}

export function TraceBody({ d }: { d: TraceDetail }) {
  const t0 = new Date(d.started_at).getTime()
  const span = Math.max(1000, d.duration_ms)
  const grid = axisTicks(span)
  const rounds = d.steps.filter((s) => s.kind === 'model')
  const sum = (f: (s: TraceStep) => number | null) => rounds.reduce((a, s) => a + (f(s) ?? 0), 0)
  const prompt = sum((s) => s.prompt_tokens)
  const cached = sum((s) => s.cached_tokens)
  const tools = d.steps.filter((s) => s.kind === 'tool')
  const [open, setOpen] = useState<number | null>(null)
  return (
    <>
      <p className="-mt-3 text-xs text-muted">
        {d.model_id} for {d.user_name || d.user_email}, {new Date(d.started_at).toLocaleString()}
      </p>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-5">
        <Kpi label="Run time" value={ms(d.duration_ms)} />
        <Kpi label="Model rounds" value={rounds.length} />
        <Kpi label="Tool calls" value={tools.length} hint={tools.some((s) => s.detail?.is_error === true) ? `${tools.filter((s) => s.detail?.is_error === true).length} failed` : undefined} />
        <Kpi label="Tokens in / out" value={`${compact(prompt)} / ${compact(sum((s) => s.completion_tokens))}`} />
        <Kpi label="Prompt cached" value={cachedShare(prompt, cached)} hint={cached === 0 ? 'the server reports none' : undefined} />
      </div>
      <section aria-label="Steps" className="@container overflow-hidden rounded-2xl border border-line bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)]" data-testid="trace-steps">
        <div className="px-3 pb-1 pt-3" aria-hidden>
          <div className="ml-8 @xl:ml-0 @xl:grid @xl:grid-cols-[2rem_minmax(0,2fr)_minmax(0,3fr)_7.5rem]">
            <div className="relative h-4 @xl:col-start-3">
              {grid.marks.map((m) => (
                <span key={m.ms} className={cx('absolute top-0 whitespace-nowrap text-[10px] tabular-nums text-muted', m.pct > 88 ? '-translate-x-full' : m.pct > 0 ? '-translate-x-1/2' : '')} style={{ left: `${m.pct}%` }}>
                  {axisLabel(m.ms)}
                </span>
              ))}
            </div>
          </div>
        </div>
        <ol className="divide-y divide-line/70 border-t border-line">
          {d.steps.map((s, i) => {
            const status = statusOf(s)
            const b = bar(s, t0, span)
            const Icon = icons[s.kind]
            const isOpen = open === s.seq
            return (
              <li key={s.seq} data-testid="trace-step" data-kind={s.kind} data-status={status} className="group/row relative">
                <span aria-hidden className={spine(i, d.steps.length)} />
                <StepDot status={status} />
                <button
                  type="button"
                  aria-expanded={isOpen}
                  onClick={() => setOpen(isOpen ? null : s.seq)}
                  className={cx(rowCols, 'w-full items-start gap-y-1 px-3 py-2.5 text-left outline-none hover:bg-surface-2/50 focus-visible:bg-surface-2/50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent')}
                >
                  <span aria-hidden />
                  <span className="min-w-0">
                    <span className="flex min-w-0 items-center gap-2 text-sm font-semibold">
                      <Icon size={13} aria-hidden className="shrink-0 text-muted" />
                      <span className="truncate">{s.kind === 'model' ? `Round ${s.round + 1}` : s.name}</span>
                      <span className="text-xs font-normal text-muted">{kindLabel[s.kind]}{s.kind === 'model' && s.name ? `, ${s.name}` : ''}</span>
                    </span>
                    <span className="mt-1 flex flex-wrap gap-1">
                      {stepChips(s).map((c) => (
                        <span key={c.text} className={cx('rounded px-1.5 py-px text-[11px] font-medium tabular-nums', tone[c.tone])}>
                          {c.text}
                        </span>
                      ))}
                    </span>
                  </span>
                  <span aria-hidden className="relative col-span-2 col-start-2 row-start-2 block h-5 @xl:col-span-1 @xl:col-start-3 @xl:row-start-1 @xl:-my-2.5 @xl:h-auto @xl:self-stretch" style={{ backgroundImage: 'linear-gradient(to right, var(--color-line) 1px, transparent 1px)', backgroundSize: `${(grid.step / span) * 100}% 100%`, backgroundRepeat: 'repeat-x' }}>
                    <span className={cx('absolute top-1/2 h-2.5 -translate-y-1/2 rounded-[3px]', barLook[status])} style={{ left: `${b.left}%`, width: `${b.width}%` }} />
                  </span>
                  <span className="col-start-3 row-start-1 text-right text-sm font-medium tabular-nums @xl:col-start-4">{ms(s.duration_ms)}</span>
                </button>
                {isOpen && (
                  <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 pb-3 pl-12 pr-3 text-xs" aria-label="Step facts">
                    {Object.entries({ ...s.detail, ...(s.finish ? { finish: s.finish } : {}), ...(s.prompt_tokens !== null ? { prompt_tokens: s.prompt_tokens } : {}), ...(s.cached_tokens !== null ? { cached_tokens: s.cached_tokens } : {}), ...(s.completion_tokens !== null ? { completion_tokens: s.completion_tokens } : {}) }).map(([k, v]) => (
                      <div key={k} className="contents">
                        <dt className="text-muted">{k.replace(/_/g, ' ')}</dt>
                        <dd className="tabular-nums">{typeof v === 'number' ? num(v) : Array.isArray(v) ? v.join(', ') : String(v)}</dd>
                      </div>
                    ))}
                  </dl>
                )}
              </li>
            )
          })}
        </ol>
      </section>
    </>
  )
}
