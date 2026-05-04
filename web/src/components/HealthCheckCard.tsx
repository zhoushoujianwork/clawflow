import { useEffect, useRef, useState } from 'react'
import { Activity, ChevronDown, ChevronRight, Loader2, CheckCircle2, AlertTriangle, XCircle, FileEdit, FilePlus2, GitCommit } from 'lucide-react'
import { cn } from '../lib/utils'

// Health-check UX is split into three exports so the project page
// can place each piece where it makes layout sense:
//
//   useHealthCheck(projectName) — single source of truth for state
//     (status, result, selected, apply outcomes, polling). Lives in
//     the parent component so multiple consumers share state.
//
//   HealthCheckSummaryCard — compact card for the page's status row.
//     Always renders. Shows the run button, last outcome pill, last
//     run timestamp, and any error.
//
//   HealthCheckReviewPanel — full-width section that only renders
//     when the latest result has outcome="changes-proposed". Hosts
//     the per-file diff cards, selection checkboxes, and Apply
//     button. Lives below the repo list because it's transient
//     review work, not always-visible status.
//
// Both visual components are dumb — all behavior lives in the hook.

interface ProposedChange {
  target: 'repo' | 'project'
  repo_id?: string
  path: string
  action: 'create' | 'update'
  proposed_content: string
  current_content: string
}

interface HealthCheckResult {
  outcome: 'healthy' | 'changes-proposed' | string
  summary: string
  changes: ProposedChange[]
  raw_output?: string
}

interface HealthCheckJob {
  status: 'running' | 'done' | 'error' | 'idle'
  error?: string
  started_at?: string
  ended_at?: string
  result?: HealthCheckResult
}

interface ApplyOutcome {
  target: string
  repo_id?: string
  path: string
  status: 'written' | 'committed' | 'committed-only' | 'failed' | string
  commit_hash?: string
  detail?: string
  error?: string
}

interface ApplyResult {
  outcomes: ApplyOutcome[]
}

// changeKey gives each ProposedChange a stable identifier the
// selection set + diff-collapsed state map can use. (target, repo_id,
// path) is unique per change.
function changeKey(c: ProposedChange): string {
  return c.target === 'repo' ? `repo:${c.repo_id ?? ''}/${c.path}` : `project:${c.path}`
}

// useHealthCheck centralizes every piece of state the run/review
// flow needs. Returned object is passed by the parent into the
// summary + review components, keeping them decoupled while sharing
// one source of truth.
export interface HealthCheckHookValue {
  status: 'idle' | 'running' | 'done' | 'error'
  error: string | null
  result: HealthCheckResult | null
  endedAt: string | null
  selected: Set<string>
  expandedDiffs: Set<string>
  applying: boolean
  applyError: string | null
  applyOutcomes: ApplyOutcome[] | null
  handleRun: () => Promise<void>
  handleApply: () => Promise<void>
  toggleSelected: (key: string) => void
  toggleDiff: (key: string) => void
  findOutcome: (c: ProposedChange) => ApplyOutcome | undefined
}

