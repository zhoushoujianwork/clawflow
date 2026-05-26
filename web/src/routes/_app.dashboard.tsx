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
  Square,
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
  status: 'success' | 'failed' | 'skipped' | 'running' | 'cancelled' | 'no-marker' | 'skipped-empty'
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
  /** Hostname this repo is pinned to. Empty means "any machine". When set
   *  and != current hostname, the dashboard hides activity from this view
   *  so it doesn't show queued/running rows that this machine will never
   *  actually execute. */
  bound_machine?: string
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

/**
 * Mirror of snapshot.IssueEntry on the Go side, from /data/issues.json. This
 * is the authoritative *current* issue state — unlike runs.json, whose
 * `issue_state` is a point-in-time snapshot captured when the run executed and
 * may be stale (e.g. null for runs that predate a later close). The dashboard
 * cross-references this map so closed issues render with strikethrough even
 * when their run records still say open/null.
 */
interface Issue {
  repo: string
  issue_number: number
  state: string // "open" | "closed"
}

type StatusFilter = 'all' | 'success' | 'failed' | 'skipped' | 'running' | 'cancelled'
type ViewMode = 'run' | 'issue'

/** Ordered list of known pipeline stages for badge display. Unknown operators
 *  are appended after these in discovery order. */
const KNOWN_STAGES = ['classify', 'implement', 'evaluate-feat', 'evaluate-bug', 'reply-comment', 'decompose']

interface IssueGroup {
  key: string       // `${repo}#${issueNumber}`
  repo: string
  issueNumber: number
  issueTitle: string
  issueState: string
  /** Latest run per operator name */
  stages: Map<string, Run>
  /** max started_at across all runs */
  lastActivity: string
  allRuns: Run[]
  overallStatus: Run['status']
}

/** Derive overall status for an issue from its per-operator latest runs.
 *  Priority: running > failed/no-marker/skipped-empty > success > cancelled > skipped */
function issueOverallStatus(stages: Map<string, Run>): Run['status'] {
  const statuses = Array.from(stages.values()).map(r => r.status)
  if (statuses.some(s => s === 'running')) return 'running'
  if (statuses.some(s => s === 'failed' || s === 'no-marker' || s === 'skipped-empty')) return 'failed'
  if (statuses.every(s => s === 'success')) return 'success'
  if (statuses.some(s => s === 'cancelled')) return 'cancelled'
  return 'skipped'
}

