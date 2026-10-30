import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { useDocumentTitle } from '../lib/useDocumentTitle'

interface Coverage {
  cost: number; input: number; output: number; cache_read: number; cache_write: number; tokens: number
}
export interface UsageAggregate {
  runs: number
  total_cost_usd: number | null
  total_tokens: number | null
  input_tokens: number | null
  output_tokens: number | null
  cache_read_input_tokens: number | null
  cache_creation_input_tokens: number | null
  subscription_runs?: number
  estimated_cost_runs: number
  reported_cost_runs: number
  cost_provenance_unknown_runs: number
  coverage: Coverage
}
interface Breakdown {
  totals: UsageAggregate
  by_project: Record<string, UsageAggregate>
  by_repo: Record<string, UsageAggregate>
  by_role: Record<string, UsageAggregate>
  by_profile: Record<string, UsageAggregate>
  by_model: Record<string, UsageAggregate>
  by_machine: Record<string, UsageAggregate>
  by_operator: Record<string, UsageAggregate>
}
interface Period extends Breakdown {
  period_start: string
  period_end: string
  daily_trend: (UsageAggregate & { date: string })[]
}
export interface UsageSummary extends Breakdown {
  generated_at: string
  range_start: string | null
  range_end: string | null
  periods: Period[]
  history_limit: number
  history: { started_at: string; repo: string; project: string; operator: string; status: string; href?: string }[]
}
const dimensions = [
  ['project', '项目', 'by_project'], ['repo', '仓库', 'by_repo'], ['role', '角色', 'by_role'],
  ['profile', '配置', 'by_profile'], ['model', '模型', 'by_model'], ['machine', '机器', 'by_machine'],
] as const
export type Filters = Partial<Record<typeof dimensions[number][0], string>>
export const Route = createFileRoute('/_app/usage')({ component: UsagePage })

