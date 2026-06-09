import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
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
  // Sub-issue linkage (GitHub native; absent on GitLab). parent_number is
  // the number of the issue this one is a sub-issue of; sub_total/
  // sub_completed mirror the parent's sub_issues_summary. See the Go
  // snapshot.IssueEntry for the source of truth.
  parent_number?: number
  sub_total?: number
  sub_completed?: number
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
  state?: string // "open" | "closed" | "unknown" (unknown = run exists but no issues.json entry)
  // Carried through from IssueEntry so the renderer can nest sub-issues
  // under their parent and draw the parent's ring progress.
  parent_number?: number
  sub_total?: number
  sub_completed?: number
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
          parent_number: iss.parent_number,
          sub_total: iss.sub_total,
          sub_completed: iss.sub_completed,
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
          // 'unknown': issue not in issues.json snapshot — don't assume open.
          // The backend's WriteIssues sticky-merge should prevent this for
          // closed issues; 'unknown' acts as a safety net so they don't
          // incorrectly land in the Done section.
          state: 'unknown',
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

// hasIncompleteSubIssues is true when a group is a parent (tracking) issue
// whose sub-issues are not all completed. sub_total/sub_completed come from
// the parent's GitHub sub_issues_summary, so this works even when some
// children fall outside the snapshot window (the data-loss fallback the
// feature evaluation called out). Closed parents are handled by the caller.
export function hasIncompleteSubIssues(g: IssueGroup): boolean {
  const total = g.sub_total ?? 0
  if (total <= 0) return false
  return (g.sub_completed ?? 0) < total
}

// effectiveStatus is the run status to display and bucket a group by. For a
// parent (tracking) issue whose children aren't all done, the parent's own
// latest run (often `success` from decompose / track-progress) masks the fact
// that sub-tasks are still in flight — so we surface `running` instead, which
// keeps the parent out of the Done section and off a misleading success badge.
// Closed parents keep their real latest status so they stay in the Closed
// bucket and don't get aggregation-overridden.
export function effectiveStatus(g: IssueGroup): Run['status'] | undefined {
  if (g.state !== 'closed' && hasIncompleteSubIssues(g)) return 'running'
  return g.runs[0]?.status
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
  // Sort each bucket by issue number descending (newest issue on top) so
  // ordering is stable and matches the sub-issue nesting, which also sorts
  // children by issue number desc.
  for (const b of buckets) b.groups.sort(byIssueNumberDesc)
  return buckets
}

// Pre-baked section sets for the two consumers.
//
// REPO_SECTIONS matches the historical repo-page layout exactly so the
// migration is a no-op visually. PROJECT_SECTIONS adds two front-of-
// list buckets that triagers care about most: in-flight work and the
// "awaiting evaluation" bottleneck.
export const REPO_SECTIONS: SectionConfig[] = [
  { title: 'Running', tone: 'blue', filter: g => g.state !== 'closed' && effectiveStatus(g) === 'running' },
  { title: 'Pending', tone: 'amber', filter: g => g.state !== 'closed' && g.pending.length > 0 },
  // Explicitly check state === 'open' so that issues with state 'unknown'
  // (run exists but no issues.json entry — see useIssueGroups) or 'closed'
  // are not incorrectly shown as Done.
  { title: 'Done', tone: 'muted', filter: g => g.state === 'open' },
  { title: 'Closed', tone: 'gray', filter: g => g.state === 'closed', limit: 10 },
]

