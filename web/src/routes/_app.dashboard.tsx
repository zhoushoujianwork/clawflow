import { createFileRoute, Link } from '@tanstack/react-router'
import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Search,
  ExternalLink,
  ChevronRight,
  CheckCircle2,
  Loader2,
  XCircle,
  SkipForward,
  Activity,
  Clock,
  Play,
  Pause,
} from 'lucide-react'
import { cn } from '../lib/utils'
import { issueUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'

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
  issue_state?: string
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
  platform?: Platform
  base_url?: string
  enabled: boolean
}

/**
 * Mirror of snapshot.PendingEntry on the Go side. One (issue × matching
 * operator) pair waiting to be processed. Many of these can stack up if the
 * runner only fires one operator per issue per pass.
 */
interface Pending {
  repo: string
  issue_number: number
  issue_title?: string
  issue_state?: string
  operator: string
  labels?: string[]
  captured_at: string
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
  // Defensive: a missing/invalid ISO must never render as a giant
  // negative number. Past bug: a `running` placeholder occasionally
  // landed on disk with a zero or future started_at, which produced
  // "-63913033671349s ago" in the UI. We now treat anything we can't
  // parse, and anything claiming the future, as "just now" — the
  // user sees nothing scary while the bad data ages out.
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

function durationStr(start: string, end?: string): string | null {
  // Defensive: a still-running snapshot used to land on disk with
  // ended_at="0001-01-01T00:00:00Z" (Go's zero-time, which json
  // omitempty does NOT skip on a value-typed time.Time). The backend
  // now omits the field when nil, but older snapshots and any future
  // regression would render here as "-639130766420870smago". Treat any
  // pre-2000 end time as "still running" and bail.
  if (!end) return null
  const tStart = new Date(start).getTime()
  const tEnd = new Date(end).getTime()
  if (!isFinite(tStart) || !isFinite(tEnd)) return null
  if (tEnd < 946684800000) return null // before 2000-01-01 → bogus
  const ms = tEnd - tStart
  if (ms < 0) return null
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
  const [pending, setPending] = useState<Pending[]>([])
  const [loading, setLoading] = useState(true)
  const [runBusy, setRunBusy] = useState(false)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [query, setQuery] = useState('')
  const [repoFilter, setRepoFilter] = useState<string>('all')
  const [visibleCount, setVisibleCount] = useState(20)

  useEffect(() => {
    let cancelled = false

    const refetch = async (initial: boolean) => {
      if (initial) setLoading(true)
      const [r, m, rp, pd] = await Promise.all([
        fetch('/data/runs.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
        fetch('/data/meta.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : null)).catch(() => null),
        fetch('/data/repos.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
        // pending.json is only written by clawflow versions that snapshot the
        // queue; missing file is normal on older installs and renders as no
        // pending section.
        fetch('/data/pending.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      ])
      if (cancelled) return
      setRuns(Array.isArray(r) ? r : [])
      setMeta(m)
      setRepos(Array.isArray(rp) ? rp : [])
      setPending(Array.isArray(pd) ? pd : [])
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

  // Poll run status — also picks up the auto-run scheduler state
  // (interval, paused, next fire time) so we render everything in one
  // banner without a second endpoint.
  const [intervalMin, setIntervalMin] = useState(0)
  const [paused, setPaused] = useState(false)
  const [nextFireMs, setNextFireMs] = useState<number>(0)
  const [pauseBusy, setPauseBusy] = useState(false)

  useEffect(() => {
    const poll = () => {
      fetch('/api/run/status', { cache: 'no-store' })
        .then(r => r.ok ? r.json() : null)
        .then(d => {
          if (!d) return
          setRunBusy(d.status === 'running')
          setIntervalMin(d.interval_minutes || 0)
          setPaused(!!d.paused)
          setNextFireMs(d.next_fire_unix_ms || 0)
        })
        .catch(() => {})
    }
    poll()
    const id = setInterval(poll, 3000)
    return () => clearInterval(id)
  }, [])

  // Tick once a second to keep the "next in M:SS" countdown live.
  // Without this the countdown only refreshes when /api/run/status
  // polls (every 3s), which feels janky.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  const triggerRun = useCallback(() => {
    setRunBusy(true)
    fetch('/api/run', { method: 'POST' })
      .then(r => r.json())
      .then(d => { if (d.status === 'busy') setRunBusy(true) })
      .catch(() => setRunBusy(false))
  }, [])

  const togglePause = useCallback(() => {
    const next = !paused
    setPauseBusy(true)
    fetch('/api/run/pause', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paused: next }),
    })
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d) setPaused(!!d.paused)
        setPauseBusy(false)
      })
      .catch(() => setPauseBusy(false))
  }, [paused])


  const counts = useMemo(() => {
    const c = { total: runs.length, success: 0, failed: 0, skipped: 0, running: 0 }
    for (const r of runs) c[r.status]++
    return c
  }, [runs])

  const repoOptions = useMemo(() => {
    const set = new Set(runs.map(r => r.repo))
    return ['all', ...Array.from(set).sort()]
  }, [runs])

  // Split filtered runs into "running now" and "everything else (history)".
  // Running rows render in their own section above; history is the main
  // scrollable list. When the user explicitly clicks the Running stat
  // card we keep all running rows in `filteredHistory` and skip the
  // dedicated section, so the page doesn't show the same row twice.
  // Reset pagination when filters change
  useEffect(() => { setVisibleCount(20) }, [statusFilter, repoFilter, query])

  const { filteredRunning, filteredHistory } = useMemo(() => {
    const q = query.trim().toLowerCase()
    const passesFilters = (r: Run) => {
      if (statusFilter !== 'all' && r.status !== statusFilter) return false
      if (repoFilter !== 'all' && r.repo !== repoFilter) return false
      if (q && !(r.issue_title || '').toLowerCase().includes(q) && !String(r.issue_number).includes(q)) return false
      return true
    }
    const matched = runs.filter(passesFilters)
    if (statusFilter === 'running') {
      return { filteredRunning: [], filteredHistory: matched }
    }
    return {
      filteredRunning: matched.filter(r => r.status === 'running'),
      filteredHistory: matched.filter(r => r.status !== 'running'),
    }
  }, [runs, statusFilter, repoFilter, query])

  // Pending ignores statusFilter (queued items have no run status yet) but
  // honors repo + search filters so the user can drill into one repo's queue.
  //
  // Dedup against running runs: pending.json is captured at the start of
  // the scan and shows every (issue × matching-operator) combination,
  // including the one the runner just kicked off. Once that operator's
  // meta.json appears in runs.json with status=running we should hide
  // the matching pending row so the user doesn't see "queued" and
  // "running" for the same job.
  // Keys for runs that are already in-flight or completed this cycle.
  // Pending entries matching these are stale snapshots and should be hidden.
  const activeRunKeys = useMemo(() => {
    const s = new Set<string>()
    for (const r of runs) {
      if (r.status === 'running' || r.status === 'success' || r.status === 'skipped') {
        s.add(`${r.repo}#${r.issue_number}/${r.operator}`)
      }
    }
    return s
  }, [runs])

  const filteredPending = useMemo(() => {
    const q = query.trim().toLowerCase()
    return pending.filter(p => {
      if (p.issue_state === 'closed') return false
      if (activeRunKeys.has(`${p.repo}#${p.issue_number}/${p.operator}`)) return false
      if (repoFilter !== 'all' && p.repo !== repoFilter) return false
      if (q && !(p.issue_title || '').toLowerCase().includes(q) && !String(p.issue_number).includes(q) && !p.operator.toLowerCase().includes(q)) return false
      return true
    })
  }, [pending, repoFilter, query, activeRunKeys])

  // Build the per-repo URL map from the same repos.json the dashboard already
  // pulls — no extra fetch needed.
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

  const enabledRepos = repos.filter(r => r.enabled).length

  return (
    <div className="max-w-6xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-5 flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Dashboard</h1>
          {meta && (
            <p className="text-xs text-muted-foreground mt-1 tabular-nums">
              last run {timeAgo(meta.last_refresh)} · {enabledRepos} repo{enabledRepos === 1 ? '' : 's'} enabled
            </p>
          )}
        </div>
        <button
          onClick={triggerRun}
          disabled={runBusy}
          className={cn(
            'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors',
            runBusy
              ? 'bg-muted text-muted-foreground cursor-not-allowed'
              : 'bg-brand text-on-brand hover:bg-brand-hover',
          )}
        >
          {runBusy ? (
            <><Loader2 className="w-3.5 h-3.5 animate-spin" /> Running…</>
          ) : (
            <><Play className="w-3.5 h-3.5" /> Run</>
          )}
        </button>
      </div>

      {intervalMin > 0 && (
        <AutoRunBanner
          intervalMin={intervalMin}
          paused={paused}
          nextFireMs={nextFireMs}
          now={now}
          busy={pauseBusy}
          onToggle={togglePause}
        />
      )}


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

      {filteredPending.length > 0 && (
        <section className="mb-4">
          <h2 className="text-sm font-semibold text-foreground mb-2 flex items-center gap-2">
            <Clock className="w-3.5 h-3.5 text-muted-foreground" />
            Pending
            <span className="font-normal text-muted-foreground">({filteredPending.length})</span>
            <span className="font-normal text-xs text-muted-foreground ml-1">— queued for the next <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">clawflow run</code></span>
          </h2>
          <div className="bg-card border border-border rounded-xl shadow-sm divide-y divide-border overflow-hidden">
            {filteredPending.map(p => (
              <PendingRow key={`${p.repo}#${p.issue_number}/${p.operator}`} p={p} repoMap={repoMap} />
            ))}
          </div>
        </section>
      )}

      {/* Running runs get their own section above the history. They're
          live state — a long-running implement op or a stuck claude can
          stay on screen for minutes — and burying them mid-list among
          weeks of finished runs makes "what's happening right now"
          harder to spot. The status filter still applies; an explicit
          "running" filter just hides this whole section because the
          history list below already shows the running rows when that
          filter is active. */}
      {statusFilter !== 'running' && filteredRunning.length > 0 && (
        <section className="mb-4">
          <h2 className="text-sm font-semibold text-foreground mb-2 flex items-center gap-2">
            <Loader2 className="w-3.5 h-3.5 text-blue-600 animate-spin" />
            Running
            <span className="font-normal text-muted-foreground">({filteredRunning.length})</span>
            <span className="font-normal text-xs text-muted-foreground ml-1">— live</span>
          </h2>
          <div className="bg-card border border-blue-200 rounded-xl shadow-sm divide-y divide-border overflow-hidden">
            {filteredRunning.map(r => <Row key={r.path} r={r} repoMap={repoMap} />)}
          </div>
        </section>
      )}

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
          {filteredHistory.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-6">No runs match the current filters.</p>
          ) : (
            <>
              {filteredHistory.slice(0, visibleCount).map(r => <Row key={r.path} r={r} repoMap={repoMap} />)}
              {visibleCount < filteredHistory.length && (
                <button
                  onClick={() => setVisibleCount(c => c + 20)}
                  className="w-full py-3 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors"
                >
                  Load more ({filteredHistory.length - visibleCount} remaining)
                </button>
              )}
            </>
          )}
        </div>
      )}
    </div>
  )
}

