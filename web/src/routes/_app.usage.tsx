import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { Receipt } from 'lucide-react'
import { type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'

interface UsageAggregate {
  runs: number
  total_cost_usd: number
  input_tokens: number
  output_tokens: number
  cache_read_input_tokens: number
  cache_creation_input_tokens: number
  duration_ms: number
}

interface ModelAggregate {
  cost_usd: number
  input_tokens: number
  output_tokens: number
  cache_read_input_tokens: number
  cache_creation_input_tokens: number
}

interface DailyPoint {
  date: string
  runs: number
  total_cost_usd: number
  input_tokens: number
  output_tokens: number
  by_operator?: Record<string, UsageAggregate>
  by_repo?: Record<string, UsageAggregate>
  by_model?: Record<string, ModelAggregate>
}

interface PeriodSummary {
  period_start: string
  period_end: string
  totals: UsageAggregate
  by_operator: Record<string, UsageAggregate>
  by_repo: Record<string, UsageAggregate>
  by_model: Record<string, ModelAggregate>
  daily_trend: DailyPoint[]
}

interface UsageSummary {
  generated_at: string
  totals: UsageAggregate
  by_operator: Record<string, UsageAggregate>
  by_repo: Record<string, UsageAggregate>
  by_model: Record<string, ModelAggregate>
  periods?: PeriodSummary[]
}

interface Repo {
  full_name: string
  platform?: Platform
  base_url?: string
  enabled: boolean
}

export const Route = createFileRoute('/_app/usage')({
  component: UsagePage,
})

function timeAgo(iso: string): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (!isFinite(t)) return '—'
  const diff = Math.floor((Date.now() - t) / 1000)
  if (diff < 0) return 'just now'
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}min ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