const statusPill: Record<Run['status'], { label: string; cls: string; Icon: typeof CheckCircle2 }> = {
  success:       { label: 'success',      cls: 'bg-green-100 text-green-700 border-green-200',   Icon: CheckCircle2 },
  running:       { label: 'running',      cls: 'bg-blue-100 text-blue-700 border-blue-200',      Icon: Loader2 },
  failed:        { label: 'failed',       cls: 'bg-red-100 text-red-700 border-red-200',         Icon: XCircle },
  skipped:       { label: 'skipped',      cls: 'bg-muted text-muted-foreground border-border',   Icon: SkipForward },
  cancelled:     { label: 'cancelled',    cls: 'bg-amber-50 text-amber-700 border-amber-200',    Icon: Square },
  'no-marker':   { label: 'no marker',    cls: 'bg-orange-100 text-orange-700 border-orange-200', Icon: XCircle },
  'skipped-empty': { label: 'empty',      cls: 'bg-orange-50 text-orange-600 border-orange-200', Icon: SkipForward },
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
  const [issues, setIssues] = useState<Issue[]>([])
  const [loading, setLoading] = useState(true)
  const [runBusy, setRunBusy] = useState(false)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [query, setQuery] = useState('')
  const [repoFilter, setRepoFilter] = useState<string>('all')
  const [visibleCount, setVisibleCount] = useState(20)
  // Bumping this counter forces the data-fetch effect to re-run, used after
  // the Cancel button so the dashboard reflects the killed run immediately
  // instead of waiting up to 5s for the next polling tick.
  const [refreshTick, setRefreshTick] = useState(0)
  // Per-row "cancel in flight" state so we can disable the button and show
  // a spinner while the kill is in progress, and so a double-click doesn't
  // fire two POSTs against the same lock.
  const [cancellingKey, setCancellingKey] = useState<string | null>(null)
  const [hostname, setHostname] = useState<string>('')
  const [viewMode, setViewMode] = useState<ViewMode>('issue')
  const [expandedIssues, setExpandedIssues] = useState<Set<string>>(new Set())

  const toggleIssueExpand = useCallback((key: string) => {
    setExpandedIssues(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])

  useEffect(() => {
    let cancelled = false

    const refetch = async (initial: boolean) => {
      if (initial) setLoading(true)
      const [r, m, rp, pd, iss, settings] = await Promise.all([
        fetch('/data/runs.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
        fetch('/data/meta.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : null)).catch(() => null),
        fetch('/data/repos.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
        // pending.json is only written by clawflow versions that snapshot the
        // queue; missing file is normal on older installs and renders as no
        // pending section.
        fetch('/data/pending.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
        // issues.json carries the authoritative current state used to override
        // the stale per-run issue_state when deciding closed-issue styling.
        fetch('/data/issues.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
        fetch('/api/settings', { cache: 'no-store' }).then(r => (r.ok ? r.json() : null)).catch(() => null),
      ])
      if (cancelled) return
      setRuns(Array.isArray(r) ? r : [])
      setMeta(m)
      setRepos(Array.isArray(rp) ? rp : [])
      setPending(Array.isArray(pd) ? pd : [])
      setIssues(Array.isArray(iss) ? iss : [])
      if (settings?.global?.hostname) setHostname(settings.global.hostname as string)
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
  }, [refreshTick])

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

  const cancelRun = useCallback((repo: string, issue: number) => {
    // Key on (repo, issue) since that's what the lock + the cancel API
    // are keyed by; multiple operators on one issue share a lock so they
    // collapse to a single in-flight cancel.
    const key = `${repo}#${issue}`
    setCancellingKey(key)
    fetch('/api/run/cancel', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo, issue }),
    })
      .then(r => r.ok ? r.json() : null)
      .catch(() => null)
      .finally(() => {
        setCancellingKey(null)
        // Force an immediate refetch so the killed row drops out of the
        // Running section and any matching pending re-appears (next run
        // will rebuild it). Without this the row sits on screen for up
        // to 5s, which feels broken.
        setRefreshTick(t => t + 1)
      })
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
    const c = { total: runs.length, success: 0, failed: 0, skipped: 0, running: 0, cancelled: 0 }
    for (const r of runs) {
      // no-marker and skipped-empty are label-state-machine failures: bucket
      // them under "failed" for the stat cards so they surface as actionable.
      const bucket = (r.status === 'no-marker' || r.status === 'skipped-empty') ? 'failed' : r.status
      if (bucket in c) c[bucket as keyof typeof c]++
    }
    return c
  }, [runs])

  /** Issue-level stat counts. Groups all runs by (repo, issue_number) and
   *  derives one status per issue using issueOverallStatus. */
  const issueCounts = useMemo(() => {
    const map = new Map<string, Run[]>()
    for (const r of runs) {
      const key = `${r.repo}#${r.issue_number}`
      if (!map.has(key)) map.set(key, [])
      map.get(key)!.push(r)
    }
    const c = { total: map.size, success: 0, failed: 0, skipped: 0, running: 0, cancelled: 0 }
    for (const issueRuns of map.values()) {
      const stages = new Map<string, Run>()
      for (const r of issueRuns) {
        const existing = stages.get(r.operator)
        if (!existing || r.started_at > existing.started_at) stages.set(r.operator, r)
      }
      const status = issueOverallStatus(stages)
      // Bucket no-marker / skipped-empty as failed at the issue level too
      const bucket = (status === 'no-marker' || status === 'skipped-empty') ? 'failed' : status
      if (bucket in c) c[bucket as keyof typeof c]++
    }
    return c
  }, [runs])

  const repoOptions = useMemo(() => {
    const set = new Set(runs.map(r => r.repo))
    return ['all', ...Array.from(set).sort()]
  }, [runs])

  // Reset pagination when filters or view mode change
  useEffect(() => { setVisibleCount(20) }, [statusFilter, repoFilter, query, viewMode])

  // Authoritative current state per issue, keyed `${repo}#${number}`. Sourced
  // from issues.json rather than the per-run snapshot so a later close is
  // reflected even on old run records whose issue_state is null/open.
  const issueStateMap = useMemo(() => {
    const m = new Map<string, string>()
    for (const i of issues) m.set(`${i.repo}#${i.issue_number}`, i.state)
    return m
  }, [issues])

  const { filteredRunning, filteredHistory } = useMemo(() => {
    const q = query.trim().toLowerCase()
    // Override the stale per-run issue_state with the authoritative current
    // state. filteredHistory feeds both the By-Run rows and the By-Issue
    // groups, so this single enrichment fixes closed-issue styling everywhere.
    const withState = (r: Run): Run => {
      const cur = issueStateMap.get(`${r.repo}#${r.issue_number}`)
      return cur && cur !== r.issue_state ? { ...r, issue_state: cur } : r
    }
    const passesFilters = (r: Run) => {
      if (statusFilter !== 'all') {
        // "failed" filter also captures no-marker and skipped-empty since they
        // are bucketed under failed in the stat cards (issue #143).
        const effectiveStatus = (r.status === 'no-marker' || r.status === 'skipped-empty') ? 'failed' : r.status
        if (effectiveStatus !== statusFilter) return false
      }
      if (repoFilter !== 'all' && r.repo !== repoFilter) return false
      if (q && !(r.issue_title || '').toLowerCase().includes(q) && !String(r.issue_number).includes(q)) return false
      return true
    }
    const matched = runs.filter(passesFilters).map(withState)
    if (statusFilter === 'running') {
      return { filteredRunning: [], filteredHistory: matched }
    }
    return {
      filteredRunning: matched.filter(r => r.status === 'running'),
      filteredHistory: matched.filter(r => r.status !== 'running'),
    }
  }, [runs, statusFilter, repoFilter, query, issueStateMap])

  /** Build issue groups from filteredHistory for the issue-aggregated view.
   *  Runs already passed the status/repo/search filters so groups reflect the
   *  same slice the flat view would show. */
  const filteredIssueGroups = useMemo<IssueGroup[]>(() => {
    const map = new Map<string, { runs: Run[]; repo: string; issueNumber: number; issueTitle: string; issueState: string }>()
    for (const r of filteredHistory) {
      const key = `${r.repo}#${r.issue_number}`
      if (!map.has(key)) {
        map.set(key, {
          runs: [],
          repo: r.repo,
          issueNumber: r.issue_number,
          issueTitle: r.issue_title || '(no title)',
          issueState: r.issue_state || 'open',
        })
      }
      map.get(key)!.runs.push(r)
    }

    const groups: IssueGroup[] = []
    for (const [key, { runs, repo, issueNumber, issueTitle, issueState }] of map) {
      // Latest run per operator
      const stages = new Map<string, Run>()
      for (const r of runs) {
        const existing = stages.get(r.operator)
        if (!existing || r.started_at > existing.started_at) stages.set(r.operator, r)
      }
      // Running operator takes priority even if started later
      for (const r of runs) {
        if (r.status === 'running') stages.set(r.operator, r)
      }
      const lastActivity = runs.reduce((best, r) => (r.started_at > best ? r.started_at : best), '')
      const overallStatus = issueOverallStatus(stages)
      groups.push({ key, repo, issueNumber, issueTitle, issueState, stages, lastActivity, allRuns: runs, overallStatus })
    }
    groups.sort((a, b) => b.lastActivity.localeCompare(a.lastActivity))
    return groups
  }, [filteredHistory])

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
    // Pending.json is captured at scan start — once a run for the same
    // (repo, issue, operator) exists in runs.json, the pending row is a
    // stale duplicate. Cover ALL statuses (including failed/cancelled) so
    // a kill or crash doesn't cause the row to bounce back into Pending
    // until the next clawflow run rewrites pending.json fresh.
    const s = new Set<string>()
    for (const r of runs) {
      s.add(`${r.repo}#${r.issue_number}/${r.operator}`)
    }
    return s
  }, [runs])

  // Map repo full_name → bound_machine for the bound-machine filter below.
  // Repos with no bound_machine are processed by every machine (the common
  // case) so we treat missing as "mine".
  const repoBoundMap = useMemo(() => {
    const m = new Map<string, string>()
    for (const r of repos) {
      if (r.bound_machine) m.set(r.full_name, r.bound_machine)
    }
    return m
  }, [repos])

  const filteredPending = useMemo(() => {
    const q = query.trim().toLowerCase()
    return pending.filter(p => {
      if (p.issue_state === 'closed') return false
      if (activeRunKeys.has(`${p.repo}#${p.issue_number}/${p.operator}`)) return false
      // Hide pending entries for repos pinned to a different machine —
      // this machine will never run them, so showing "queued" is misleading.
      // Empty bound_machine = any machine = always show. If we don't know
      // hostname yet (first paint), don't filter so we don't briefly hide
      // legitimate rows.
      const bound = repoBoundMap.get(p.repo)
      if (bound && hostname && bound !== hostname) return false
      if (repoFilter !== 'all' && p.repo !== repoFilter) return false
      if (q && !(p.issue_title || '').toLowerCase().includes(q) && !String(p.issue_number).includes(q) && !p.operator.toLowerCase().includes(q)) return false
      return true
    })
  }, [pending, repoFilter, query, activeRunKeys, repoBoundMap, hostname])

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


      {/* View mode toggle + stat cards */}
      <div className="flex items-center justify-between mb-2 flex-wrap gap-2">
        <span className="text-xs text-muted-foreground">
          {viewMode === 'issue' ? 'Counts by issue' : 'Counts by run'}
        </span>
        <div className="inline-flex rounded-lg border border-border overflow-hidden text-xs font-medium">
          <button
            onClick={() => setViewMode('issue')}
            className={cn(
              'px-3 py-1.5 transition-colors',
              viewMode === 'issue' ? 'bg-primary text-primary-foreground' : 'bg-card text-muted-foreground hover:bg-secondary',
            )}
          >
            By Issue
          </button>
          <button
            onClick={() => setViewMode('run')}
            className={cn(
              'px-3 py-1.5 transition-colors border-l border-border',
              viewMode === 'run' ? 'bg-primary text-primary-foreground' : 'bg-card text-muted-foreground hover:bg-secondary',
            )}
          >
            By Run
          </button>
        </div>
      </div>
      {(() => {
        const c = viewMode === 'issue' ? issueCounts : counts
        return (
          <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2 mb-4">
            <StatCard label="Total"     value={c.total}     filter="all"       active={statusFilter === 'all'}       onClick={setStatusFilter} tone="neutral" />
            <StatCard label="Running"   value={c.running}   filter="running"   active={statusFilter === 'running'}   onClick={setStatusFilter} tone="blue" />
            <StatCard label="Success"   value={c.success}   filter="success"   active={statusFilter === 'success'}   onClick={setStatusFilter} tone="green" />
            <StatCard label="Failed"    value={c.failed}    filter="failed"    active={statusFilter === 'failed'}    onClick={setStatusFilter} tone="red" />
            <StatCard label="Cancelled" value={c.cancelled} filter="cancelled" active={statusFilter === 'cancelled'} onClick={setStatusFilter} tone="amber" />
            <StatCard label="Skipped"   value={c.skipped}   filter="skipped"   active={statusFilter === 'skipped'}   onClick={setStatusFilter} tone="muted" />
          </div>
        )
      })()}

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
            {filteredRunning.map(r => (
              <Row
                key={r.path}
                r={r}
                repoMap={repoMap}
                onCancel={cancelRun}
                cancelling={cancellingKey === `${r.repo}#${r.issue_number}`}
              />
            ))}
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
      ) : viewMode === 'issue' ? (
        <div className="bg-card border border-border rounded-xl shadow-sm divide-y divide-border overflow-hidden">
          {filteredIssueGroups.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-6">No issues match the current filters.</p>
          ) : (
            <>
              {filteredIssueGroups.slice(0, visibleCount).map(group => (
                <IssueGroupRow
                  key={group.key}
                  group={group}
                  repoMap={repoMap}
                  expanded={expandedIssues.has(group.key)}
                  onToggleExpand={() => toggleIssueExpand(group.key)}
                  onCancel={cancelRun}
                  cancellingKey={cancellingKey}
                />
              ))}
              {visibleCount < filteredIssueGroups.length && (
                <button
                  onClick={() => setVisibleCount(c => c + 20)}
                  className="w-full py-3 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors"
                >
                  Load more ({filteredIssueGroups.length - visibleCount} remaining)
                </button>
              )}
            </>
          )}
        </div>
      ) : (
        <div className="bg-card border border-border rounded-xl shadow-sm divide-y divide-border overflow-hidden">
          {filteredHistory.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-6">No runs match the current filters.</p>
          ) : (
            <>
              {filteredHistory.slice(0, visibleCount).map(r => (
                <Row
                  key={r.path}
                  r={r}
                  repoMap={repoMap}
                  onCancel={r.status === 'running' ? cancelRun : undefined}
                  cancelling={r.status === 'running' && cancellingKey === `${r.repo}#${r.issue_number}`}
                />
              ))}
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
      <span className="text-sm text-foreground truncate flex-1" title={p.issue_title || '(no title)'}>
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
  tone: 'neutral' | 'blue' | 'green' | 'red' | 'muted' | 'amber'
}) {
  const toneCls = {
    neutral: 'text-foreground',
    blue: 'text-blue-600',
    green: 'text-green-600',
    red: 'text-red-600',
    muted: 'text-muted-foreground',
    amber: 'text-amber-600',
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

/** A single operator badge in the issue-aggregated view. Shows the operator
 *  name and its latest run status. Clicking navigates to the run detail. */
function StageBadge({ operator, run }: { operator: string; run: Run | undefined }) {
  const dur = run ? durationStr(run.started_at, run.ended_at) : null
  if (!run) {
    return (
      <span
        title={`${operator} — not run`}
        className="inline-flex items-center gap-1 border border-dashed px-1.5 py-0.5 rounded text-[11px] font-mono border-muted-foreground/30 text-muted-foreground/40"
      >
        {operator}
      </span>
    )
  }
  const { cls, Icon } = statusPill[run.status]
  const spinning = run.status === 'running'
  return (
    <a
      href={`/runs/${repoSlug(run.repo)}/issue-${run.issue_number}/${runIdFromPath(run.path)}`}
      target="_blank"
      rel="noopener noreferrer"
      title={`${operator} · ${run.status}${dur ? ` · ${dur}` : ''}`}
      className={cn(
        'inline-flex items-center gap-1 border px-1.5 py-0.5 rounded text-[11px] font-mono hover:opacity-80 transition-opacity',
        cls,
      )}
      onClick={e => e.stopPropagation()}
    >
      <Icon className={cn('w-3 h-3', spinning && 'animate-spin')} />
      {operator}
      {dur && <span className="opacity-70 ml-0.5">{dur}</span>}
    </a>
  )
}

/** Aggregated row for one issue — shows all pipeline-stage badges inline and
 *  can be expanded to reveal the underlying flat run rows (drill-down). */
function IssueGroupRow({
  group,
  repoMap,
  expanded,
  onToggleExpand,
  onCancel,
  cancellingKey,
}: {
  group: IssueGroup
  repoMap: RepoInfoMap
  expanded: boolean
  onToggleExpand: () => void
  onCancel: (repo: string, issue: number) => void
  cancellingKey: string | null
}) {
  const isClosed = group.issueState === 'closed'

  // Display stages: known operators in fixed order first, then any unknown ones
  const allOperators = Array.from(group.stages.keys())
  const orderedStages = [
    ...KNOWN_STAGES.filter(s => group.stages.has(s)),
    ...allOperators.filter(s => !KNOWN_STAGES.includes(s)),
  ]

  return (
    <div>
      <div
        className={cn(
          'flex items-center gap-3 px-4 py-2.5 hover:bg-secondary/50 transition-colors cursor-pointer group',
          isClosed && 'opacity-60',
        )}
        onClick={onToggleExpand}
      >
        <StatusChip status={group.overallStatus} />
        <VcsIcon repo={group.repo} map={repoMap} className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
        <a
          href={issueUrl(group.repo, group.issueNumber, repoMap)}
          target="_blank"
          rel="noopener noreferrer"
          className={cn(
            'font-mono text-xs hover:text-foreground hover:underline shrink-0',
            isClosed ? 'text-muted-foreground/50 line-through' : 'text-muted-foreground',
          )}
          onClick={e => e.stopPropagation()}
        >
          #{group.issueNumber}
        </a>
        <span
          className={cn('text-sm text-foreground flex-1 truncate min-w-0', isClosed && 'line-through')}
          title={group.issueTitle}
        >
          {group.issueTitle}
        </span>
        {/* Stage badges — hidden on very narrow screens to avoid overflow */}
        <div className="hidden sm:flex items-center gap-1 flex-wrap shrink-0" onClick={e => e.stopPropagation()}>
          {orderedStages.map(op => (
            <StageBadge key={op} operator={op} run={group.stages.get(op)} />
          ))}
        </div>
        <span className="text-xs text-muted-foreground shrink-0 w-16 text-right tabular-nums">
          {timeAgo(group.lastActivity)}
        </span>
        <Link
          to="/repos/$repoName"
          params={{ repoName: encodeURIComponent(group.repo) }}
          className="text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0 hidden lg:inline"
          onClick={e => e.stopPropagation()}
        >
          {group.repo}
        </Link>
        <ChevronRight
          className={cn(
            'w-4 h-4 text-muted-foreground/40 group-hover:text-muted-foreground shrink-0 transition-transform duration-150',
            expanded && 'rotate-90',
          )}
        />
      </div>
      {expanded && (
        <div className="border-t border-border divide-y divide-border bg-secondary/20 pl-4">
          {group.allRuns.map(r => (
            <Row
              key={r.path}
              r={r}
              repoMap={repoMap}
              onCancel={r.status === 'running' ? onCancel : undefined}
              cancelling={r.status === 'running' && cancellingKey === `${r.repo}#${r.issue_number}`}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function Row({
  r,
  repoMap,
  onCancel,
  cancelling,
}: {
  r: Run
  repoMap: RepoInfoMap
  onCancel?: (repo: string, issue: number) => void
  cancelling?: boolean
}) {
  const dur = durationStr(r.started_at, r.ended_at)
  const runHref = `/runs/${repoSlug(r.repo)}/issue-${r.issue_number}/${runIdFromPath(r.path)}`
  return (
    // Outer div instead of <a> to avoid nesting interactive elements
    // (<button> and <a>) inside an anchor, which is invalid HTML5 and
    // causes browsers to misfire the outer navigation on cancel clicks.
    <div className="flex items-center gap-3 px-4 py-2.5 hover:bg-secondary/50 transition-colors group">
      {/* Navigable region — clicking anywhere in this flex-1 area opens the run detail */}
      <a
        href={runHref}
        target="_blank"
        rel="noopener noreferrer"
        className="flex items-center gap-3 flex-1 min-w-0"
      >
        <StatusChip status={r.status} />
        <VcsIcon repo={r.repo} map={repoMap} className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
        <span className="text-sm text-foreground truncate flex-1" title={`${r.operator} · ${r.issue_title || '(no title)'}`}>
          {r.operator} · {r.issue_title || '(no title)'}
        </span>
        {dur && <span className="text-xs text-muted-foreground shrink-0 tabular-nums w-14 text-right">{dur}</span>}
        <span className="text-xs text-muted-foreground shrink-0 w-16 text-right">{timeAgo(r.started_at)}</span>
        <ChevronRight className="w-4 h-4 text-muted-foreground/40 group-hover:text-muted-foreground shrink-0" />
      </a>
      {/* Standalone interactive elements — siblings of the anchor, not descendants */}
      <a
        href={issueUrl(r.repo, r.issue_number, repoMap)}
        target="_blank"
        rel="noopener noreferrer"
        className={`font-mono text-xs hover:text-foreground hover:underline shrink-0 ${r.issue_state === 'closed' ? 'text-muted-foreground/50 line-through' : 'text-muted-foreground'}`}
      >
        #{r.issue_number}
      </a>
      <Link
        to="/repos/$repoName"
        params={{ repoName: encodeURIComponent(r.repo) }}
        className="text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0 hidden sm:inline"
      >
        {r.repo}
      </Link>
      {r.pr_url && (
        <a
          href={r.pr_url}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-0.5 text-xs text-foreground hover:underline shrink-0"
        >
          PR <ExternalLink className="w-3 h-3" />
        </a>
      )}
      {onCancel && (
        <button
          type="button"
          // No confirm() — the button is small but explicit ("cancel"
          // label + red border) and the misclick risk doesn't justify
          // a modal interrupt. The row reverts to "failed" on the next
          // poll, which is recoverable enough to forgive a stray click.
          onClick={() => {
            if (cancelling) return
            onCancel(r.repo, r.issue_number)
          }}
          disabled={cancelling}
          title={`Kill ${r.operator} on ${r.repo} #${r.issue_number}`}
          className={cn(
            'inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium border shrink-0 transition-colors',
            cancelling
              ? 'bg-muted text-muted-foreground border-border cursor-not-allowed'
              : 'bg-card text-red-700 border-red-200 hover:bg-red-50',
          )}
        >
          {cancelling ? (
            <><Loader2 className="w-3 h-3 animate-spin" /> killing…</>
          ) : (
            <><Square className="w-3 h-3" /> cancel</>
          )}
        </button>
      )}
    </div>
  )
}
