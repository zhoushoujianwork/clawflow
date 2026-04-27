import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import {
  Search,
  ExternalLink,
  ChevronRight,
  CheckCircle2,
  Loader2,
  XCircle,
  SkipForward,
  Activity,
} from 'lucide-react'
import { cn } from '../lib/utils'

/**
 * Shape of one entry in /data/runs.json. Mirrors snapshot.RunIndexEntry on
 * the Go side — if the Go type gains a field, add it here and the dashboard
 * renders richer detail.
 */
interface Run {
  operator: string
  repo: string
  issue_number: number
  issue_title?: string
  started_at: string
  ended_at?: string
  status: 'success' | 'failed' | 'skipped' | 'running'
  summary?: string
  pr_url?: string
  error?: string
  /** dashboard-relative path to the run dir (contains events.jsonl + meta.json) */
  path: string
}

interface Meta {
  clawflow_version: string
  last_refresh: string
}

interface Repo {
  full_name: string
  platform?: string
  enabled: boolean
}

type StatusFilter = 'all' | 'success' | 'failed' | 'skipped' | 'running'

const statusPill: Record<Run['status'], { label: string; cls: string; Icon: typeof CheckCircle2 }> = {
  success: { label: 'success', cls: 'bg-green-100 text-green-700 border-green-200', Icon: CheckCircle2 },
  running: { label: 'running', cls: 'bg-blue-100 text-blue-700 border-blue-200', Icon: Loader2 },
  failed:  { label: 'failed',  cls: 'bg-red-100 text-red-700 border-red-200',     Icon: XCircle },
  skipped: { label: 'skipped', cls: 'bg-muted text-muted-foreground border-border', Icon: SkipForward },
}

function StatusChip({ status }: { status: Run['status'] }) {
  const { label, cls, Icon } = statusPill[status]
  const spinning = status === 'running'
  return (
    <span className={cn('inline-flex items-center gap-1 border px-1.5 py-0.5 rounded text-[11px] font-semibold', cls)}>
      <Icon className={cn('w-3 h-3', spinning && 'animate-spin')} />
      {label}
    </span>
  )
}

function timeAgo(iso: string): string {
  const diff = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}min ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