export function useHealthCheck(projectName: string): HealthCheckHookValue {
  const [status, setStatus] = useState<'idle' | 'running' | 'done' | 'error'>('idle')
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<HealthCheckResult | null>(null)
  const [endedAt, setEndedAt] = useState<string | null>(null)

  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [expandedDiffs, setExpandedDiffs] = useState<Set<string>>(new Set())

  const [applying, setApplying] = useState(false)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [applyOutcomes, setApplyOutcomes] = useState<ApplyOutcome[] | null>(null)

  const pollTimerRef = useRef<number | null>(null)

  function stopPolling() {
    if (pollTimerRef.current !== null) {
      window.clearInterval(pollTimerRef.current)
      pollTimerRef.current = null
    }
  }

  function startPolling() {
    if (pollTimerRef.current !== null) return
    const tick = async () => {
      try {
        const r = await fetch(
          `/api/project/health-check/status?project=${encodeURIComponent(projectName)}`,
          { cache: 'no-store' },
        )
        const d: HealthCheckJob = await r.json().catch(() => ({}) as HealthCheckJob)
        if (r.status === 404 || d.status === 'idle') {
          stopPolling()
          setStatus('idle')
          return
        }
        if (d.status === 'running') return
        if (d.status === 'done') {
          stopPolling()
          setStatus('done')
          if (d.result) {
            setResult(d.result)
            setSelected(new Set((d.result.changes ?? []).map(changeKey)))
            setExpandedDiffs(new Set())
            setApplyOutcomes(null)
            setApplyError(null)
          }
          if (d.ended_at) setEndedAt(d.ended_at)
          return
        }
        if (d.status === 'error') {
          stopPolling()
          setStatus('error')
          setError(d.error ?? 'health check failed')
          if (d.ended_at) setEndedAt(d.ended_at)
          return
        }
      } catch {
        // network blip — keep polling
      }
    }
    void tick()
    pollTimerRef.current = window.setInterval(tick, 2000)
  }

  // Hydrate on mount: a previously-completed run (in memory or
  // persisted to disk) shows up immediately so the user doesn't see
  // an empty card after navigating back.
  useEffect(() => {
    fetch(`/api/project/health-check/status?project=${encodeURIComponent(projectName)}`, {
      cache: 'no-store',
    })
      .then(async r => ({ s: r.status, body: (await r.json().catch(() => ({}))) as HealthCheckJob }))
      .then(({ s, body }) => {
        if (s === 200 && body.status === 'running') {
          setStatus('running')
          startPolling()
          return
        }
        if (s === 200 && body.status === 'done' && body.result) {
          setStatus('done')
          setResult(body.result)
          setSelected(new Set((body.result.changes ?? []).map(changeKey)))
          if (body.ended_at) setEndedAt(body.ended_at)
        }
        if (s === 200 && body.status === 'error') {
          setStatus('error')
          setError(body.error ?? 'health check failed')
          if (body.ended_at) setEndedAt(body.ended_at)
        }
      })
      .catch(() => {})
    return () => stopPolling()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectName])

  async function handleRun() {
    setError(null)
    setApplyOutcomes(null)
    setApplyError(null)
    setStatus('running')
    try {
      const r = await fetch('/api/project/health-check/run', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: projectName }),
      })
      const d = await r.json().catch(() => ({}))
      if (r.status === 202 || r.status === 409) {
        startPolling()
        return
      }
      throw new Error(d.error || `HTTP ${r.status}`)
    } catch (e) {
      setStatus('error')
      setError(e instanceof Error ? e.message : 'unknown error')
    }
  }

  async function handleApply() {
    if (!result || selected.size === 0) return
    const changes = result.changes.filter(c => selected.has(changeKey(c)))
    setApplying(true)
    setApplyError(null)
    try {
      const r = await fetch('/api/project/health-check/apply', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: projectName, changes }),
      })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
      setApplyOutcomes((d as ApplyResult).outcomes ?? [])
    } catch (e) {
      setApplyError(e instanceof Error ? e.message : 'unknown error')
    } finally {
      setApplying(false)
    }
  }

  function toggleSelected(key: string) {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  function toggleDiff(key: string) {
    setExpandedDiffs(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  function findOutcome(c: ProposedChange): ApplyOutcome | undefined {
    if (!applyOutcomes) return undefined
    return applyOutcomes.find(
      o =>
        o.path === c.path &&
        ((c.target === 'repo' && o.target === 'repo' && o.repo_id === c.repo_id) ||
          (c.target === 'project' && o.target === 'project')),
    )
  }

  return {
    status,
    error,
    result,
    endedAt,
    selected,
    expandedDiffs,
    applying,
    applyError,
    applyOutcomes,
    handleRun,
    handleApply,
    toggleSelected,
    toggleDiff,
    findOutcome,
  }
}

// formatRelativeTime turns an ISO timestamp into a compact "2h ago"
// label suitable for status pills. Falls back to the local date
// string for anything older than a week. Pure UI sugar — accuracy
// beyond minute resolution doesn't matter.
function formatRelativeTime(iso: string | null): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const diff = Date.now() - t
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  if (diff < 604_800_000) return `${Math.floor(diff / 86_400_000)}d ago`
  return new Date(iso).toLocaleDateString()
}

// HealthCheckSummaryCard is the compact card that lives in the
// project page's status row alongside Automation. Same visual
// weight: a single short card showing button + outcome + when. Heavy
// content (per-file diffs, apply UI) lives in HealthCheckReviewPanel.
export function HealthCheckSummaryCard({ healthCheck }: { healthCheck: HealthCheckHookValue }) {
  const { status, error, result, endedAt, handleRun } = healthCheck
  const changeCount = result?.changes?.length ?? 0
  const hasChanges = result?.outcome === 'changes-proposed'
  const isHealthy = result?.outcome === 'healthy'

  return (
    <section>
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-sm font-semibold text-foreground">Health Check</h2>
        {status === 'done' && isHealthy && (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400">
            <CheckCircle2 className="w-3 h-3" /> all healthy
          </span>
        )}
        {status === 'done' && hasChanges && (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium bg-amber-100 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400">
            <AlertTriangle className="w-3 h-3" /> {changeCount} change{changeCount === 1 ? '' : 's'}
          </span>
        )}
        {status === 'error' && (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium bg-red-100 text-red-700 dark:bg-red-950/40 dark:text-red-400">
            <XCircle className="w-3 h-3" /> error
          </span>
        )}
      </div>

      <div
        className={cn(
          'rounded-xl p-4 border h-full transition-colors',
          isHealthy
            ? 'bg-emerald-50/40 border-emerald-200 dark:bg-emerald-950/10 dark:border-emerald-900/40'
            : hasChanges
              ? 'bg-amber-50/40 border-amber-200 dark:bg-amber-950/10 dark:border-amber-900/40'
              : 'bg-card border-border',
        )}
      >
        <div className="flex items-start justify-between gap-3">
          <div className="flex-1 min-w-0">
            <p className="text-xs text-muted-foreground">
              Audit each repo's CLAUDE.md and the project docs against a 12-dimension rubric.
            </p>
            {status === 'done' && endedAt && (
              <p className="text-[11px] text-muted-foreground mt-1 tabular-nums">
                last run {formatRelativeTime(endedAt)}
              </p>
            )}
            {status === 'running' && (
              <p className="text-[11px] text-muted-foreground mt-1">
                Calling claude -p — typically 30s–2min. Runs in the background.
              </p>
            )}
          </div>
          <button
            type="button"
            onClick={handleRun}
            disabled={status === 'running'}
            className={cn(
              'shrink-0 inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors',
              'bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed',
            )}
          >
            {status === 'running' ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Activity className="w-3.5 h-3.5" />}
            {status === 'running' ? 'Running…' : status === 'done' ? 'Re-run' : 'Run'}
          </button>
        </div>

        {error && (
          <div className="mt-3 text-xs text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 dark:bg-red-950/30 dark:border-red-900">
            {error}
          </div>
        )}
      </div>
    </section>
  )
}

// HealthCheckReviewPanel renders the per-file diff cards plus the
// Apply UI. Only mounts when there's review work to do (the parent
// already guards on outcome === 'changes-proposed'), so it can take
// full page width without competing with always-visible elements.
export function HealthCheckReviewPanel({ healthCheck }: { healthCheck: HealthCheckHookValue }) {
  const {
    result,
    selected,
    expandedDiffs,
    applying,
    applyError,
    applyOutcomes,
    handleApply,
    toggleSelected,
    toggleDiff,
    findOutcome,
  } = healthCheck
  if (!result || result.outcome !== 'changes-proposed') return null
  const changes = result.changes ?? []

  return (
    <section className="mb-6">
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-semibold text-foreground">Health Check Review</h2>
          <span className="text-xs text-muted-foreground">
            {changes.length} proposed change{changes.length === 1 ? '' : 's'} — review the diff and apply selectively
          </span>
        </div>
      </div>

      <div className="bg-card border border-border rounded-xl p-4 space-y-3">
        {result.summary && (
          <div className="text-xs text-foreground bg-secondary/40 rounded-lg px-3 py-2 whitespace-pre-wrap font-mono">
            {result.summary}
          </div>
        )}

        <div className="space-y-2">
          {changes.map(c => {
            const key = changeKey(c)
            return (
              <ChangeRow
                key={key}
                change={c}
                selected={selected.has(key)}
                expanded={expandedDiffs.has(key)}
                outcome={findOutcome(c)}
                onToggleSelect={() => toggleSelected(key)}
                onToggleDiff={() => toggleDiff(key)}
              />
            )
          })}
        </div>

        {applyError && (
          <div className="text-xs text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 dark:bg-red-950/30 dark:border-red-900">
            {applyError}
          </div>
        )}

        {applyOutcomes === null && (
          <div className="flex items-center justify-end gap-3 pt-1">
            <span className="text-xs text-muted-foreground">
              {selected.size} of {changes.length} selected
            </span>
            <button
              type="button"
              onClick={handleApply}
              disabled={applying || selected.size === 0}
              className={cn(
                'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors',
                'bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed',
              )}
            >
              {applying ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <GitCommit className="w-3.5 h-3.5" />}
              {applying ? 'Applying…' : `Apply ${selected.size} change${selected.size === 1 ? '' : 's'}`}
            </button>
          </div>
        )}

        {applyOutcomes !== null && (
          <p className="text-xs text-muted-foreground pt-1">
            Applied. Re-run if you want a fresh check.
          </p>
        )}
      </div>
    </section>
  )
}

interface ChangeRowProps {
  change: ProposedChange
  selected: boolean
  expanded: boolean
  outcome?: ApplyOutcome
  onToggleSelect: () => void
  onToggleDiff: () => void
}

function ChangeRow({ change, selected, expanded, outcome, onToggleSelect, onToggleDiff }: ChangeRowProps) {
  const Icon = change.action === 'create' ? FilePlus2 : FileEdit
  const targetLabel =
    change.target === 'repo' ? `${change.repo_id} · ${change.path}` : `project · ${change.path}`

  return (
    <div className="border border-border rounded-lg overflow-hidden">
      <div className="flex items-center gap-2 px-3 py-2 hover:bg-secondary/20">
        {!outcome && (
          <input
            type="checkbox"
            checked={selected}
            onChange={onToggleSelect}
            className="w-4 h-4 accent-primary cursor-pointer"
          />
        )}
        <Icon className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
        <span className="text-sm font-mono text-foreground truncate" title={targetLabel}>
          {targetLabel}
        </span>
        <span
          className={cn(
            'px-1.5 py-0.5 rounded text-[10px] font-medium uppercase tracking-wide shrink-0',
            change.action === 'create'
              ? 'bg-blue-100 text-blue-700 dark:bg-blue-950/40 dark:text-blue-400'
              : 'bg-amber-100 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400',
          )}
        >
          {change.action}
        </span>
        {outcome && <OutcomeBadge outcome={outcome} />}
        <button
          type="button"
          onClick={onToggleDiff}
          className="ml-auto inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground shrink-0"
        >
          {expanded ? (
            <>
              <ChevronDown className="w-3.5 h-3.5" /> hide diff
            </>
          ) : (
            <>
              <ChevronRight className="w-3.5 h-3.5" /> show diff
            </>
          )}
        </button>
      </div>

      {expanded && (
        <div className="border-t border-border">
          {change.action === 'update' && (
            <DiffView current={change.current_content} proposed={change.proposed_content} />
          )}
          {change.action === 'create' && (
            <DiffView current="" proposed={change.proposed_content} />
          )}
        </div>
      )}
    </div>
  )
}

// DiffRow describes one aligned row in the side-by-side diff.
// `null` on either side means a placeholder (the other side is the
// only one carrying content for this row, e.g. an inserted line shows
// `null` on the left).
type DiffStatus = 'eq' | 'del' | 'add'
interface DiffRow {
  left: string | null
  right: string | null
  leftLine: number | null
  rightLine: number | null
  status: DiffStatus
}

// computeLineDiff produces a list of aligned (left, right) rows
// suitable for a side-by-side diff view. Standard LCS dynamic
// programming, O(n*m) time and memory. Fine for CLAUDE.md /
// context.md / testing.md (typically <1k lines); swap in Myers if
// files ever push past the comfortable budget.
function computeLineDiff(current: string, proposed: string): DiffRow[] {
  const a = current === '' ? [] : current.split('\n')
  const b = proposed === '' ? [] : proposed.split('\n')
  const n = a.length
  const m = b.length

  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      if (a[i] === b[j]) dp[i][j] = dp[i + 1][j + 1] + 1
      else dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }

  const rows: DiffRow[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      rows.push({ left: a[i], right: b[j], leftLine: i + 1, rightLine: j + 1, status: 'eq' })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ left: a[i], right: null, leftLine: i + 1, rightLine: null, status: 'del' })
      i++
    } else {
      rows.push({ left: null, right: b[j], leftLine: null, rightLine: j + 1, status: 'add' })
      j++
    }
  }
  while (i < n) {
    rows.push({ left: a[i], right: null, leftLine: i + 1, rightLine: null, status: 'del' })
    i++
  }
  while (j < m) {
    rows.push({ left: null, right: b[j], leftLine: null, rightLine: j + 1, status: 'add' })
    j++
  }
  return rows
}