export const PROJECT_SECTIONS: SectionConfig[] = [
  {
    title: 'In flight',
    tone: 'blue',
    filter: g => g.state !== 'closed' && effectiveStatus(g) === 'running',
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
  { title: 'Done', tone: 'muted', filter: g => g.state === 'open', limit: 10 },
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
  // Split sub-issues out of the top-level list: a group whose parent is
  // also present is rendered nested under that parent (collapsed by
  // default), not as its own section row. Groups whose parent is absent
  // (e.g. parent fell outside the snapshot window) stay top-level so they
  // never vanish. childrenOf is keyed by the parent's rowKey.
  const { topLevel, childrenOf } = useMemo(() => splitSubIssues(groups), [groups])
  const buckets = useMemo(() => bucketize(topLevel, sections), [topLevel, sections])
  return (
    <div className="space-y-4">
      {buckets.map(b => (
        <IssueSection
          key={b.config.title}
          config={b.config}
          groups={b.groups}
          childrenOf={childrenOf}
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

// byIssueNumberDesc sorts groups newest-issue-first. Used both for the
// section buckets and for the nested sub-issue lists so the whole tree
// shares one ordering.
function byIssueNumberDesc(a: IssueGroup, b: IssueGroup): number {
  return b.issue_number - a.issue_number
}

// splitSubIssues partitions groups into top-level rows and a parent→children
// index. A group is treated as a child only when its parent_number resolves
// to another group in the same repo that is actually present; otherwise it
// stays top-level so an orphaned sub-issue is never hidden. Children are
// sorted newest-first to match the section ordering.
export function splitSubIssues(groups: IssueGroup[]): {
  topLevel: IssueGroup[]
  childrenOf: Map<string, IssueGroup[]>
} {
  const present = new Set(groups.map(rowKey))
  const childrenOf = new Map<string, IssueGroup[]>()
  const topLevel: IssueGroup[] = []
  for (const g of groups) {
    const parentKey = g.parent_number != null ? `${g.repo}#${g.parent_number}` : null
    if (parentKey && present.has(parentKey)) {
      const arr = childrenOf.get(parentKey) ?? []
      arr.push(g)
      childrenOf.set(parentKey, arr)
    } else {
      topLevel.push(g)
    }
  }
  for (const arr of childrenOf.values()) arr.sort(byIssueNumberDesc)
  return { topLevel, childrenOf }
}

// SubIssueRing draws a small donut showing how many of an issue's
// sub-issues are completed. Green once everything is done, blue while in
// progress. Rendered only when the issue actually has sub-issues.
export function SubIssueRing({ completed, total }: { completed: number; total: number }) {
  const radius = 6.5
  const circumference = 2 * Math.PI * radius
  const pct = total > 0 ? Math.min(1, completed / total) : 0
  const done = total > 0 && completed >= total
  return (
    <span
      className="inline-flex items-center gap-1 shrink-0"
      title={`${completed}/${total} sub-issues done`}
    >
      <svg width="16" height="16" viewBox="0 0 16 16" className="shrink-0 -rotate-90">
        <circle cx="8" cy="8" r={radius} fill="none" strokeWidth="2.5" className="stroke-border" />
        <circle
          cx="8"
          cy="8"
          r={radius}
          fill="none"
          strokeWidth="2.5"
          strokeLinecap="round"
          className={done ? 'stroke-green-500' : 'stroke-blue-500'}
          strokeDasharray={`${(circumference * pct).toFixed(2)} ${circumference.toFixed(2)}`}
        />
      </svg>
      <span className="text-[11px] tabular-nums text-muted-foreground">
        {completed}/{total}
      </span>
    </span>
  )
}

function IssueSection({
  config,
  groups,
  childrenOf,
  showRepo,
  repoMap,
  slug,
  expanded,
  onToggle,
}: {
  config: SectionConfig
  groups: IssueGroup[]
  childrenOf: Map<string, IssueGroup[]>
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
            childrenOf={childrenOf}
            depth={0}
            showRepo={showRepo}
            repoMap={repoMap}
            slug={slug}
            expanded={expanded}
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
  childrenOf,
  depth,
  showRepo,
  repoMap,
  slug,
  expanded,
  onToggle,
}: {
  group: IssueGroup
  childrenOf: Map<string, IssueGroup[]>
  depth: number
  showRepo: boolean
  repoMap: RepoInfoMap
  slug?: string
  expanded: Set<string>
  onToggle: (key: string) => void
}) {
  const chatDrawer = useChatDrawer()
  const latest = group.runs[0]
  // For tracking issues with unfinished sub-issues this resolves to
  // `running` so the badge reflects aggregate progress, not just the
  // parent's own last run (see effectiveStatus).
  const displayStatus = effectiveStatus(group)
  const k = rowKey(group)
  const isExpanded = expanded.has(k)
  const children = childrenOf.get(k) ?? []
  const hasChildren = children.length > 0
  const subTotal = group.sub_total ?? 0
  // Mode picker state: null = picker hidden, 'issue'|'edit' = picker shown
  const [showModePicker, setShowModePicker] = useState(false)
  // Menu position is computed from the trigger's bounding rect and rendered
  // into a body-level portal so the section container's `overflow-hidden`
  // (needed for the rounded-xl corners) doesn't clip the dropdown.
  const [menuPos, setMenuPos] = useState<{ top: number; right: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!showModePicker) return
    const handlePointerDown = (e: MouseEvent) => {
      const target = e.target as Node
      const inMenu = menuRef.current?.contains(target)
      const inTrigger = triggerRef.current?.contains(target)
      if (!inMenu && !inTrigger) {
        setShowModePicker(false)
      }
    }
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setShowModePicker(false)
    }
    // Close on scroll/resize rather than try to follow the trigger —
    // simpler, and matches the behaviour of most native dropdowns.
    const handleDismiss = () => setShowModePicker(false)
    document.addEventListener('mousedown', handlePointerDown)
    document.addEventListener('keydown', handleKey)
    window.addEventListener('scroll', handleDismiss, true)
    window.addEventListener('resize', handleDismiss)
    return () => {
      document.removeEventListener('mousedown', handlePointerDown)
      document.removeEventListener('keydown', handleKey)
      window.removeEventListener('scroll', handleDismiss, true)
      window.removeEventListener('resize', handleDismiss)
    }
  }, [showModePicker])

  const toggleModePicker = () => {
    setShowModePicker(prev => {
      const next = !prev
      if (next && triggerRef.current) {
        const rect = triggerRef.current.getBoundingClientRect()
        // Anchor the menu below the trigger, right-aligned to its right edge.
        // `right` is measured from the viewport's right edge because the
        // portal uses position: fixed.
        setMenuPos({
          top: rect.bottom + 4,
          right: Math.max(4, window.innerWidth - rect.right),
        })
      }
      return next
    })
  }

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
        style={depth > 0 ? { paddingLeft: `${1 + depth * 1.5}rem` } : undefined}
      >
        <ChevronRight
          className={cn(
            'w-3.5 h-3.5 text-muted-foreground transition-transform shrink-0',
            isExpanded && 'rotate-90',
          )}
        />
        {showRepo && (
          <Link
            to="/repos/$"
            params={{ _splat: group.repo }}
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
        {displayStatus && (
          <StatusBadge
            status={displayStatus}
            // Only surface the live dot for a genuinely-running run; an
            // aggregated `running` (parent with unfinished children) has no
            // active runner on the parent itself.
            runnerAlive={latest?.status === 'running' ? latest.runner_alive : undefined}
          />
        )}
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
        {subTotal > 0 && (
          <SubIssueRing completed={group.sub_completed ?? 0} total={subTotal} />
        )}
        <span className="text-xs text-muted-foreground shrink-0 tabular-nums w-16 text-right">
          {group.runs.length} {group.runs.length === 1 ? 'run' : 'runs'}
        </span>
        {/* Chat button with mode picker */}
        <div className="relative shrink-0" onClick={e => e.stopPropagation()}>
          <button
            ref={triggerRef}
            onClick={toggleModePicker}
            className="text-muted-foreground hover:text-foreground"
            title="Chat about this issue"
            aria-haspopup="true"
            aria-expanded={showModePicker}
          >
            <MessageSquare className="w-3.5 h-3.5" />
          </button>
          {showModePicker && menuPos && createPortal(
            <div
              ref={menuRef}
              className="fixed z-50 bg-card border border-border rounded-lg shadow-lg py-1 min-w-[160px]"
              style={{ top: menuPos.top, right: menuPos.right }}
              role="menu"
              onClick={e => e.stopPropagation()}
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
            </div>,
            document.body,
          )}
        </div>
      </button>
      {isExpanded && (
        <>
          {hasChildren &&
            children.map(child => (
              <IssueRow
                key={rowKey(child)}
                group={child}
                childrenOf={childrenOf}
                depth={depth + 1}
                showRepo={showRepo}
                repoMap={repoMap}
                slug={slug}
                expanded={expanded}
                onToggle={onToggle}
              />
            ))}
          <Timeline group={group} slug={slug} />
        </>
      )}
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