function AutoRunBanner({
  intervalMin,
  paused,
  nextFireMs,
  now,
  busy,
  onToggle,
}: {
  intervalMin: number
  paused: boolean
  nextFireMs: number
  now: number
  busy: boolean
  onToggle: () => void
}) {
  // Format the countdown to next tick. When paused we still show what
  // "would have fired" so the user knows roughly how often this banner
  // will move when they resume — useful sanity check for the interval
  // they configured.
  const countdownLabel = (() => {
    if (!nextFireMs) return ''
    const ms = nextFireMs - now
    if (ms <= 0) return 'now'
    const totalSec = Math.floor(ms / 1000)
    const m = Math.floor(totalSec / 60)
    const s = totalSec % 60
    return `${m}:${s.toString().padStart(2, '0')}`
  })()

  const summary = paused
    ? `Auto-run paused · would fire every ${intervalMin}m`
    : `Auto-run · every ${intervalMin}m${countdownLabel ? ` · next in ${countdownLabel}` : ''}`

  return (
    <div
      className={cn(
        'flex items-center gap-3 px-4 py-2.5 rounded-xl mb-4 border',
        paused ? 'bg-amber-50 border-amber-200' : 'bg-blue-50 border-blue-200',
      )}
    >
      {paused ? (
        <Pause className="w-4 h-4 shrink-0 text-amber-700" />
      ) : (
        <Clock className="w-4 h-4 shrink-0 text-blue-700" />
      )}
      <span
        className={cn(
          'text-sm flex-1 tabular-nums',
          paused ? 'text-amber-900' : 'text-blue-900',
        )}
      >
        {summary}
      </span>
      <button
        onClick={onToggle}
        disabled={busy}
        className={cn(
          'inline-flex items-center gap-1.5 px-3 py-1 rounded-lg text-xs font-medium transition-colors',
          busy
            ? 'bg-muted text-muted-foreground cursor-not-allowed'
            : paused
              ? 'bg-amber-600 text-white hover:bg-amber-700'
              : 'bg-blue-600 text-white hover:bg-blue-700',
        )}
      >
        {busy ? (
          <><Loader2 className="w-3 h-3 animate-spin" /> …</>
        ) : paused ? (
          <><Play className="w-3 h-3" /> Resume</>
        ) : (
          <><Pause className="w-3 h-3" /> Pause</>
        )}
      </button>
    </div>
  )
}