// DiffView renders a side-by-side line diff with color-coded
// add/remove highlighting. Both sides live inside the same scroll
// container — that gives synced scrolling without ref plumbing
// (one container = one scroll position).
function DiffView({ current, proposed }: { current: string; proposed: string }) {
  const rows = computeLineDiff(current, proposed)
  const additions = rows.filter(r => r.status === 'add').length
  const deletions = rows.filter(r => r.status === 'del').length

  return (
    <div>
      <div className="grid grid-cols-2 border-b border-border">
        <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wide bg-secondary/40 text-muted-foreground flex items-center justify-between">
          <span>Current</span>
          {deletions > 0 && (
            <span className="text-red-700 dark:text-red-400 normal-case font-mono">−{deletions}</span>
          )}
        </div>
        <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wide bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400 flex items-center justify-between border-l border-border">
          <span>Proposed</span>
          {additions > 0 && (
            <span className="text-emerald-700 dark:text-emerald-400 normal-case font-mono">+{additions}</span>
          )}
        </div>
      </div>

      {rows.length === 0 ? (
        <div className="px-3 py-4 text-xs text-muted-foreground italic">(empty)</div>
      ) : (
        <div className="grid grid-cols-2 max-h-96 overflow-y-auto text-xs font-mono">
          {rows.map((row, idx) => (
            <DiffRowView key={idx} row={row} />
          ))}
        </div>
      )}
    </div>
  )
}