function durationStr(start: string, end?: string): string | null {
  if (!end) return null
  const ms = new Date(end).getTime() - new Date(start).getTime()
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m${s % 60}s`
}

/** run id = ".../runs/<repo-slug>/issue-<N>/<timestamp>/" → the timestamp */
function runIdFromPath(path: string): string {
  return path.replace(/\/$/, '').split('/').pop() || ''
}

/** owner/repo slug in our runs/<slug> layout replaces slashes with `__`. */
function repoSlug(repo: string): string {
  return repo.replace(/\//g, '__')
}

export const Route = createFileRoute('/_app/dashboard')({
  component: Dashboard,
})

function Dashboard() {
  const [runs, setRuns] = useState<Run[]>([])
  const [meta, setMeta] = useState<Meta | null>(null)
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [query, setQuery] = useState('')
  const [repoFilter, setRepoFilter] = useState<string>('all')

  useEffect(() => {
    let cancelled = false

    const refetch = async (initial: boolean) => {
      if (initial) setLoading(true)
      const [r, m, rp] = await Promise.all([
        fetch('/data/runs.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
        fetch('/data/meta.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : null)).catch(() => null),
        fetch('/data/repos.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      ])
      if (cancelled) return
      setRuns(Array.isArray(r) ? r : [])
      setMeta(m)
      setRepos(Array.isArray(rp) ? rp : [])
      setLoading(false)
    }

    refetch(true)
    // Periodically refresh so a `clawflow run` triggered from another shell
    // (or cron) shows up without a manual reload. 5s is unobtrusive and
    // matches the cadence the user is realistically waiting at.
    const id = setInterval(() => refetch(false), 5000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  const counts = useMemo(() => {
    const c = { total: runs.length, success: 0, failed: 0, skipped: 0, running: 0 }
    for (const r of runs) c[r.status]++
    return c
  }, [runs])

  const repoOptions = useMemo(() => {
    const set = new Set(runs.map(r => r.repo))
    return ['all', ...Array.from(set).sort()]
  }, [runs])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    return runs.filter(r => {
      if (statusFilter !== 'all' && r.status !== statusFilter) return false
      if (repoFilter !== 'all' && r.repo !== repoFilter) return false
      if (q && !(r.issue_title || '').toLowerCase().includes(q) && !String(r.issue_number).includes(q)) return false
      return true
    })
  }, [runs, statusFilter, repoFilter, query])

  const enabledRepos = repos.filter(r => r.enabled).length

  return (
    <div className="max-w-6xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-5 flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Dashboard</h1>
          {meta && (
            <p className="text-xs text-muted-foreground mt-1 tabular-nums">
              {meta.clawflow_version} · last run {timeAgo(meta.last_refresh)} · {enabledRepos} repo{enabledRepos === 1 ? '' : 's'} enabled
            </p>
          )}
        </div>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-5 gap-2 mb-4">
        <StatCard label="Total"   value={counts.total}   filter="all"     active={statusFilter === 'all'}     onClick={setStatusFilter} tone="neutral" />
        <StatCard label="Running" value={counts.running} filter="running" active={statusFilter === 'running'} onClick={setStatusFilter} tone="blue" />
        <StatCard label="Success" value={counts.success} filter="success" active={statusFilter === 'success'} onClick={setStatusFilter} tone="green" />
        <StatCard label="Failed"  value={counts.failed}  filter="failed"  active={statusFilter === 'failed'}  onClick={setStatusFilter} tone="red" />
        <StatCard label="Skipped" value={counts.skipped} filter="skipped" active={statusFilter === 'skipped'} onClick={setStatusFilter} tone="muted" />
      </div>

      <div className="flex gap-2 mb-3 flex-wrap">
        <div className="relative flex-1 min-w-[200px]">
          <Search className="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground" />
          <input
            type="text"
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder="Search issue number or title…"
            className="w-full pl-8 pr-3 py-1.5 text-sm bg-card border border-border rounded-lg focus:outline-none focus:ring-2 focus:ring-primary/30"
          />
        </div>
        {repoOptions.length > 2 && (
          <select
            value={repoFilter}
            onChange={e => setRepoFilter(e.target.value)}
            className="px-3 py-1.5 text-sm bg-card border border-border rounded-lg focus:outline-none focus:ring-2 focus:ring-primary/30"
          >
            {repoOptions.map(r => (
              <option key={r} value={r}>{r === 'all' ? 'All repos' : r}</option>
            ))}
          </select>
        )}
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground text-center py-8">Loading…</p>
      ) : runs.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-12 flex flex-col items-center text-center">
          <Activity className="w-12 h-12 text-muted-foreground/40 mb-4" />
          <p className="text-base font-semibold text-foreground mb-1">No runs yet</p>
          <p className="text-sm text-muted-foreground">
            Execute <code className="px-1.5 py-0.5 bg-secondary rounded text-xs font-mono">clawflow run</code> in your terminal and refresh.
          </p>
        </div>
      ) : (
        <div className="bg-card border border-border rounded-xl shadow-sm divide-y divide-border overflow-hidden">
          {filtered.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-6">No runs match the current filters.</p>
          ) : (
            filtered.map(r => <Row key={r.path} r={r} />)
          )}
        </div>
      )}
    </div>
  )
}

function StatCard({
  label,
  value,
  filter,
  active,
  onClick,
  tone,
}: {
  label: string
  value: number
  filter: StatusFilter
  active: boolean
  onClick: (f: StatusFilter) => void
  tone: 'neutral' | 'blue' | 'green' | 'red' | 'muted'
}) {
  const toneCls = {
    neutral: 'text-foreground',
    blue: 'text-blue-600',
    green: 'text-green-600',
    red: 'text-red-600',
    muted: 'text-muted-foreground',
  }[tone]
  return (
    <button
      onClick={() => onClick(filter)}
      className={cn(
        'bg-card border rounded-xl p-3 text-left transition-all hover:shadow-sm',
        active ? 'border-primary ring-2 ring-primary/20' : 'border-border',
      )}
    >
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={cn('text-2xl font-bold mt-0.5', toneCls)}>{value}</div>
    </button>
  )
}

function Row({ r }: { r: Run }) {
  const dur = durationStr(r.started_at, r.ended_at)
  return (
    <Link
      to="/runs/$slug/$issue/$ts"
      params={{ slug: repoSlug(r.repo), issue: `issue-${r.issue_number}`, ts: runIdFromPath(r.path) }}
      className="flex items-center gap-3 px-4 py-2.5 hover:bg-secondary/50 transition-colors group"
    >
      <StatusChip status={r.status} />
      <span className="font-mono text-xs text-muted-foreground shrink-0">#{r.issue_number}</span>
      <span className="text-sm text-foreground truncate flex-1">
        {r.operator} · {r.issue_title || '(no title)'}
      </span>
      <span className="text-xs text-muted-foreground shrink-0 hidden sm:inline">{r.repo}</span>
      {dur && <span className="text-xs text-muted-foreground shrink-0 tabular-nums w-14 text-right">{dur}</span>}
      {r.pr_url && (
        <a
          href={r.pr_url}
          target="_blank"
          rel="noopener noreferrer"
          onClick={e => e.stopPropagation()}
          className="inline-flex items-center gap-0.5 text-xs text-foreground hover:underline shrink-0"
        >
          PR <ExternalLink className="w-3 h-3" />
        </a>
      )}
      <span className="text-xs text-muted-foreground shrink-0 w-16 text-right">{timeAgo(r.started_at)}</span>
      <ChevronRight className="w-4 h-4 text-muted-foreground/40 group-hover:text-muted-foreground shrink-0" />
    </Link>
  )
}
