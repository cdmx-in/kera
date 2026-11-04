// Spend page (ported from the old standalone page): LiteLLM spend per user, day and model.
import { useMemo, useState } from 'react'
import { Search } from 'lucide-react'
import { cx } from '@/lib/format'
import { useAdmin } from './api'
import { EmptyRow } from './Analytics'
import { ago, monthRange, num, pageSlice, sortPeople, spendLevel, spendView, usd, type SortKey, type SpendData } from './lib'
import { Pagination, useUrlPager } from './Pagination'
import { UserAvatar } from './Users'
import { Badge, Bar, Card, DateRange, DayBars, inputCls, Kpi, Loading, Page, scrollCls, tdCls, theadCls, thCls } from './ui'

const levels = { high: ['red', 'High'], medium: ['amber', 'Medium'], light: ['green', 'Light'], none: ['grey', 'No usage'] } as const

export function SpendPage() {
  const [range, setRange] = useState(monthRange)
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: 'spend', desc: true })
  const userPager = useUrlPager()
  const modelPager = useUrlPager('models')
  const data = useAdmin<SpendData>(['spend', ...range], `/api/v1/analytics/spend?start_date=${range[0]}&end_date=${range[1]}`)
  const view = useMemo(() => (data.data ? spendView(data.data) : null), [data.data])
  const people = useMemo(() => {
    if (!view) return []
    const needle = q.trim().toLowerCase()
    return sortPeople(needle ? view.people.filter((p) => `${p.name} ${p.email}`.toLowerCase().includes(needle)) : view.people, sort.key, sort.desc)
  }, [view, q, sort])
  const maxSpend = Math.max(0, ...(view?.people ?? []).map((p) => p.spend))
  const head = (key: SortKey, label: string, right = false) => (
    <th className={cx(thCls, right && 'text-right')} aria-sort={sort.key === key ? (sort.desc ? 'descending' : 'ascending') : undefined}>
      <button type="button" className="hover:text-fg" onClick={() => (setSort({ key, desc: key === sort.key ? !sort.desc : key !== 'name' }), userPager.setPage(1, true))}>
        {label}
        {sort.key === key ? (sort.desc ? ' ▾' : ' ▴') : ''}
      </button>
    </th>
  )

  return (
    <Page title="Spend" actions={<DateRange value={range} onChange={setRange} />}>
      <p className="-mt-3 text-xs text-muted">From the LiteLLM spend logs · days are UTC</p>
      <Loading q={data} />
      {view && (
        <>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <Kpi label="Total spend (USD)" value={usd(view.total)} />
            <Kpi label="Total users" value={num(view.users)} />
            <Kpi label="Active users (spend > 0)" value={num(view.active)} />
            <Kpi label="No usage" value={num(view.idle)} />
          </div>
          <section className="space-y-2">
            <h3 className="text-sm font-semibold">Per day</h3>
            <DayBars days={view.days.map((d) => ({ day: d.day, value: d.spend }))} format={usd} />
          </section>
          <Card
            title="Per user"
            actions={
              <label className="relative w-full sm:w-64">
                <Search size={15} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
                <input type="search" value={q} onChange={(e) => (setQ(e.target.value), userPager.setPage(1, true))} placeholder="Search name or email" aria-label="Search spend users" className={cx(inputCls, 'pl-8')} />
              </label>
            }
          >
            <div className={scrollCls}>
              <table className="w-full text-sm" data-testid="spend-users">
                <thead className={theadCls}>
                  <tr>
                    {head('name', 'User')}
                    {head('last', 'Last active')}
                    {head('requests', 'Requests', true)}
                    {head('tokens', 'Tokens', true)}
                    {head('spend', 'Spend', true)}
                    <th className={thCls} />
                    <th className={thCls}>Status</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line tabular-nums">
                  {pageSlice(people, userPager.page, userPager.size).map((p) => {
                    const [tone, label] = levels[spendLevel(p.spend)]
                    const inactive = p.last != null && Date.now() / 1000 - p.last > 30 * 86400
                    return (
                      <tr key={p.id ?? `ext:${p.email}`}>
                        <td className={tdCls}>
                          <div className="flex min-w-48 items-center gap-2">
                            {p.id ? <UserAvatar id={p.id} /> : <span className="grid size-7 shrink-0 place-items-center rounded-full bg-accent-bright text-xs text-white">{(p.name || '?')[0].toUpperCase()}</span>}
                            <div className="min-w-0">
                              <div className="truncate font-medium">{p.name}</div>
                              <div className="truncate text-xs text-muted">{p.email}</div>
                            </div>
                          </div>
                        </td>
                        <td className={cx(tdCls, 'whitespace-nowrap')}>
                          {ago(p.last)} {inactive && <Badge>Inactive</Badge>}
                        </td>
                        <td className={cx(tdCls, 'text-right')}>{num(p.requests)}</td>
                        <td className={cx(tdCls, 'text-right')}>{num(p.tokens)}</td>
                        <td className={cx(tdCls, 'text-right')}>{usd(p.spend)}</td>
                        <td className={tdCls}>
                          <Bar value={p.spend} max={maxSpend} />
                        </td>
                        <td className={tdCls}>
                          <Badge tone={tone}>{label}</Badge>
                        </td>
                      </tr>
                    )
                  })}
                  {!people.length && <EmptyRow cols={7} />}
                </tbody>
              </table>
            </div>
            {!!people.length && <Pagination pager={userPager} total={people.length} label="Users pages" />}
          </Card>
          <Card title="Per model">
            <div className={scrollCls}>
              <table className="w-full text-sm" data-testid="spend-models">
                <thead className={theadCls}>
                  <tr>
                    <th className={thCls}>Model</th>
                    <th className={cx(thCls, 'text-right')}>Requests</th>
                    <th className={cx(thCls, 'text-right')}>Input tokens</th>
                    <th className={cx(thCls, 'text-right')}>Output tokens</th>
                    <th className={cx(thCls, 'text-right')}>Spend</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line tabular-nums">
                  {pageSlice(view.models, modelPager.page, modelPager.size).map((m) => (
                    <tr key={m.model}>
                      <td className={cx(tdCls, 'font-medium')}>{m.model || '(none)'}</td>
                      <td className={cx(tdCls, 'text-right')}>{num(m.requests)}</td>
                      <td className={cx(tdCls, 'text-right')}>{num(m.input)}</td>
                      <td className={cx(tdCls, 'text-right')}>{num(m.output)}</td>
                      <td className={cx(tdCls, 'text-right')}>{usd(m.spend)}</td>
                    </tr>
                  ))}
                  {!view.models.length && <EmptyRow cols={5} />}
                </tbody>
              </table>
            </div>
            {!!view.models.length && <Pagination pager={modelPager} total={view.models.length} label="Models pages" />}
          </Card>
        </>
      )}
    </Page>
  )
}
