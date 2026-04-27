import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { Receipt } from 'lucide-react'
import { type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'

/**
 * Mirrors snapshot.UsageAggregate / snapshot.ModelAggregate / snapshot.UsageSummary
 * on the Go side. Fields stay snake_case to match writeJSON output.
 */
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

interface UsageSummary {
  generated_at: string
  totals: UsageAggregate
  by_operator: Record<string, UsageAggregate>
  by_repo: Record<string, UsageAggregate>
  by_model: Record<string, ModelAggregate>
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
  const diff = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
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

function UsagePage() {
  const [summary, setSummary] = useState<UsageSummary | null>(null)
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)

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
    // Match the dashboard's polling cadence so a fresh `clawflow run` shows
    // up here without a manual reload.
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

  // Sort each breakdown by cost desc so the highest spenders surface first —
  // that is what a "where did the money go" page is actually for.
  const operatorRows = useMemo(() => {
    if (!summary) return []
    return Object.entries(summary.by_operator)
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.total_cost_usd - a.total_cost_usd)
  }, [summary])

  const modelRows = useMemo(() => {
    if (!summary) return []
    return Object.entries(summary.by_model)
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.cost_usd - a.cost_usd)
  }, [summary])

  const repoRows = useMemo(() => {
    if (!summary) return []
    return Object.entries(summary.by_repo)
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.total_cost_usd - a.total_cost_usd)
  }, [summary])

  const empty =
    !summary ||
    summary.totals.runs === 0

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
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 mb-5">
            <StatCard label="Total runs" value={fmtNum(summary!.totals.runs)} />
            <StatCard
              label="Total cost"
              value={fmtCost(summary!.totals.total_cost_usd)}
              tone="brand"
            />
            <StatCard
              label="Total tokens (in + out)"
              value={fmtNum(
                summary!.totals.input_tokens + summary!.totals.output_tokens,
              )}
            />
          </div>

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

function StatCard({
  label,
  value,
  tone = 'neutral',
}: {
  label: string
  value: string
  tone?: 'neutral' | 'brand'
}) {
  const valueCls = tone === 'brand' ? 'text-primary' : 'text-foreground'
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
