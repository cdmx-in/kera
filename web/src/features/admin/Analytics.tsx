import { useState } from 'react'
import { cx } from '@/lib/format'
import { useAdmin } from './api'
import { compact, lastDays, num, pageSlice } from './lib'
import { Pagination, useUrlPager } from './Pagination'
import { UserAvatar } from './Users'
import { Bar, Card, DateRange, DayBars, Kpi, Loading, Page, scrollCls, tdCls, theadCls, thCls } from './ui'

interface Summary {
  users_total: number
  active_users: number
  messages: number
  chats: number
  models: number
  prompt_tokens: number
  completion_tokens: number
}
interface DayRow {
  day: string
  messages: number
  users: number
  prompt_tokens: number
  completion_tokens: number
}
interface ModelRow {
  model: string
  messages: number
  users: number
  chats: number
  prompt_tokens: number
  completion_tokens: number
}
interface UserRow {
  user_id: string
  name: string
  email: string
  messages: number
  chats: number
  prompt_tokens: number
  completion_tokens: number
  last_at: number
}

export function AnalyticsPage() {
  const [range, setRange] = useState(() => lastDays(30))
  const qs = `?start_date=${range[0]}&end_date=${range[1]}`
  const summary = useAdmin<Summary>(['analytics', 'summary', ...range], '/api/v1/analytics/summary' + qs)
  const daily = useAdmin<DayRow[]>(['analytics', 'daily', ...range], '/api/v1/analytics/daily' + qs)
  const models = useAdmin<ModelRow[]>(['analytics', 'models', ...range], '/api/v1/analytics/models' + qs)
  const users = useAdmin<UserRow[]>(['analytics', 'users', ...range], '/api/v1/analytics/users' + qs)
  const s = summary.data
  const modelPager = useUrlPager('models')
  const userPager = useUrlPager()
  const maxModel = Math.max(0, ...(models.data ?? []).map((m) => m.messages))
  const maxUser = Math.max(0, ...(users.data ?? []).map((u) => u.messages))

  return (
    <Page title="Analytics" actions={<DateRange value={range} onChange={setRange} />}>
      <p className="-mt-3 text-xs text-muted">Assistant replies (imported history included) and token usage. Days are UTC.</p>
      <Loading q={summary} />
      {s && (
        <div className="grid grid-cols-2 gap-3 md:grid-cols-5">
          <Kpi label="Replies" value={num(s.messages)} />
          <Kpi label="Active users" value={num(s.active_users)} hint={`of ${num(s.users_total)} users`} />
          <Kpi label="Chats" value={num(s.chats)} />
          <Kpi label="Models used" value={num(s.models)} />
          <Kpi label="Tokens in / out" value={`${compact(s.prompt_tokens)} / ${compact(s.completion_tokens)}`} />
        </div>
      )}
      <section className="space-y-2">
        <h3 className="text-sm font-semibold">Replies per day</h3>
        <DayBars days={(daily.data ?? []).map((d) => ({ day: d.day, value: d.messages }))} format={(v) => `${num(v)} replies`} />
      </section>
      <Card title="Per model">
        <Loading q={models} />
        <div className={scrollCls}>
          <table className="w-full text-sm" data-testid="analytics-models">
            <thead className={theadCls}>
              <tr>
                <th className={thCls}>Model</th>
                <th className={cx(thCls, 'text-right')}>Replies</th>
                <th className={thCls} />
                <th className={cx(thCls, 'text-right')}>Users</th>
                <th className={cx(thCls, 'text-right')}>Chats</th>
                <th className={cx(thCls, 'text-right')}>Tokens in</th>
                <th className={cx(thCls, 'text-right')}>Tokens out</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line tabular-nums">
              {pageSlice(models.data ?? [], modelPager.page, modelPager.size).map((m) => (
                <tr key={m.model}>
                  <td className={cx(tdCls, 'font-medium')}>{m.model || '(none)'}</td>
                  <td className={cx(tdCls, 'text-right')}>{num(m.messages)}</td>
                  <td className={tdCls}>
                    <Bar value={m.messages} max={maxModel} />
                  </td>
                  <td className={cx(tdCls, 'text-right')}>{num(m.users)}</td>
                  <td className={cx(tdCls, 'text-right')}>{num(m.chats)}</td>
                  <td className={cx(tdCls, 'text-right')}>{num(m.prompt_tokens)}</td>
                  <td className={cx(tdCls, 'text-right')}>{num(m.completion_tokens)}</td>
                </tr>
              ))}
              {models.data && !models.data.length && <EmptyRow cols={7} />}
            </tbody>
          </table>
        </div>
        {!!models.data?.length && <Pagination pager={modelPager} total={models.data.length} label="Models pages" />}
      </Card>
      <Card title="Per user">
        <Loading q={users} />
        <div className={scrollCls}>
          <table className="w-full text-sm" data-testid="analytics-users">
            <thead className={theadCls}>
              <tr>
                <th className={thCls}>User</th>
                <th className={cx(thCls, 'text-right')}>Replies</th>
                <th className={thCls} />
                <th className={cx(thCls, 'text-right')}>Chats</th>
                <th className={cx(thCls, 'text-right')}>Tokens in</th>
                <th className={cx(thCls, 'text-right')}>Tokens out</th>
                <th className={thCls}>Last reply</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line tabular-nums">
              {pageSlice(users.data ?? [], userPager.page, userPager.size).map((u) => (
                <tr key={u.user_id}>
                  <td className={tdCls}>
                    <div className="flex min-w-48 items-center gap-2">
                      <UserAvatar id={u.user_id} />
                      <div className="min-w-0">
                        <div className="truncate font-medium">{u.name || u.user_id}</div>
                        <div className="truncate text-xs text-muted">{u.email}</div>
                      </div>
                    </div>
                  </td>
                  <td className={cx(tdCls, 'text-right')}>{num(u.messages)}</td>
                  <td className={tdCls}>
                    <Bar value={u.messages} max={maxUser} />
                  </td>
                  <td className={cx(tdCls, 'text-right')}>{num(u.chats)}</td>
                  <td className={cx(tdCls, 'text-right')}>{num(u.prompt_tokens)}</td>
                  <td className={cx(tdCls, 'text-right')}>{num(u.completion_tokens)}</td>
                  <td className={cx(tdCls, 'whitespace-nowrap text-muted')}>{new Date(u.last_at * 1000).toLocaleDateString()}</td>
                </tr>
              ))}
              {users.data && !users.data.length && <EmptyRow cols={7} />}
            </tbody>
          </table>
        </div>
        {!!users.data?.length && <Pagination pager={userPager} total={users.data.length} label="Users pages" />}
      </Card>
      <FeedbackCard />
    </Page>
  )
}