function durationStr(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h${m % 60}m`
}

function fmtCost(usd: number): string {
  return `$${usd.toFixed(4)}`
}

function fmtNum(n: number): string {
  return n.toLocaleString()
}

function fmtPeriodLabel(start: string, end: string): string {
  const s = new Date(start + 'T00:00:00')
  const e = new Date(end + 'T00:00:00')
  const fmt = (d: Date) =>
    d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
  return `${fmt(s)} – ${fmt(e)}`
}

function UsagePage() {
  const [summary, setSummary] = useState<UsageSummary | null>(null)
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedPeriod, setSelectedPeriod] = useState<string>('current')
  const [selectedDay, setSelectedDay] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false

    const refetch = async (initial: boolean) => {
      if (initial) setLoading(true)
      const [u, rp] = await Promise.all([
        fetch('/data/usage.json', { cache: 'no-store' })
          .then(r => (r.ok ? r.json() : null))
          .catch(() => null),
        fetch('/data/repos.json', { cache: 'no-store' })
          .then(r => (r.ok ? r.json() : []))
          .catch(() => []),
      ])
      if (cancelled) return
      setSummary(u)
      setRepos(Array.isArray(rp) ? rp : [])
      setLoading(false)
    }

    refetch(true)
    const id = setInterval(() => refetch(false), 5000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  const repoMap = useMemo<RepoInfoMap>(() => {
    const m: RepoInfoMap = {}
    for (const r of repos) {
      const platform: Platform = r.platform || 'github'
      const defaultHost = platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com'
      m[r.full_name] = {
        platform,
        host: (r.base_url || defaultHost).replace(/\/$/, ''),
      }
    }
    return m
  }, [repos])

  const periods = summary?.periods ?? []

  const activePeriod = useMemo<PeriodSummary | null>(() => {
    if (selectedPeriod === 'all' || periods.length === 0) return null
    if (selectedPeriod === 'current') return periods[0] ?? null
    return periods.find(p => p.period_start === selectedPeriod) ?? null
  }, [selectedPeriod, periods])

  // When a day bar is clicked, drill down into that day's breakdowns
  const activeDay = useMemo<DailyPoint | null>(() => {
    if (!selectedDay || !activePeriod) return null
    return activePeriod.daily_trend.find(d => d.date === selectedDay) ?? null
  }, [selectedDay, activePeriod])

  // Clear day selection when period changes
  useEffect(() => { setSelectedDay(null) }, [selectedPeriod])

  const viewTotals = activeDay
    ? { runs: activeDay.runs, total_cost_usd: activeDay.total_cost_usd, input_tokens: activeDay.input_tokens, output_tokens: activeDay.output_tokens, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, duration_ms: 0 } as UsageAggregate
    : activePeriod?.totals ?? summary?.totals
  const viewByOperator = activeDay?.by_operator ?? activePeriod?.by_operator ?? summary?.by_operator ?? {}
  const viewByRepo = activeDay?.by_repo ?? activePeriod?.by_repo ?? summary?.by_repo ?? {}
  const viewByModel = activeDay?.by_model ?? activePeriod?.by_model ?? summary?.by_model ?? {}

  const operatorRows = useMemo(() => {
    return Object.entries(viewByOperator)
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.total_cost_usd - a.total_cost_usd)
  }, [viewByOperator])

  const modelRows = useMemo(() => {
    return Object.entries(viewByModel)
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.cost_usd - a.cost_usd)
  }, [viewByModel])

  const repoRows = useMemo(() => {
    return Object.entries(viewByRepo)
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.total_cost_usd - a.total_cost_usd)
  }, [viewByRepo])

  const empty = !summary || summary.totals.runs === 0

  return (
    <div className="max-w-6xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-5 flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Usage &amp; billing</h1>
          {summary && (
            <p className="text-xs text-muted-foreground mt-1 tabular-nums">
              last refresh {timeAgo(summary.generated_at)}
            </p>
          )}
        </div>
        {!empty && periods.length > 0 && (
          <select
            value={selectedPeriod}
            onChange={e => setSelectedPeriod(e.target.value)}
            className="text-sm bg-secondary border border-border rounded-lg px-3 py-1.5 text-foreground"
          >
            <option value="current">
              Current period ({fmtPeriodLabel(periods[0].period_start, periods[0].period_end)})
            </option>
            {periods.slice(1).map(p => (
              <option key={p.period_start} value={p.period_start}>
                {fmtPeriodLabel(p.period_start, p.period_end)}
              </option>
            ))}
            <option value="all">All time</option>
          </select>
        )}
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground text-center py-8">Loading…</p>
      ) : empty ? (
        <div className="bg-card border border-border rounded-xl p-12 flex flex-col items-center text-center">
          <Receipt className="w-12 h-12 text-muted-foreground/40 mb-4" />
          <p className="text-base font-semibold text-foreground mb-1">No usage data yet</p>
          <p className="text-sm text-muted-foreground">
            No completed runs with usage data yet — run{' '}
            <code className="px-1.5 py-0.5 bg-secondary rounded text-xs font-mono">clawflow run</code>{' '}
            first.
          </p>
        </div>
      ) : (
        <>
          {viewTotals && (
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 mb-5">
              <StatCard label="Total runs" value={fmtNum(viewTotals.runs)} />
              <StatCard
                label="Total cost"
                value={fmtCost(viewTotals.total_cost_usd)}
                tone="brand"
              />
              <StatCard
                label="Total tokens (in + out)"
                value={fmtNum(viewTotals.input_tokens + viewTotals.output_tokens)}
              />
            </div>
          )}

          {activePeriod && activePeriod.daily_trend.length > 0 && (
            <Section title={
              selectedDay
                ? `Daily cost trend — viewing ${selectedDay}`
                : 'Daily cost trend'
            }>
              <DailyChart
                data={activePeriod.daily_trend}
                selectedDay={selectedDay}
                onSelectDay={(day) => setSelectedDay(day === selectedDay ? null : day)}
              />
            </Section>
          )}

          <Section title="By operator">
            <table className="w-full text-sm">
              <thead className="bg-secondary/30 text-xs uppercase text-muted-foreground">
                <tr>
                  <Th align="left">Operator</Th>
                  <Th align="right">Runs</Th>
                  <Th align="right">Cost</Th>
                  <Th align="right">Input</Th>
                  <Th align="right">Output</Th>
                  <Th align="right">Cache read</Th>
                  <Th align="right">Duration</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border tabular-nums">
                {operatorRows.map(r => (
                  <tr key={r.name} className="hover:bg-secondary/20">
                    <td className="px-4 py-2 font-mono text-foreground">{r.name}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{r.runs}</td>
                    <td className="px-4 py-2 text-right text-foreground font-medium">
                      {fmtCost(r.total_cost_usd)}
                    </td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{fmtNum(r.input_tokens)}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{fmtNum(r.output_tokens)}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{fmtNum(r.cache_read_input_tokens)}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{durationStr(r.duration_ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>

          <Section title="By model">
            <table className="w-full text-sm">
              <thead className="bg-secondary/30 text-xs uppercase text-muted-foreground">
                <tr>
                  <Th align="left">Model</Th>
                  <Th align="right">Cost</Th>
                  <Th align="right">Input</Th>
                  <Th align="right">Output</Th>
                  <Th align="right">Cache read</Th>
                  <Th align="right">Cache creation</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border tabular-nums">
                {modelRows.map(r => (
                  <tr key={r.name} className="hover:bg-secondary/20">
                    <td className="px-4 py-2 font-mono text-foreground">{r.name}</td>
                    <td className="px-4 py-2 text-right text-foreground font-medium">{fmtCost(r.cost_usd)}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{fmtNum(r.input_tokens)}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{fmtNum(r.output_tokens)}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{fmtNum(r.cache_read_input_tokens)}</td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{fmtNum(r.cache_creation_input_tokens)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>

          <Section title="By repo">
            <table className="w-full text-sm">
              <thead className="bg-secondary/30 text-xs uppercase text-muted-foreground">
                <tr>
                  <Th align="left">Repo</Th>
                  <Th align="right">Runs</Th>
                  <Th align="right">Cost</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border tabular-nums">
                {repoRows.map(r => (
                  <tr key={r.name} className="hover:bg-secondary/20">
                    <td className="px-4 py-2">
                      <div className="flex items-center gap-2">
                        <VcsIcon
                          repo={r.name}
                          map={repoMap}
                          className="w-3.5 h-3.5 text-muted-foreground shrink-0"
                        />
                        <span className="font-mono text-foreground">{r.name}</span>
                      </div>
                    </td>
                    <td className="px-4 py-2 text-right text-muted-foreground">{r.runs}</td>
                    <td className="px-4 py-2 text-right text-foreground font-medium">
                      {fmtCost(r.total_cost_usd)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>
        </>
      )}
    </div>
  )
}

function DailyChart({ data, selectedDay, onSelectDay }: { data: DailyPoint[]; selectedDay: string | null; onSelectDay: (day: string) => void }) {
  const maxCost = Math.max(...data.map(d => d.total_cost_usd), 0.0001)
  const barWidth = Math.max(4, Math.floor(600 / data.length) - 2)
  const chartWidth = data.length * (barWidth + 2)
  const chartHeight = 120
  const [hovered, setHovered] = useState<number | null>(null)

  return (
    <div className="px-4 py-3">
      <div className="overflow-x-auto">
        <svg
          width={Math.max(chartWidth, 200)}
          height={chartHeight + 32}
          className="block cursor-pointer"
          viewBox={`0 0 ${Math.max(chartWidth, 200)} ${chartHeight + 32}`}
        >
          {data.map((d, i) => {
            const h = (d.total_cost_usd / maxCost) * chartHeight
            const x = i * (barWidth + 2)
            const y = chartHeight - h
            const isHovered = hovered === i
            const isSelected = selectedDay === d.date
            let fillClass = 'fill-brand/40'
            if (isSelected) fillClass = 'fill-brand'
            else if (isHovered) fillClass = 'fill-brand/70'
            else if (selectedDay) fillClass = 'fill-brand/20'
            return (
              <g
                key={d.date}
                onMouseEnter={() => setHovered(i)}
                onMouseLeave={() => setHovered(null)}
                onClick={() => onSelectDay(d.date)}
              >
                <rect
                  x={x}
                  y={y}
                  width={barWidth}
                  height={Math.max(h, 1)}
                  rx={1}
                  className={fillClass}
                />
                {(isHovered || isSelected) && (
                  <text
                    x={x + barWidth / 2}
                    y={y - 4}
                    textAnchor="middle"
                    className="fill-foreground text-[10px]"
                  >
                    {fmtCost(d.total_cost_usd)}
                  </text>
                )}
              </g>
            )
          })}
          {data.map((d, i) => {
            if (data.length <= 15 || i % Math.ceil(data.length / 10) === 0 || i === data.length - 1) {
              const x = i * (barWidth + 2) + barWidth / 2
              const label = new Date(d.date + 'T00:00:00').getDate().toString()
              return (
                <text
                  key={`label-${d.date}`}
                  x={x}
                  y={chartHeight + 14}
                  textAnchor="middle"
                  className="fill-muted-foreground text-[10px]"
                >
                  {label}
                </text>
              )
            }
            return null
          })}
        </svg>
      </div>
      {(hovered !== null || selectedDay) && (
        <div className="text-xs text-muted-foreground mt-1 tabular-nums">
          {(() => {
            const d = selectedDay
              ? data.find(p => p.date === selectedDay)
              : hovered !== null ? data[hovered] : null
            if (!d) return null
            return (
              <>
                {d.date} — {d.runs} run{d.runs !== 1 ? 's' : ''},{' '}
                {fmtCost(d.total_cost_usd)},{' '}
                {fmtNum(d.input_tokens + d.output_tokens)} tokens
                {selectedDay && <span className="ml-2 text-muted-foreground/60">(click again to deselect)</span>}
              </>
            )
          })()}
        </div>
      )}
    </div>
  )
}

function StatCard({
  label,
  value,
  tone = 'neutral',
}: {
  label: string
  value: string
  tone?: 'neutral' | 'brand'
}) {
  const valueCls = tone === 'brand' ? 'text-brand' : 'text-foreground'
  return (
    <div className="bg-card border border-border rounded-xl p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={`text-2xl font-bold mt-0.5 tabular-nums ${valueCls}`}>{value}</div>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mb-5">
      <h2 className="text-sm font-semibold text-foreground mb-2">{title}</h2>
      <div className="bg-card border border-border rounded-xl overflow-hidden">{children}</div>
    </section>
  )
}

function Th({ children, align }: { children: React.ReactNode; align: 'left' | 'right' }) {
  return (
    <th className={`px-4 py-2 font-semibold ${align === 'right' ? 'text-right' : 'text-left'}`}>
      {children}
    </th>
  )
}
