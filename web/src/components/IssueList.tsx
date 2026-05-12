import { useEffect, useMemo, useRef, useState } from 'react'
import { ChevronRight, ExternalLink, MessageSquare, Code } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { cn } from '../lib/utils'
import { issueUrl, repoUrl, type RepoInfoMap } from '../lib/vcsUrls'
import { useChatDrawer } from '../lib/chatContext'

// IssueList renders the merged "issues + runs + pending" view shared
// between the per-repo page and the project-level backlog. The per-repo
// page hides the repo column and uses a 4-section layout (Running /
// Pending / Done / Closed); the project page shows the repo column
// and uses a 5-section layout that adds an "Awaiting evaluation"
// bucket so users can spot the bottleneck.
//
// Inputs are the raw arrays read from /data/issues.json /data/runs.json
// /data/pending.json — callers filter them however they need (single
// repo for the repo page, project.repos.includes(repo) for the project
// page) before passing in. useIssueGroups merges and sorts; the visual
// component only renders.

export interface Run {
  operator: string
  repo: string
  issue_number: number
  issue_title?: string
  started_at: string
  ended_at?: string
  status: 'success' | 'failed' | 'skipped' | 'running' | 'cancelled' | 'no-marker' | 'skipped-empty'
  summary?: string
  path: string
  pr_url?: string
  error?: string
  // Set by the snapshot index for status="running" rows when the
  // (repo, issue) lockfile is held by a live PID. Lets the dashboard
  // distinguish "actively running, just quietly retrying upstream"
  // from a frozen row pending reconcile.
  runner_alive?: boolean
}

export interface PendingEntry {
  repo: string
  issue_number: number
  issue_title?: string
  operator: string
  labels?: string[]
  captured_at: string
}

export interface IssueEntry {
  repo: string
  issue_number: number
  issue_title?: string
  labels?: string[]
  state: string // "open" | "closed"
  captured_at: string
}

// IssueGroup is the merged per-issue view consumed by the renderer.
// `repo` is carried so the project-level list can render a per-row
// repo prefix; the repo page just ignores it.
export interface IssueGroup {
  repo: string
  issue_number: number
  issue_title?: string
  runs: Run[]
  pending: PendingEntry[]
  labels?: string[]
  state?: string // "open" | "closed"
}

// SectionConfig parameterizes how groups bucket into headed sections.
// `filter` is a pure predicate: each group falls into the FIRST
// matching section (mutually exclusive — same as the repo page's
// implicit if/else chain). `limit` caps visible rows; bucketing isn't
// affected (counts in the header still show the true total).
export interface SectionConfig {
  title: string
  tone: 'blue' | 'amber' | 'muted' | 'gray' | 'red'
  filter: (g: IssueGroup) => boolean
  limit?: number
  emptyHidden?: boolean
}

// useIssueGroups merges (issues, runs, pending) into per-issue groups
// keyed by `repo:issue_number` so cross-repo collisions don't collapse
// rows. Output is sorted by latest activity (newest first) per group's
// runs/pending; section ordering is the caller's job.
export function useIssueGroups({
  issues,
  runs,
  pending,
}: {
  issues: IssueEntry[]
  runs: Run[]
  pending: PendingEntry[]
}): IssueGroup[] {
  return useMemo(() => {
    const map = new Map<string, IssueGroup>()
    const key = (repo: string, n: number) => `${repo}#${n}`

    for (const iss of issues) {
      const k = key(iss.repo, iss.issue_number)
      if (!map.has(k)) {
        map.set(k, {
          repo: iss.repo,
          issue_number: iss.issue_number,
          issue_title: iss.issue_title,
          runs: [],
          pending: [],
          labels: [...(iss.labels ?? [])],
          state: iss.state,
        })
      }
    }
    for (const r of runs) {
      const k = key(r.repo, r.issue_number)
      let g = map.get(k)
      if (!g) {
        g = {
          repo: r.repo,
          issue_number: r.issue_number,
          issue_title: r.issue_title,
          runs: [],
          pending: [],
          labels: [],
          state: 'open',
        }
        map.set(k, g)
      }
      g.runs.push(r)
      if (!g.issue_title && r.issue_title) g.issue_title = r.issue_title
    }
    for (const p of pending) {
      const k = key(p.repo, p.issue_number)
      let g = map.get(k)
      if (!g) {
        g = {
          repo: p.repo,
          issue_number: p.issue_number,
          issue_title: p.issue_title,
          runs: [],
          pending: [],
          labels: [],
          state: 'open',
        }
        map.set(k, g)
      }
      // Skip if a running run for the same operator already exists — the
      // pending entry is stale from the previous scan cycle.
      if (g.runs.some(r => r.operator === p.operator && r.status === 'running')) continue
      g.pending.push(p)
      if (!g.issue_title && p.issue_title) g.issue_title = p.issue_title
      if (p.labels && g.labels) {
        for (const label of p.labels) {
          if (!g.labels.includes(label)) g.labels.push(label)
        }
      }
    }
    for (const g of map.values()) {
      g.runs.sort((a, b) => b.started_at.localeCompare(a.started_at))
      g.pending.sort((a, b) => a.operator.localeCompare(b.operator))
      if (g.labels) g.labels.sort()
    }
    return Array.from(map.values())
  }, [issues, runs, pending])
}