function PendingRow({ p, repoMap }: { p: Pending; repoMap: RepoInfoMap }) {
  return (
    <div className="flex items-center gap-3 px-4 py-2.5">
      <span className="inline-flex items-center gap-1 border px-1.5 py-0.5 rounded text-[11px] font-semibold bg-amber-100 text-amber-800 border-amber-200 shrink-0">
        <Clock className="w-3 h-3" />
        queued
      </span>
      <VcsIcon repo={p.repo} map={repoMap} className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
      <a
        href={issueUrl(p.repo, p.issue_number, repoMap)}
        target="_blank"
        rel="noopener noreferrer"
        className="font-mono text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0"
      >
        #{p.issue_number}
      </a>
      <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-mono bg-secondary text-foreground border border-border shrink-0">
        {p.operator}
      </span>
      <span className="text-sm text-foreground truncate flex-1">
        {p.issue_title || '(no title)'}
      </span>
      <Link
        to="/repos/$repoName"
        params={{ repoName: encodeURIComponent(p.repo) }}
        onClick={e => e.stopPropagation()}
        className="text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0 hidden sm:inline"
      >
        {p.repo}
      </Link>
      <span className="text-xs text-muted-foreground shrink-0 w-16 text-right">{timeAgo(p.captured_at)}</span>
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

function Row({ r, repoMap }: { r: Run; repoMap: RepoInfoMap }) {
  const dur = durationStr(r.started_at, r.ended_at)
  const runHref = `/runs/${repoSlug(r.repo)}/issue-${r.issue_number}/${runIdFromPath(r.path)}`
  return (
    <a
      href={runHref}
      target="_blank"
      rel="noopener noreferrer"
      className="flex items-center gap-3 px-4 py-2.5 hover:bg-secondary/50 transition-colors group"
    >
      <StatusChip status={r.status} />
      <VcsIcon repo={r.repo} map={repoMap} className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
      <a
        href={issueUrl(r.repo, r.issue_number, repoMap)}
        target="_blank"
        rel="noopener noreferrer"
        onClick={e => e.stopPropagation()}
        className={`font-mono text-xs hover:text-foreground hover:underline shrink-0 ${r.issue_state === 'closed' ? 'text-muted-foreground/50 line-through' : 'text-muted-foreground'}`}
      >
        #{r.issue_number}
      </a>
      <span className="text-sm text-foreground truncate flex-1">
        {r.operator} · {r.issue_title || '(no title)'}
      </span>
      <Link
        to="/repos/$repoName"
        params={{ repoName: encodeURIComponent(r.repo) }}
        onClick={e => e.stopPropagation()}
        className="text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0 hidden sm:inline"
      >
        {r.repo}
      </Link>
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
    </a>
  )
}