interface FeedbackRow {
  id: string
  user_name: string
  user_email: string
  model_id: string
  rating: number
  comment: string
  source: string
  voter: string
  created_at: number
}

/** The newest 200 thumbs votes (web and Google Chat), not tied to the date range. */
function FeedbackCard() {
  const q = useAdmin<FeedbackRow[]>(['feedback'], '/api/v1/evaluations/feedbacks/all')
  const pager = useUrlPager('feedback')
  const rows = q.data ?? []
  const down = rows.filter((r) => r.rating < 0).length
  return (
    <Card title={`Recent feedback${rows.length ? ` · ${rows.length - down} up, ${down} down` : ''}`}>
      <Loading q={q} />
      <div className={scrollCls}>
        <table className="w-full text-sm" data-testid="analytics-feedback">
          <thead className={theadCls}>
            <tr>
              <th className={thCls}>Vote</th>
              <th className={thCls}>Model</th>
              <th className={thCls}>Comment</th>
              <th className={thCls}>By</th>
              <th className={thCls}>When</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {pageSlice(rows, pager.page, pager.size).map((r) => (
              <tr key={r.id}>
                <td className={tdCls} aria-label={r.rating > 0 ? 'Good' : 'Bad'}>
                  {r.rating > 0 ? '👍' : '👎'}
                </td>
                <td className={cx(tdCls, 'font-medium')}>{r.model_id || '(none)'}</td>
                <td className={cx(tdCls, 'max-w-md')}>{r.comment || <span className="text-muted">-</span>}</td>
                <td className={cx(tdCls, 'whitespace-nowrap')}>
                  {r.voter || r.user_email || r.user_name}
                  {r.source !== 'web' && <span className="ml-1 text-xs text-muted">({r.source})</span>}
                </td>
                <td className={cx(tdCls, 'whitespace-nowrap text-muted')}>{new Date(r.created_at * 1000).toLocaleString()}</td>
              </tr>
            ))}
            {q.data && !rows.length && (
              <tr>
                <td colSpan={5} className="p-6 text-center text-muted">
                  No votes yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {!!rows.length && <Pagination pager={pager} total={rows.length} label="Feedback pages" />}
    </Card>
  )
}

export function EmptyRow({ cols }: { cols: number }) {
  return (
    <tr>
      <td colSpan={cols} className="p-6 text-center text-muted">
        No data in this range.
      </td>
    </tr>
  )
}