// bucketize partitions groups across sections in declaration order.
// Same `if/else if/...` semantics the repo page used inline, just
// extracted so callers can swap section configurations.
export function bucketize(
  groups: IssueGroup[],
  sections: SectionConfig[],
): { config: SectionConfig; groups: IssueGroup[] }[] {
  const buckets = sections.map(s => ({ config: s, groups: [] as IssueGroup[] }))
  for (const g of groups) {
    for (let i = 0; i < sections.length; i++) {
      if (sections[i].filter(g)) {
        buckets[i].groups.push(g)
        break
      }
    }
  }
  // Sort each bucket by latest activity desc.
  const sortKey = (g: IssueGroup) => g.runs[0]?.started_at || g.pending[0]?.captured_at || ''
  const cmp = (a: IssueGroup, b: IssueGroup) => sortKey(b).localeCompare(sortKey(a))
  for (const b of buckets) b.groups.sort(cmp)
  return buckets
}

// Pre-baked section sets for the two consumers.
//
// REPO_SECTIONS matches the historical repo-page layout exactly so the
// migration is a no-op visually. PROJECT_SECTIONS adds two front-of-
// list buckets that triagers care about most: in-flight work and the
// "awaiting evaluation" bottleneck.
export const REPO_SECTIONS: SectionConfig[] = [
  { title: 'Running', tone: 'blue', filter: g => g.state !== 'closed' && g.runs[0]?.status === 'running' },
  { title: 'Pending', tone: 'amber', filter: g => g.state !== 'closed' && g.pending.length > 0 },
  { title: 'Done', tone: 'muted', filter: g => g.state !== 'closed' },
  { title: 'Closed', tone: 'gray', filter: g => g.state === 'closed', limit: 10 },
]

export const PROJECT_SECTIONS: SectionConfig[] = [
  {
    title: 'In flight',
    tone: 'blue',
    filter: g => g.state !== 'closed' && g.runs[0]?.status === 'running',
  },
  {
    title: 'Queued',
    tone: 'amber',
    filter: g => g.state !== 'closed' && g.pending.length > 0,
  },
  {
    // Has a trigger label (bug / feature) but no operator has touched
    // it yet — the actual progress bottleneck on most projects. Sits
    // ahead of "Done" so PMs see it first.
    title: 'Awaiting evaluation',
    tone: 'red',
    filter: g => {
      if (g.state === 'closed') return false
      if (g.runs.length > 0) return false
      const labels = g.labels ?? []
      const hasTrigger = labels.includes('bug') || labels.includes('feature')
      const hasAgentLabel = labels.some(l => l.startsWith('agent-'))
      return hasTrigger && !hasAgentLabel
    },
  },
  { title: 'Done', tone: 'muted', filter: g => g.state !== 'closed', limit: 10 },
  { title: 'Closed', tone: 'gray', filter: g => g.state === 'closed', limit: 10 },
]

// IssueList is the visual root. Caller passes pre-computed groups and
// the section configuration; the component handles bucketing, headers,
// and per-row rendering. Expansion state is owned here (one Set keyed
// by `repo:issue_number`) — there's no use case for a parent to share
// it across multiple lists, and lifting it out only adds prop wiring.
export function IssueList({
  groups,
  sections,
  showRepo,
  repoMap,
  slug,
  expanded,
  onToggle,
}: {
  groups: IssueGroup[]
  sections: SectionConfig[]
  showRepo: boolean
  repoMap: RepoInfoMap
  // slug is only used by the repo page (which has a single canonical
  // repo slug for the run-detail link). Project-level rows compute it
  // per row from `g.repo` instead.
  slug?: string
  expanded: Set<string>
  onToggle: (key: string) => void
}) {
  const buckets = useMemo(() => bucketize(groups, sections), [groups, sections])
  return (
    <div className="space-y-4">
      {buckets.map(b => (
        <IssueSection
          key={b.config.title}
          config={b.config}
          groups={b.groups}
          showRepo={showRepo}
          repoMap={repoMap}
          slug={slug}
          expanded={expanded}
          onToggle={onToggle}
        />
      ))}
    </div>
  )
}

