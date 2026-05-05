import { createFileRoute, Link } from '@tanstack/react-router'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronLeft, ChevronRight, ExternalLink, MessageSquare, Download, Loader2, RotateCw } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, issueUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { useChatDrawer } from '../lib/chatContext'

interface Repo {
  full_name: string
  platform?: Platform
  base_url?: string
  base_branch: string
  local_path?: string
  enabled: boolean
  auto_approve: boolean
  auto_merge: boolean
}

interface Run {
  operator: string
  repo: string
  issue_number: number
  issue_title?: string
  started_at: string
  ended_at?: string
  status: 'success' | 'failed' | 'skipped' | 'running'
  summary?: string
  path: string
  pr_url?: string
  error?: string
}

interface PendingEntry {
  repo: string
  issue_number: number
  issue_title?: string
  operator: string
  labels?: string[]
  captured_at: string
}

interface IssueEntry {
  repo: string
  issue_number: number
  issue_title?: string
  labels?: string[]
  state: string // "open" | "closed"
  captured_at: string
}

interface IssueGroup {
  issue_number: number
  issue_title?: string
  runs: Run[]
  pending: PendingEntry[]
  labels?: string[]
  state?: string // "open" | "closed"
}
export const Route = createFileRoute('/_app/repos/$repoName')({
  component: RepoDetail,
})