function DiffRowView({ row }: { row: DiffRow }) {
  return (
    <>
      <DiffCell text={row.left} lineNum={row.leftLine} side="left" status={row.status} />
      <DiffCell text={row.right} lineNum={row.rightLine} side="right" status={row.status} />
    </>
  )
}

function DiffCell({
  text,
  lineNum,
  side,
  status,
}: {
  text: string | null
  lineNum: number | null
  side: 'left' | 'right'
  status: DiffStatus
}) {
  const isPlaceholder =
    (side === 'left' && status === 'add') || (side === 'right' && status === 'del')

  let bg = ''
  if (isPlaceholder) {
    bg = 'bg-secondary/30'
  } else if (side === 'left' && status === 'del') {
    bg = 'bg-red-100/60 dark:bg-red-950/30'
  } else if (side === 'right' && status === 'add') {
    bg = 'bg-emerald-100/60 dark:bg-emerald-950/30'
  }

  return (
    <div
      className={cn(
        'flex gap-2 px-2 py-0.5 whitespace-pre-wrap break-words min-w-0',
        bg,
        side === 'right' && 'border-l border-border',
      )}
    >
      <span className="select-none text-muted-foreground tabular-nums w-8 shrink-0 text-right">
        {lineNum ?? ''}
      </span>
      <span className="flex-1 min-w-0">{text === null ? '' : text === '' ? ' ' : text}</span>
    </div>
  )
}