// rowKey gives each group a stable expansion-state identifier that's
// safe across repos (issue_number alone collides between repos in a
// project-level list).
export function rowKey(g: IssueGroup): string {
  return `${g.repo}#${g.issue_number}`
}

function IssueSection({
  config,
  groups,
  showRepo,
  repoMap,
  slug,
  expanded,
  onToggle,
}: {
  config: SectionConfig
  groups: IssueGroup[]
  showRepo: boolean
  repoMap: RepoInfoMap
  slug?: string
  expanded: Set<string>
  onToggle: (key: string) => void
}) {
  if (groups.length === 0) {
    if (config.emptyHidden ?? true) return null
  }
  const dotCls = {
    blue: 'bg-blue-400',
    amber: 'bg-amber-400',
    muted: 'bg-muted-foreground/40',
    gray: 'bg-gray-400',
    red: 'bg-red-400',
  }[config.tone]
  const visible = config.limit ? groups.slice(0, config.limit) : groups
  const hidden = groups.length - visible.length
  // Closed gets the "view more on GitHub" footer link to a single repo.
  // Cross-repo lists (project page) skip this — there's no single repo
  // to link to. The href uses the first group's repo just to give the
  // user a starting point on a single-repo view.
  const showGithubFooter =
    config.title === 'Closed' && groups.length > 0 && !showRepo && groups[0]?.repo
  return (
    <div>
      <div className="flex items-center gap-2 mb-1.5 px-1">
        <span className={cn('w-1.5 h-1.5 rounded-full', dotCls)} />
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {config.title} <span className="font-normal">({groups.length})</span>
        </h3>
        {hidden > 0 && (
          <span className="text-[11px] text-muted-foreground">
            · showing {visible.length} most recent
          </span>
        )}
      </div>
      <div className="bg-card border border-border rounded-xl overflow-hidden divide-y divide-border">
        {visible.map(g => (
          <IssueRow
            key={rowKey(g)}
            group={g}
            showRepo={showRepo}
            repoMap={repoMap}
            slug={slug}
            expanded={expanded.has(rowKey(g))}
            onToggle={onToggle}
          />
        ))}
        {showGithubFooter && (
          <div className="px-4 py-3 bg-secondary/20 border-t border-border">
            <a
              href={`${repoUrl(groups[0].repo, repoMap)}/issues?q=is%3Aissue+is%3Aclosed`}
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
  group,
  showRepo,
  repoMap,
  slug,
  expanded,
  onToggle,
}: {
  group: IssueGroup
  showRepo: boolean
  repoMap: RepoInfoMap
  slug?: string
  expanded: boolean
  onToggle: (key: string) => void
}) {
  const chatDrawer = useChatDrawer()
  const latest = group.runs[0]
  const k = rowKey(group)
  // Mode picker state: null = picker hidden, 'issue'|'edit' = picker shown
  const [showModePicker, setShowModePicker] = useState(false)
  const modePickerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!showModePicker) return
    const handlePointerDown = (e: MouseEvent) => {
      if (modePickerRef.current && !modePickerRef.current.contains(e.target as Node)) {
        setShowModePicker(false)
      }
    }
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setShowModePicker(false)
    }
    document.addEventListener('mousedown', handlePointerDown)
    document.addEventListener('keydown', handleKey)
    return () => {
      document.removeEventListener('mousedown', handlePointerDown)
      document.removeEventListener('keydown', handleKey)
    }
  }, [showModePicker])

  const openChat = (mode: 'issue' | 'edit') => {
    setShowModePicker(false)
    void chatDrawer.open({ repo: group.repo, issue: group.issue_number, mode })
  }

  return (
    <div>
      <button
        type="button"
        onClick={() => onToggle(k)}
        className="w-full flex items-center gap-3 px-4 py-2 hover:bg-secondary/30 text-left flex-wrap"
      >
        <ChevronRight
          className={cn(
            'w-3.5 h-3.5 text-muted-foreground transition-transform shrink-0',
            expanded && 'rotate-90',
          )}
        />
        {showRepo && (
          <Link
            to="/repos/$repoName"
            params={{ repoName: encodeURIComponent(group.repo) }}
            onClick={e => e.stopPropagation()}
            className="font-mono text-[11px] text-muted-foreground hover:text-foreground hover:underline shrink-0 max-w-[14rem] truncate"
            title={group.repo}
          >
            {group.repo}
          </Link>
        )}
        <a
          href={issueUrl(group.repo, group.issue_number, repoMap)}
          target="_blank"
          rel="noopener noreferrer"
          onClick={e => e.stopPropagation()}
          className="font-mono text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0 w-12"
        >
          #{group.issue_number}
        </a>
        {latest && <StatusBadge status={latest.status} runnerAlive={latest.runner_alive} />}
        <span
          className={cn(
            'text-sm truncate flex-1',
            group.state === 'closed' && 'line-through text-muted-foreground',
          )}
          title={group.issue_title || '(no title)'}
        >
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
        {/* Chat button with mode picker */}
        <div ref={modePickerRef} className="relative shrink-0" onClick={e => e.stopPropagation()}>
          <button
            onClick={() => setShowModePicker(v => !v)}
            className="text-muted-foreground hover:text-foreground"
            title="Chat about this issue"
            aria-haspopup="true"
            aria-expanded={showModePicker}
          >
            <MessageSquare className="w-3.5 h-3.5" />
          </button>
          {showModePicker && (
            <div
              className="absolute right-0 top-6 z-20 bg-card border border-border rounded-lg shadow-lg py-1 min-w-[160px]"
              role="menu"
            >
              <button
                role="menuitem"
                onClick={() => openChat('issue')}
                className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-secondary/60 text-left"
                title="Discuss requirements and land conclusions as comments/labels. File editing disabled."
              >
                <MessageSquare className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
                <span>
                  <span className="font-medium text-foreground">Issue mode</span>
                  <span className="block text-muted-foreground">discuss · no file edits</span>
                </span>
              </button>
              <button
                role="menuitem"
                onClick={() => openChat('edit')}
                className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-secondary/60 text-left"
                title="Full file-editing access for hot-fixing the issue directly."
              >
                <Code className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
                <span>
                  <span className="font-medium text-foreground">Edit mode</span>
                  <span className="block text-muted-foreground">hot-fix · full access</span>
                </span>
              </button>
            </div>
          )}
        </div>
      </button>
      {expanded && <Timeline group={group} slug={slug} />}
    </div>
  )
}