function RepoDetail() {
  const { repoName } = Route.useParams()
  const fullName = decodeURIComponent(repoName)
  const chatDrawer = useChatDrawer()

  const [repo, setRepo] = useState<Repo | null>(null)
  const [runs, setRuns] = useState<Run[]>([])
  const [pending, setPending] = useState<PendingEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const [saving, setSaving] = useState(false)
  const [cloning, setCloning] = useState(false)
  const [cloneError, setCloneError] = useState<string | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [allIssues, setAllIssues] = useState<IssueEntry[]>([])

  const cloneNow = useCallback(() => {
    if (!repo || cloning) return
    setCloning(true)
    setCloneError(null)
    fetch('/api/repo/clone', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo: fullName }),
    })
      .then(async r => {
        const data = await r.json().catch(() => null)
        if (!r.ok || !data) {
          throw new Error((data && data.error) || `HTTP ${r.status}`)
        }
        return data
      })
      .then(d => {
        if (d.status === 'ok' && d.local_path) {
          setRepo(prev => prev ? { ...prev, local_path: d.local_path } : prev)
        }
      })
      .catch(err => setCloneError(String(err.message || err)))
      .finally(() => setCloning(false))
  }, [repo, fullName, cloning])

  const refreshData = useCallback(() => {
    return Promise.all([
      fetch('/data/repos.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/data/runs.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/data/pending.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/data/issues.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
    ]).then(([repos, allRuns, allPending, allIssuesData]) => {
      const match = (Array.isArray(repos) ? repos : []).find((x: Repo) => x.full_name === fullName) || null
      setRepo(match)
      setRuns((Array.isArray(allRuns) ? allRuns : []).filter((r: Run) => r.repo === fullName))
      setPending((Array.isArray(allPending) ? allPending : []).filter((p: PendingEntry) => p.repo === fullName))
      setAllIssues((Array.isArray(allIssuesData) ? allIssuesData : []).filter((i: IssueEntry) => i.repo === fullName))
    })
  }, [fullName])

  const syncNow = useCallback(() => {
    if (syncing) return
    setSyncing(true)

    // POST /api/run spawns `clawflow run` as a background subprocess and
    // returns immediately. issues.json is only rewritten when that run
    // finishes, so we must poll /api/run/status until it flips back to
    // "idle" before refreshing — otherwise we re-read a stale file and
    // the user sees no change. Both 200 ("started") and 409 ("busy", a
    // run is already in flight) join the same polling loop.
    const pollIntervalMs = 2000
    const safetyTimeoutMs = 120_000
    let pollId: ReturnType<typeof setInterval> | null = null
    let timeoutId: ReturnType<typeof setTimeout> | null = null

    const finish = () => {
      if (pollId !== null) clearInterval(pollId)
      if (timeoutId !== null) clearTimeout(timeoutId)
      refreshData().finally(() => setSyncing(false))
    }

    const startPolling = () => {
      pollId = setInterval(() => {
        fetch('/api/run/status', { cache: 'no-store' })
          .then(r => (r.ok ? r.json() : null))
          .then(d => {
            if (d && d.status !== 'running') finish()
          })
          .catch(() => {})
      }, pollIntervalMs)
      timeoutId = setTimeout(finish, safetyTimeoutMs)
    }

    fetch('/api/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo: fullName }),
    })
      .then(() => startPolling())
      .catch(() => finish())
  }, [fullName, syncing, refreshData])


  const toggleConfig = useCallback((field: 'enabled' | 'auto_approve' | 'auto_merge') => {
    if (!repo || saving) return
    const newVal = !(repo as any)[field]
    setSaving(true)
    fetch('/api/repo/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo: fullName, [field]: newVal }),
    })
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d && d.status === 'ok') {
          setRepo(prev => prev ? { ...prev, [field]: newVal } : prev)
        }
      })
      .catch(() => {})
      .finally(() => setSaving(false))
  }, [repo, fullName, saving])

  useEffect(() => {
    refreshData().then(() => setLoading(false))
  }, [fullName, refreshData])

  const repoMap = useMemo<RepoInfoMap>(() => {
    if (!repo) return {}
    const platform: Platform = repo.platform || 'github'
    const defaultHost = platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com'
    return {
      [repo.full_name]: {
        platform,
        host: (repo.base_url || defaultHost).replace(/\/$/, ''),
      },
    }
  }, [repo])

  const repoVcsUrl = repo ? repoUrl(repo.full_name, repoMap) : null

  const slug = fullName.replace(/\//g, '__')

  const issues = useMemo<IssueGroup[]>(() => {
    const map = new Map<number, IssueGroup>()
    
    // Start with all issues from issues.json
    for (const issue of allIssues) {
      let g = map.get(issue.issue_number)
      if (!g) {
        g = { 
          issue_number: issue.issue_number, 
          issue_title: issue.issue_title, 
          runs: [], 
          pending: [], 
          labels: [...(issue.labels || [])],
          state: issue.state
        }
        map.set(issue.issue_number, g)
      }
      // Merge labels from issue data
      if (issue.labels) {
        for (const label of issue.labels) {
          if (!g.labels?.includes(label)) {
            g.labels?.push(label)
          }
        }
      }
    }
    
    // Add runs data
    for (const r of runs) {
      let g = map.get(r.issue_number)
      if (!g) {
        g = { issue_number: r.issue_number, issue_title: r.issue_title, runs: [], pending: [], labels: [], state: 'open' }
        map.set(r.issue_number, g)
      }
      g.runs.push(r)
      if (!g.issue_title && r.issue_title) g.issue_title = r.issue_title
    }
    
    // Add pending data
    for (const p of pending) {
      let g = map.get(p.issue_number)
      if (!g) {
        g = { issue_number: p.issue_number, issue_title: p.issue_title, runs: [], pending: [], labels: [], state: 'open' }
        map.set(p.issue_number, g)
      }
      g.pending.push(p)
      if (!g.issue_title && p.issue_title) g.issue_title = p.issue_title
      // Collect labels from pending entries
      if (p.labels && g.labels) {
        for (const label of p.labels) {
          if (!g.labels.includes(label)) {
            g.labels.push(label)
          }
        }
      }
    }
    
    for (const g of map.values()) {
      g.runs.sort((a, b) => b.started_at.localeCompare(a.started_at))
      g.pending.sort((a, b) => a.operator.localeCompare(b.operator))
      if (g.labels) g.labels.sort()
    }
    return Array.from(map.values())
  }, [runs, pending, allIssues])

  const sections = useMemo(() => {
    const running: IssueGroup[] = []
    const pendingI: IssueGroup[] = []
    const done: IssueGroup[] = []
    const closed: IssueGroup[] = []
    
    for (const g of issues) {
      if (g.state === 'closed') {
        closed.push(g)
      } else {
        const latest = g.runs[0]
        if (latest?.status === 'running') running.push(g)
        else if (g.pending.length > 0) pendingI.push(g)
        else done.push(g)
      }
    }
    
    const sortKey = (g: IssueGroup) => g.runs[0]?.started_at || g.pending[0]?.captured_at || ''
    const cmp = (a: IssueGroup, b: IssueGroup) => sortKey(b).localeCompare(sortKey(a))
    running.sort(cmp)
    pendingI.sort(cmp)
    done.sort(cmp)
    closed.sort(cmp)
    return { running, pending: pendingI, done, closed }
  }, [issues])

  function toggle(n: number) {
    setExpanded(prev => {
      const next = new Set(prev)
      if (next.has(n)) next.delete(n)
      else next.add(n)
      return next
    })
  }

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <Link to="/repos" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground mb-4">
        <ChevronLeft className="w-3.5 h-3.5" /> Repos
      </Link>

      {loading ? (
        <p className="text-sm text-muted-foreground py-8">Loading…</p>
      ) : !repo ? (
        <div className="bg-card border border-border rounded-xl p-6 text-sm text-muted-foreground">
          Repo <code className="font-mono text-foreground">{fullName}</code> is not in your config. Run{' '}
          <code className="px-1 py-0.5 bg-secondary rounded font-mono">clawflow repo add {fullName}</code>.
        </div>
      ) : (
        <>
          <div className="mb-6">
            <h1 className="text-2xl font-bold text-foreground font-mono">{repo.full_name}</h1>
            <div className="flex items-center gap-2 mt-1 text-xs text-muted-foreground">
              <span className="inline-flex items-center gap-1">
                <VcsIcon repo={repo.full_name} map={repoMap} className="w-3.5 h-3.5 shrink-0" />
                {repo.platform || 'github'}
              </span>
              <span>·</span>
              <span className="font-mono">base: {repo.base_branch}</span>
              {repoVcsUrl && (
                <>
                  <span>·</span>
                  <a href={repoVcsUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline">
                    view <ExternalLink className="w-3 h-3" />
                  </a>
                </>
              )}
              <span>·</span>
              <button
                onClick={() => chatDrawer.open({ repo: fullName })}
                className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
              >
                <MessageSquare className="w-3 h-3" /> chat
              </button>
            </div>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 mb-6">
            <ToggleCard label="Status" enabled={repo.enabled} onToggle={() => toggleConfig('enabled')} disabled={saving} />
            <ToggleCard label="Auto-approve" enabled={repo.auto_approve} onToggle={() => toggleConfig('auto_approve')} disabled={saving} />
            <ToggleCard label="Auto-merge" enabled={repo.auto_merge} onToggle={() => toggleConfig('auto_merge')} disabled={saving} />
            <div className="bg-card border border-border rounded-xl p-3 min-w-0">
              <div className="text-xs text-muted-foreground">Local path</div>
              {repo.local_path ? (
                <div className="text-xs font-mono mt-1.5 text-foreground truncate" title={repo.local_path}>
                  {repo.local_path}
                </div>
              ) : (
                <button
                  type="button"
                  onClick={cloneNow}
                  disabled={cloning}
                  title={cloneError ?? 'git clone into the default location and save the path'}
                  className={cn(
                    'mt-1 inline-flex items-center gap-1 text-xs font-medium px-2 py-1 rounded border transition-colors',
                    cloning
                      ? 'border-border text-muted-foreground bg-secondary/30'
                      : cloneError
                        ? 'border-red-200 text-red-700 bg-red-50 hover:bg-red-100'
                        : 'border-border text-foreground hover:bg-secondary/50',
                  )}
                >
                  {cloning ? (
                    <>
                      <Loader2 className="w-3 h-3 animate-spin" /> cloning…
                    </>
                  ) : cloneError ? (
                    <>retry clone</>
                  ) : (
                    <>
                      <Download className="w-3 h-3" /> Clone now
                    </>
                  )}
                </button>
              )}
              {cloneError && !cloning && (
                <div className="mt-1 text-[11px] text-red-600 truncate" title={cloneError}>
                  {cloneError}
                </div>
              )}
            </div>
          </div>

          <section>
            <div className="flex items-baseline gap-2 mb-2">
              <h2 className="text-sm font-semibold text-foreground">
                Issues <span className="font-normal text-muted-foreground">({issues.length})</span>
              </h2>
              <span className="text-xs text-muted-foreground">
                {sections.running.length} running · {sections.pending.length} pending · {sections.done.length} done · {sections.closed.length} closed
              </span>
              <button
                onClick={syncNow}
                disabled={syncing}
                title="Sync all issues including pending"
                className={cn(
                  'ml-auto inline-flex items-center gap-1 text-xs font-medium px-2 py-1 rounded border transition-colors',
                  syncing
                    ? 'border-border text-muted-foreground bg-secondary/30'
                    : 'border-border text-foreground hover:bg-secondary/50',
                )}
              >
                {syncing ? (
                  <>
                    <Loader2 className="w-3 h-3 animate-spin" /> syncing…
                  </>
                ) : (
                  <>
                    <RotateCw className="w-3 h-3" /> Sync
                  </>
                )}
              </button>
            </div>

            {issues.length === 0 ? (
              <p className="text-sm text-muted-foreground py-4">
                No activity yet for this repo. Run <code className="px-1 py-0.5 bg-secondary rounded font-mono">clawflow run --repo {repo.full_name}</code>.
              </p>
            ) : (
              <div className="space-y-4">
                <IssueSection title="Running" tone="blue" groups={sections.running} repo={fullName} repoMap={repoMap} slug={slug} expanded={expanded} onToggle={toggle} />
                <IssueSection title="Pending" tone="amber" groups={sections.pending} repo={fullName} repoMap={repoMap} slug={slug} expanded={expanded} onToggle={toggle} />
                <IssueSection title="Done" tone="muted" groups={sections.done} repo={fullName} repoMap={repoMap} slug={slug} expanded={expanded} onToggle={toggle} />
                <IssueSection title="Closed" tone="gray" groups={sections.closed} repo={fullName} repoMap={repoMap} slug={slug} expanded={expanded} onToggle={toggle} />
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function IssueSection({
  title, tone, groups, repo, repoMap, slug, expanded, onToggle,
}: {
  title: string
  tone: 'blue' | 'amber' | 'muted' | 'gray'
  groups: IssueGroup[]
  repo: string
  repoMap: RepoInfoMap
  slug: string
  expanded: Set<number>
  onToggle: (n: number) => void
}) {
  if (groups.length === 0) return null
  const dotCls = {
    blue: 'bg-blue-400',
    amber: 'bg-amber-400',
    muted: 'bg-muted-foreground/40',
    gray: 'bg-gray-400',
  }[tone]
  return (
    <div>
      <div className="flex items-center gap-2 mb-1.5 px-1">
        <span className={cn('w-1.5 h-1.5 rounded-full', dotCls)} />
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {title} <span className="font-normal">({groups.length})</span>
        </h3>
      </div>
      <div className="bg-card border border-border rounded-xl overflow-hidden divide-y divide-border">
        {groups.map(g => (
          <IssueRow
            key={g.issue_number}
            group={g}
            repo={repo}
            repoMap={repoMap}
            slug={slug}
            expanded={expanded.has(g.issue_number)}
            onToggle={onToggle}
          />
        ))}
        {title === 'Closed' && groups.length > 0 && (
          <div className="px-4 py-3 bg-secondary/20 border-t border-border">
            <a
              href={`${repoUrl(repo, repoMap)}/issues?q=is%3Aissue+is%3Aclosed`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
            >
              View more closed issues on GitHub <ExternalLink className="w-3 h-3" />
            </a>
          </div>
        )}
      </div>
    </div>
  )
}

function IssueRow({
  group, repo, repoMap, slug, expanded, onToggle,
}: {
  group: IssueGroup
  repo: string
  repoMap: RepoInfoMap
  slug: string
  expanded: boolean
  onToggle: (n: number) => void
}) {
  const chatDrawer = useChatDrawer()
  const latest = group.runs[0]
  return (
    <div>
      <button
        type="button"
        onClick={() => onToggle(group.issue_number)}
        className="w-full flex items-center gap-3 px-4 py-2 hover:bg-secondary/30 text-left flex-wrap"
      >
        <ChevronRight className={cn('w-3.5 h-3.5 text-muted-foreground transition-transform shrink-0', expanded && 'rotate-90')} />
        <a
          href={issueUrl(repo, group.issue_number, repoMap)}
          target="_blank"
          rel="noopener noreferrer"
          onClick={e => e.stopPropagation()}
          className="font-mono text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0 w-12"
        >
          #{group.issue_number}
        </a>
        {latest && <StatusBadge status={latest.status} />}
        <span className={cn("text-sm truncate flex-1", group.state === 'closed' && "line-through text-muted-foreground")}>
          {group.issue_title || '(no title)'}
        </span>
        {group.labels && group.labels.length > 0 && (
          <div className="flex flex-wrap gap-1 shrink-0">
            {group.labels.map(label => (
              <span
                key={label}
                className="inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium bg-secondary text-secondary-foreground border border-border"
              >
                {label}
              </span>
            ))}
          </div>
        )}
        {group.pending.length > 0 && (
          <span className="text-[11px] font-medium text-amber-700 bg-amber-50 border border-amber-200 px-1.5 py-0.5 rounded shrink-0">
            queued: {group.pending.map(p => p.operator).join(', ')}
          </span>
        )}
        <span className="text-xs text-muted-foreground shrink-0 tabular-nums w-16 text-right">
          {group.runs.length} {group.runs.length === 1 ? 'run' : 'runs'}
        </span>
        <button
          onClick={e => { e.stopPropagation(); chatDrawer.open({ repo, issue: group.issue_number }) }}
          className="shrink-0 text-muted-foreground hover:text-foreground"
          title="Chat about this issue"
        >
          <MessageSquare className="w-3.5 h-3.5" />
        </button>
      </button>
      {expanded && <Timeline group={group} slug={slug} />}
    </div>
  )
}

function Timeline({ group, slug }: { group: IssueGroup; slug: string }) {
  return (
    <div className="bg-secondary/20 border-t border-border px-6 py-4">
      <ol className="relative border-l border-border space-y-3 ml-1">
        {group.pending.map(p => (
          <li key={'pending-' + p.operator} className="pl-4 relative">
            <span className="absolute -left-[7px] top-1.5 w-3 h-3 rounded-full bg-amber-200 border-2 border-amber-400" />
            <div className="text-sm">
              <span className="font-mono">{p.operator}</span>
              <span className="ml-2 text-[11px] font-medium text-amber-700 bg-amber-50 border border-amber-200 px-1.5 py-0.5 rounded">
                queued
              </span>
            </div>
            {p.labels && p.labels.length > 0 && (
              <div className="flex flex-wrap gap-1 mt-1">
                {p.labels.map(label => (
                  <span
                    key={label}
                    className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-secondary text-secondary-foreground border border-border"
                  >
                    {label}
                  </span>
                ))}
              </div>
            )}
            <div className="text-xs text-muted-foreground tabular-nums mt-0.5">
              captured {new Date(p.captured_at).toLocaleString()}
            </div>
          </li>
        ))}
        {group.runs.map(r => {
          const ts = r.path.replace(/\/$/, '').split('/').pop() || ''
          const runHref = `/runs/${slug}/issue-${group.issue_number}/${ts}`
          const dotCls =
            r.status === 'success' ? 'bg-green-400 border-green-600' :
            r.status === 'failed' ? 'bg-red-400 border-red-600' :
            r.status === 'running' ? 'bg-blue-400 border-blue-600 animate-pulse' :
            'bg-muted-foreground/30 border-muted-foreground/60'
          return (
            <li key={r.path} className="pl-4 relative">
              <span className={cn('absolute -left-[7px] top-1.5 w-3 h-3 rounded-full border-2', dotCls)} />
              <a
                href={runHref}
                target="_blank"
                rel="noopener noreferrer"
                className="block -mx-2 px-2 py-1 rounded hover:bg-background"
              >
                <div className="flex items-center gap-2 text-sm">
                  <span className="font-mono">{r.operator}</span>
                  <StatusBadge status={r.status} />
                  {r.pr_url && (
                    <a
                      href={r.pr_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      onClick={e => e.stopPropagation()}
                      className="inline-flex items-center gap-0.5 text-xs text-muted-foreground hover:text-foreground hover:underline"
                    >
                      PR <ExternalLink className="w-3 h-3" />
                    </a>
                  )}
                </div>
                <div className="text-xs text-muted-foreground tabular-nums mt-0.5">
                  {new Date(r.started_at).toLocaleString()}
                  {r.ended_at && ' · ' + formatDuration(r.started_at, r.ended_at)}
                </div>
              </a>
            </li>
          )
        })}
      </ol>
    </div>
  )
}

function StatusBadge({ status }: { status: Run['status'] }) {
  return (
    <span className={cn(
      'inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-semibold border shrink-0',
      status === 'success' && 'bg-green-100 text-green-700 border-green-200',
      status === 'failed' && 'bg-red-100 text-red-700 border-red-200',
      status === 'skipped' && 'bg-muted text-muted-foreground border-border',
      status === 'running' && 'bg-blue-100 text-blue-700 border-blue-200',
    )}>{status}</span>
  )
}

function formatDuration(start: string, end: string): string {
  const ms = new Date(end).getTime() - new Date(start).getTime()
  if (!isFinite(ms) || ms < 0) return ''
  if (ms < 1000) return ms + 'ms'
  const s = Math.floor(ms / 1000)
  if (s < 60) return s + 's'
  const m = Math.floor(s / 60)
  if (m < 60) return m + 'm ' + (s % 60) + 's'
  const h = Math.floor(m / 60)
  return h + 'h ' + (m % 60) + 'm'
}

function ToggleCard({ label, enabled, onToggle, disabled }: {
  label: string
  enabled: boolean
  onToggle: () => void
  disabled?: boolean
}) {
  return (
    <button
      onClick={onToggle}
      disabled={disabled}
      className={cn(
        'bg-card border rounded-xl p-3 text-left transition-all',
        enabled ? 'border-green-300' : 'border-border',
        disabled ? 'opacity-60 cursor-not-allowed' : 'hover:shadow-sm cursor-pointer',
      )}
    >
      <div className="flex items-center justify-between">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div
          className={cn(
            'w-8 h-[18px] rounded-full transition-colors relative',
            enabled ? 'bg-green-500' : 'bg-muted-foreground/30',
          )}
        >
          <div
            className={cn(
              'absolute top-[2px] w-[14px] h-[14px] rounded-full bg-white transition-transform shadow-sm',
              enabled ? 'translate-x-[16px]' : 'translate-x-[2px]',
            )}
          />
        </div>
      </div>
      <div className={cn('text-base font-semibold mt-0.5', enabled ? 'text-green-600' : 'text-muted-foreground')}>
        {enabled ? (label === 'Status' ? 'enabled' : 'on') : (label === 'Status' ? 'disabled' : 'off')}
      </div>
    </button>
  )
}