function OutcomeBadge({ outcome }: { outcome: ApplyOutcome }) {
  switch (outcome.status) {
    case 'committed':
      return (
        <span
          className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400 shrink-0"
          title={outcome.commit_hash ? `commit ${outcome.commit_hash.slice(0, 7)}` : ''}
        >
          <CheckCircle2 className="w-3 h-3" />
          committed{outcome.commit_hash ? ` ${outcome.commit_hash.slice(0, 7)}` : ''}
        </span>
      )
    case 'committed-only':
      return (
        <span
          className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-100 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400 shrink-0"
          title={outcome.detail ?? 'commit succeeded but push failed'}
        >
          <AlertTriangle className="w-3 h-3" />
          local-only{outcome.commit_hash ? ` ${outcome.commit_hash.slice(0, 7)}` : ''}
        </span>
      )
    case 'written':
      return (
        <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400 shrink-0">
          <CheckCircle2 className="w-3 h-3" />
          written
        </span>
      )
    case 'failed':
      return (
        <span
          className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-red-100 text-red-700 dark:bg-red-950/40 dark:text-red-400 shrink-0"
          title={outcome.error ?? 'failed'}
        >
          <XCircle className="w-3 h-3" />
          failed
        </span>
      )
    default:
      return (
        <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-secondary text-muted-foreground shrink-0">
          {outcome.status}
        </span>
      )
  }
}