export function fmtCost(usd: number | null, subscriptions = 0): string {
  if (usd == null) return subscriptions > 0 ? 'Subscription' : 'Not reported'
  return `$${usd.toFixed(4)}${subscriptions > 0 ? ' + subscription' : ''}`
}
function number(n: number | null | undefined) { return n == null ? '未报告' : n.toLocaleString() }
export function coverage(reported: number | undefined, runs: number) {
  if (reported == null) return '覆盖率未报告'
  return `${reported}/${runs} 次报告 · ${runs - reported} 次缺失`
}
export function usageQuery(filters: Filters, period: string, day: string) {
  const q = new URLSearchParams()
  for (const [key, value] of Object.entries(filters)) if (value) q.set(key, value)
  if (day) {
    q.set('from', day)
    q.set('until', new Date(Date.parse(day + 'T00:00:00Z') + 86400000).toISOString())
  } else q.set('period', period)
  return q.toString()
}
function useInitialSelection() {
  const q = new URLSearchParams(window.location.search)
  const filters: Filters = {}
  for (const [key] of dimensions) if (q.get(key)) filters[key] = q.get(key)!
  const period = q.get('period') || 'current'
  const rawDay = q.get('day') || ''
  const day = /^\d{4}-\d{2}-\d{2}$/.test(rawDay) && Number.isFinite(Date.parse(rawDay + 'T00:00:00Z')) ? rawDay : ''
  return { filters, period, day }
}
function UsagePage() {
  useDocumentTitle('Usage')
  const [initial] = useState(useInitialSelection)
  const [filters, setFilters] = useState<Filters>(initial.filters)
  const [period, setPeriod] = useState(initial.period)
  const [day, setDay] = useState(initial.day)
  const [catalog, setCatalog] = useState<UsageSummary | null>(null)
  const [summary, setSummary] = useState<UsageSummary | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const query = usageQuery(filters, period, day)
  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const r = await fetch('/api/usage?period=all', { cache: 'no-store' })
        if (!r.ok) throw new Error('读取失败')
        const value = await r.json()
        if (!cancelled) setCatalog(value)
      } catch { /* The scoped request below owns the visible error state. */ }
    }
    void load()
    const timer = setInterval(load, 30000)
    return () => { cancelled = true; clearInterval(timer) }
  }, [])
  useEffect(() => {
    let cancelled = false
    const url = new URL(window.location.href)
    for (const [key] of dimensions) url.searchParams.delete(key)
    for (const [key, value] of Object.entries(filters)) if (value) url.searchParams.set(key, value)
    url.searchParams.set('period', period)
    if (day) url.searchParams.set('day', day); else url.searchParams.delete('day')
    window.history.replaceState(window.history.state, '', url)
    setLoading(true)
    setSummary(null)
    const load = async () => {
      try {
        const r = await fetch(`/api/usage?${query}`, { cache: 'no-store' })
        if (!r.ok) throw new Error('用量暂时不可用，请稍后重试。')
        const value = await r.json()
        if (!cancelled) { setSummary(value); setError('') }
      } catch { if (!cancelled) { setSummary(null); setError('用量暂时不可用，请稍后重试。') } }
      finally { if (!cancelled) setLoading(false) }
    }
    void load()
    const timer = setInterval(load, 30000)
    return () => { cancelled = true; clearInterval(timer) }
  }, [query, filters, period, day])
  return <div className="w-full px-4 sm:px-6 py-6 space-y-5">
    <header className="space-y-2">
      <h1 className="text-2xl font-semibold">Usage &amp; billing</h1>
      <p className="text-sm text-muted-foreground">仅统计 ClawFlow 已结束尝试的已存用量（包含失败和取消）。不包含个人 Offload 全局任务；重试按本次用量分别记录。</p>
    </header>
    <section aria-label="用量筛选" className="bg-card border rounded-xl p-4 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
      <label className="text-sm">计费周期（UTC）
        <select aria-label="计费周期" className="block w-full bg-secondary border rounded p-2 mt-1" value={period} onChange={e => { setPeriod(e.target.value); setDay('') }}>
          <option value="current">当前计费周期</option><option value="all">全部时间</option>
          {(catalog?.periods || []).map(p => <option key={p.period_start} value={p.period_start}>{p.period_start} – {p.period_end}</option>)}
        </select>
      </label>
      {dimensions.map(([key, label, group]) => <label key={key} className="text-sm min-w-0">{label}
        <select aria-label={label} className="block w-full min-w-0 bg-secondary border rounded p-2 mt-1" value={filters[key] || ''} onChange={e => setFilters({ ...filters, [key]: e.target.value })}>
          <option value="">全部{label}</option>
          {Array.from(new Set([...(Object.keys(catalog?.[group] || {})), ...(filters[key] ? [filters[key]!] : [])])).sort().map(name => <option key={name} value={name}>{name === 'unknown' ? '未归属 / 未报告' : name}</option>)}
        </select>
      </label>)}
      <button className="border rounded p-2 self-end" onClick={() => { setFilters({}); setPeriod('current'); setDay('') }}>重置筛选</button>
    </section>
    {day && <p>正在查看 {day}（UTC） <button className="underline" onClick={() => setDay('')}>返回整个周期</button></p>}
    {filters.model && <p className="text-sm text-muted-foreground">模型筛选选择包含该模型的完整尝试；总量包含这些尝试中其他模型的用量。模型分组显示各自已存用量。</p>}
    {loading ? <p role="status">正在读取用量…</p> : error ? <p role="alert">{error}</p> : summary && <UsageReport summary={summary} onFilter={(key, value) => setFilters({ ...filters, [key]: value })} onDay={setDay} />}
  </div>
}
function Section({ title, children }: { title: string; children: ReactNode }) {
  return <section className="bg-card border rounded-xl p-4 min-w-0 space-y-3"><h2 className="text-lg font-semibold">{title}</h2>{children}</section>
}
function Metric({ label, value, count, runs }: { label: string; value: string; count: number | undefined; runs: number }) {
  return <div className="border rounded-lg p-3 min-w-0"><h3 className="text-sm text-muted-foreground">{label}</h3><p className="text-xl font-semibold break-words">{value}</p><p className="text-xs text-muted-foreground">{coverage(count, runs)}</p></div>
}
export function UsageReport({ summary: s, onFilter, onDay }: { summary: UsageSummary; onFilter: (key: keyof Filters, value: string) => void; onDay: (day: string) => void }) {
  const a = s.totals
  const trend = s.periods.flatMap(p => p.daily_trend).filter(d => {
    const start = Date.parse(d.date + 'T00:00:00Z')
    return (!s.range_end || start < Date.parse(s.range_end)) && (!s.range_start || start + 86400000 > Date.parse(s.range_start))
  }).sort((a, b) => a.date.localeCompare(b.date))
  const max = Math.max(0.0001, ...trend.map(d => d.total_cost_usd ?? 0))
  return <div className="space-y-5">
    <div className="flex flex-wrap gap-3 justify-between text-sm">
      <p>时间范围（UTC，结束不含）：{s.range_start || '无数据'} → {s.range_end || '无数据'}</p>
      <a className="underline" href="#run-history">查看筛选后的运行历史</a>
    </div>
    <p className="text-2xl font-semibold">{a.runs} 次已结束尝试</p>
    {a.runs === 0 && <p>此范围没有已结束尝试；缺失用量不会被当作零。</p>}
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-3">
      <Metric label="已报告费用（USD，可能含估算）" value={fmtCost(a.total_cost_usd, a.subscription_runs)} count={a.coverage?.cost} runs={a.runs} />
      <Metric label="完整 token 总量（含缓存输入和输出）" value={number(a.total_tokens)} count={a.coverage?.tokens} runs={a.runs} />
      <Metric label="非缓存输入" value={number(a.input_tokens)} count={a.coverage?.input} runs={a.runs} />
      <Metric label="输出" value={number(a.output_tokens)} count={a.coverage?.output} runs={a.runs} />
      <Metric label="缓存读取输入" value={number(a.cache_read_input_tokens)} count={a.coverage?.cache_read} runs={a.runs} />
      <Metric label="缓存写入输入" value={number(a.cache_creation_input_tokens)} count={a.coverage?.cache_write} runs={a.runs} />
    </div>
    <p className="text-sm text-muted-foreground">{provenance(a)}。Subscription 表示订阅计费，不代表免费；Not reported 表示未报告。费用为运行记录中的金额，不是账单。缓存读取/写入是总输入的子集；完整总量只累加组件齐全的尝试，部分报告保留在各指标中。</p>
    {trend.length > 0 && <Section title="每日已报告费用">
      <p className="text-sm text-muted-foreground">仅绘制已报告金额；部分覆盖不是全天费用。虚线表示未报告，订阅不折算为美元。选择日期查看当天。</p>
      <div className="overflow-x-auto"><div className="flex items-end gap-2 min-w-max py-2">
        {trend.map(d => <button key={d.date} onClick={() => onDay(d.date)} className="w-28 shrink-0 text-xs text-left rounded border p-2 focus-visible:ring-2" aria-label={`${d.date} ${fmtCost(d.total_cost_usd, d.subscription_runs)} ${coverage(d.coverage?.cost, d.runs)}`}>
          <span className="block h-20 relative border-b" aria-hidden="true"><span className={`absolute bottom-0 w-full rounded-t ${d.total_cost_usd == null ? 'border border-dashed' : 'bg-brand/60'}`} style={{ height: d.total_cost_usd == null ? 8 : Math.max(2, d.total_cost_usd / max * 80) }} /></span>
          <span className="block mt-2">{d.date}</span><span className="block break-words">{fmtCost(d.total_cost_usd, d.subscription_runs)}</span><span className="block text-muted-foreground">{coverage(d.coverage?.cost, d.runs)}</span>
        </button>)}
      </div></div>
    </Section>}
    {dimensions.map(([key, label, group]) => <Section key={key} title={`按${label}`}>
      {key === 'model' && <p className="text-sm text-muted-foreground">一次尝试可使用多个模型；模型的尝试次数不可相加。</p>}
      <div className="overflow-x-auto"><table className="w-full text-sm text-left"><thead><tr><th className="p-2">{label}</th><th className="p-2">尝试次数</th><th className="p-2">已报告费用</th><th className="p-2">完整 token 总量</th><th className="p-2">费用来源</th></tr></thead>
        <tbody>{Object.entries(s[group] || {}).sort(([a], [b]) => a.localeCompare(b)).map(([name, row]) => <tr key={name} className="border-t align-top">
          <td className="p-2 break-all max-w-64"><button className="underline text-left" onClick={() => onFilter(key, name)}>{name === 'unknown' ? '未归属 / 未报告' : name}</button></td>
          <td className="p-2">{row.runs}</td><td className="p-2">{fmtCost(row.total_cost_usd, row.subscription_runs)}<small className="block text-muted-foreground">{coverage(row.coverage?.cost, row.runs)}</small></td>
          <td className="p-2">{number(row.total_tokens)}<small className="block text-muted-foreground">{coverage(row.coverage?.tokens, row.runs)}</small></td><td className="p-2 min-w-40">{provenance(row)}</td>
        </tr>)}</tbody>
      </table></div>
    </Section>)}
    <section id="run-history" className="bg-card border rounded-xl p-4 space-y-3 scroll-mt-20">
      <h2 className="text-lg font-semibold">筛选后的运行历史</h2>
      <p className="text-sm text-muted-foreground">与上方时间和全部筛选一致，最近 {s.history?.length || 0}/{a.runs} 次（最多 {s.history_limit} 次）；汇总包含全部匹配尝试。详情链接复用现有记录页。</p>
      <ul className="space-y-3">{(s.history || []).map((r, i) => <li key={i} className="border-t pt-2 text-sm break-words">
        <span>{r.started_at} · {r.repo || r.project || '未归属'} · {r.operator} · {r.status} </span>
        {r.href ? <a className="underline" href={r.href}>{r.repo ? '运行详情' : '项目 Steward 历史'}</a> : <span className="text-muted-foreground">旧记录无可用详情链接</span>}
      </li>)}</ul>
    </section>
    <p className="text-xs text-muted-foreground">数据生成时间：{s.generated_at}</p>
  </div>
}
function provenance(a: UsageAggregate) {
  return `${a.estimated_cost_runs ?? 0} 次明确估算 · ${a.reported_cost_runs ?? 0} 次报告金额 · ${a.cost_provenance_unknown_runs ?? 0} 次估算性质未报告 · ${a.subscription_runs ?? 0} 次订阅`
}