function Timeline({ group, slug }: { group: IssueGroup; slug?: string }) {
  // Run-detail page is keyed by repo slug (`owner__name`). The repo
  // page passes a precomputed slug in (single repo, computed once);
  // the project page omits it and we derive per-row from group.repo
  // so cross-repo timelines link to the right detail page.
  const effectiveSlug = slug ?? group.repo.replace(/\//g, '__')
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
          const runHref = `/runs/${effectiveSlug}/issue-${group.issue_number}/${ts}`
          const dotCls =
            r.status === 'success'
              ? 'bg-green-400 border-green-600'
              : r.status === 'failed'
                ? 'bg-red-400 border-red-600'
                : r.status === 'running'
                  ? 'bg-blue-400 border-blue-600 animate-pulse'
                  : 'bg-muted-foreground/30 border-muted-foreground/60'
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
                  <StatusBadge status={r.status} runnerAlive={r.runner_alive} />
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

export function StatusBadge({ status, runnerAlive }: { status: Run['status']; runnerAlive?: boolean }) {
  // For running rows the snapshot index sets runner_alive when the lockfile
  // PID is live. Show a green dot adjacent to the badge so users can tell
  // a true-running run apart from a stuck one (latter will reconcile soon).
  return (
    <span className="inline-flex items-center gap-1 shrink-0">
      <span
        className={cn(
          'inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-semibold border',
          status === 'success' && 'bg-green-100 text-green-700 border-green-200',
          status === 'failed' && 'bg-red-100 text-red-700 border-red-200',
          status === 'skipped' && 'bg-muted text-muted-foreground border-border',
          status === 'running' && 'bg-blue-100 text-blue-700 border-blue-200',
          status === 'cancelled' && 'bg-amber-50 text-amber-700 border-amber-200',
          status === 'no-marker' && 'bg-orange-100 text-orange-700 border-orange-200',
          status === 'skipped-empty' && 'bg-orange-50 text-orange-600 border-orange-200',
        )}
      >
        {status}
      </span>
      {status === 'running' && runnerAlive && (
        <span
          className="inline-flex items-center gap-0.5 text-[10px] text-green-700"
          title="lockfile PID is alive — runner is actively working"
        >
          <span className="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
          live
        </span>
      )}
    </span>
  )
}

export function formatDuration(start: string, end: string): string {
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
