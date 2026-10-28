import { createFileRoute } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { ChevronRight, ExternalLink, Loader2, RefreshCw } from 'lucide-react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { cn } from '../lib/utils'
import { issueUrl, repoUrl, useRepoInfoMap, type RepoInfoMap } from '../lib/vcsUrls'

type Stage =
  | 'queued'
  | 'implementing'
  | 'pr_open'
  | 'ci_pending'
  | 'ci_failed'
  | 'acceptance_pending'
  | 'acceptance_running'
  | 'accepted'
  | 'rejected'
  | 'infra_retry'
  | 'merged'
  | 'failed'
  | 'blocked'

interface RunInfo {
  operator: string
  status: string
  stage?: string
  started_at: string
  ended_at?: string
  live?: boolean
}

interface PRInfo {
  number: number
  url?: string
  title?: string
  state: string
  draft?: boolean
  head_branch?: string
  head_sha?: string
  ci?: string
  ci_error?: string
  mergeable?: string
}

interface AcceptanceInfo {
  required: boolean
  running?: boolean
  verdict?: string
  valid?: boolean
  summary?: string
  attempts: number
  infra_attempts: number
  max_infra_attempts?: number
  latest_is_infra?: boolean
  next_retry_at?: string
  completed_at?: string
  planner_session?: string
  reviewer_session?: string
  error?: string
}

interface Delivery {
  pr: number
  pr_url?: string
  merge_commit_sha?: string
  merged_at?: string
}

interface Item {
  state?: string
  attention?: string[]
  issue: number
  title: string
  stage: Stage
  why: string
  evidence: string[]
  updated_at?: string
  in_queue?: boolean
  queue_position?: number
  run?: RunInfo
  pr?: PRInfo
  acceptance?: AcceptanceInfo
  delivery?: Delivery
}

interface RepoBoard {
  repo: string
  enabled: boolean
  platform: string
  auto_merge: boolean
  ci_required: boolean
  acceptance_required: boolean
  max_in_flight: number
  stale?: boolean
  items: Item[]
  error?: string
}

interface PipelineEvent {
  time: string
  level: string
  event: string
  fields: Record<string, string>
}

interface GlobalQueueEntry {
  position: number
  key: string
  repo: string
  number: number
  title?: string
  tier: string
  rank?: number
  state: string
}

interface Board {
  stale: boolean
  refreshing: boolean
  error?: string
  generated_at: string
  repos: RepoBoard[]
  global_queue?: GlobalQueueEntry[]
  events: PipelineEvent[]
  runtime: {
    version: string
    scheduler: { status: string; interval_minutes: number; paused: boolean; next_fire_unix_ms?: number; started_at?: string; last_result?: string; last_ended_at?: string; agent_slots_used: number; agent_slots_total: number }
    execution: { ready: boolean; error?: string; profile?: string; backend?: string; model?: string; service_tier?: string }
  }
}

type Preferences = { tab?: 'active' | 'delivered' | 'all'; expanded?: string; events?: 'important' | 'all'; repo?: string }
export function preferences(s: Record<string, unknown>): Preferences {
  return {
    tab: s.tab === 'active' || s.tab === 'delivered' || s.tab === 'all' ? s.tab : undefined,
    expanded: typeof s.expanded === 'string' ? s.expanded : undefined,
    events: s.events === 'important' || s.events === 'all' ? s.events : undefined,
    repo: typeof s.repo === 'string' ? s.repo : undefined,
  }
}
const STORAGE_KEY = 'clawflow.pipeline'
export const Route = createFileRoute('/_app/pipeline')({
  component: PipelinePage,
  validateSearch: preferences,
})

const REFRESH_MS = 30_000

const STAGE_LABEL: Record<Stage, string> = {
  queued: 'Queued',
  implementing: 'Implementing',
  pr_open: 'PR open',
  ci_pending: 'CI pending',
  ci_failed: 'CI failed',
  acceptance_pending: 'Acceptance pending',
  acceptance_running: 'Acceptance running',
  accepted: 'Accepted',
  rejected: 'Acceptance failed',
  infra_retry: 'Infra retry',
  merged: 'Merged',
  failed: 'Failed',
  blocked: 'Blocked',
}

const GREEN = 'bg-green-100 text-green-800 border-green-200'
const RED = 'bg-red-100 text-red-800 border-red-200'
const AMBER = 'bg-amber-100 text-amber-800 border-amber-200'
const BLUE = 'bg-blue-100 text-blue-800 border-blue-200'
const GREY = 'bg-secondary text-muted-foreground border-border'

const STAGE_TONE: Record<Stage, string> = {
  queued: GREY,
  implementing: BLUE,
  pr_open: BLUE,
  ci_pending: AMBER,
  ci_failed: RED,
  acceptance_pending: AMBER,
  acceptance_running: BLUE,
  accepted: GREEN,
  rejected: RED,
  infra_retry: AMBER,
  merged: GREEN,
  failed: RED,
  blocked: RED,
}

function timeAgo(iso?: string): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (!isFinite(t)) return '—'
  const diff = Math.floor((Date.now() - t) / 1000)
  if (diff < 0) return 'in ' + humanSeconds(-diff)
  if (diff < 60) return `${diff}s ago`
  return humanSeconds(diff) + ' ago'
}

function humanSeconds(s: number): string {
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}min`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

function shortSha(sha?: string): string {
  return sha ? sha.slice(0, 7) : '—'
}

function StageChip({ stage }: { stage: Stage }) {
  return (
    <span
      data-stage={stage}
      className={cn('inline-flex items-center px-2 py-0.5 rounded-full border text-[11px] font-medium whitespace-nowrap', STAGE_TONE[stage] ?? GREY)}
    >
      {STAGE_LABEL[stage] ?? stage}
    </span>
  )
}

function PipelinePage() {
  useDocumentTitle('Pipeline')
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const [restored, setRestored] = useState(false)
  useEffect(() => {
    if (restored) return
    setRestored(true)
    try {
      const stored = preferences(JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}'))
      if (stored.tab === 'all') stored.tab = 'active'
      if (stored.events === 'all') stored.events = 'important'
      const next = Object.fromEntries(Object.entries({ ...stored, ...Object.fromEntries(Object.entries(search).filter(([, v]) => v !== undefined)) }))
      void navigate({ search: next, replace: true })
    } catch { /* Storage may be disabled. URL remains usable. */ }
  }, [restored, search, navigate])
  function update(next: Preferences) {
    const value = { ...search, ...next }
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...value, tab: value.tab === 'all' ? 'active' : value.tab, events: value.events === 'all' ? 'important' : value.events })) } catch { /* URL still persists. */ }
    void navigate({ search: value })
  }
  const expanded = (search.expanded || '').split(',').filter(Boolean)

  const repoInfo = useRepoInfoMap()
  const [board, setBoard] = useState<Board | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  const load = useCallback(async () => {
    setRefreshing(true)
    try {
      const r = await fetch('/api/pipeline', { cache: 'no-store' })
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.error || `HTTP ${r.status}`)
      }
      const next = (await r.json()) as Board
      setBoard(next)
      setError(next.error ?? null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const id = window.setInterval(() => void load(), REFRESH_MS)
    return () => window.clearInterval(id)
  }, [load])

  return (
    <div className="max-w-6xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-4 flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Delivery pipeline</h1>
          <p className="text-xs text-muted-foreground mt-1">
            Issue → PR → CI → acceptance → merge. Read-only.
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground tabular-nums">
          {board && <span>{board.generated_at ? `updated ${timeAgo(board.generated_at)}` : 'Waiting for first snapshot'}{board.refreshing ? ' · refreshing…' : board.stale ? ' · stale' : ''}</span>}
          <button
            onClick={() => void load()}
            disabled={refreshing}
            aria-label="Refresh pipeline"
            className="inline-flex items-center gap-1 px-2 py-1 rounded-lg border border-border bg-card hover:bg-secondary disabled:opacity-60"
          >
            <RefreshCw className={cn('w-3.5 h-3.5', refreshing && 'animate-spin')} />
            Refresh
          </button>
        </div>
      </div>

      {error && (
        <div role="alert" className="mb-4 px-3 py-2 rounded-lg border border-red-200 bg-red-50 text-sm text-red-800">
          Could not load the pipeline: {error}
        </div>
      )}

      {loading && !board ? (
        <p className="text-sm text-muted-foreground text-center py-8 flex items-center justify-center gap-2">
          <Loader2 className="w-4 h-4 animate-spin" /> Loading…
        </p>
      ) : board ? (
        <>
          <RuntimeHeader runtime={board.runtime} />
          <Attention board={board} repoInfo={repoInfo} />
          <div className="flex flex-wrap gap-2 mb-3" role="tablist" aria-label="Pipeline items">
            {(['active', 'delivered', 'all'] as const).map(tab => <button key={tab} role="tab" aria-selected={(search.tab || 'active') === tab} onClick={() => update({ tab })} className={cn('px-3 py-1 rounded border text-sm capitalize', (search.tab || 'active') === tab && 'bg-secondary')}>{tab}</button>)}
          </div>
          {board.repos.length === 0 ? (
            <div className="bg-card border border-border rounded-xl p-10 text-center">
              <p className="text-base font-semibold text-foreground mb-1">{board.generated_at ? 'No repository is tracked yet' : 'Preparing pipeline snapshot…'}</p>
              {board.generated_at && <p className="text-sm text-muted-foreground">
                Enable a repository with <code>clawflow repo enable</code> and enroll issues using the clawflow label.
              </p>}
            </div>
          ) : (
            board.repos.map(r => <RepoSection key={r.repo} board={r} repoInfo={repoInfo} queue={board.global_queue || []} tab={search.tab || 'active'} open={expanded.includes(r.repo)} toggle={() => update({ expanded: expanded.includes(r.repo) ? expanded.filter(n => n !== r.repo).join(',') : [...expanded, r.repo].join(',') })} />)
          )}
          <EventTimeline events={board.events || []} filter={search.events || 'important'} repo={search.repo || ''} repos={board.repos.map(r => r.repo)} update={update} />
        </>
      ) : null}
    </div>
  )
}

export function RuntimeHeader({ runtime }: { runtime: Board['runtime'] }) {
  const s = runtime.scheduler
  return <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs border rounded-lg px-3 py-2 mb-4" aria-label="Runtime">
    <span>{runtime.version || 'Version unavailable'}</span>
    <span>Executor: {runtime.execution.profile || 'unavailable'} · backend: {runtime.execution.backend ? runtime.execution.backend + ' (last observed)' : 'unavailable'}{runtime.execution.model && <> · model: {runtime.execution.model}</>}{runtime.execution.service_tier && <> · tier: {runtime.execution.service_tier}</>}</span>
    <span>Agent slots: {s.agent_slots_used} / {s.agent_slots_total}</span>
    <span>{s.paused ? 'paused' : s.status} {s.status === 'running' ? (s.started_at ? '· started ' + timeAgo(s.started_at) : '· age unavailable') : ''}</span>
    <span>Last pass: {s.last_result || 'unavailable'} {s.last_ended_at && timeAgo(s.last_ended_at)}</span>
    <span className="rounded bg-secondary px-1">Read-only</span>
    {!runtime.execution.ready && <span className="text-red-700">{runtime.execution.error}</span>}
  </div>
}

export function Attention({ board, repoInfo }: { board: Board; repoInfo: RepoInfoMap }) {
  const rows = board.repos.flatMap(repo => repo.items.flatMap(item => (item.attention || []).map(reason => ({
    repo: repo.repo, issue: item.issue, reason, time: item.updated_at, url: item.pr?.url || issueUrl(repo.repo, item.issue, repoInfo),
  }))))
  for (const repo of board.repos) if (repo.error) rows.push({ repo: repo.repo, issue: 0, reason: (repo.stale ? 'Stale snapshot: ' : '') + repo.error, time: board.generated_at, url: repoUrl(repo.repo, repoInfo) })
  // Only the latest lock outcome for a repository is actionable.
  const locks = new Map<string, PipelineEvent>()
  for (const e of board.events || []) {
    if (e.event === 'run/lock_timeout' || e.event === 'run/end') locks.set(e.fields.repo || '', e)
  }
  for (const [repo, e] of locks) if (e.event === 'run/lock_timeout') rows.push({ repo, issue: Number(e.fields.issue) || 0, reason: 'Lock wait timed out: ' + (e.fields.reason || ''), time: e.time, url: e.fields.issue ? issueUrl(repo, Number(e.fields.issue), repoInfo) : repoUrl(repo, repoInfo) })
  return <section className="mb-4" aria-label="Needs attention">
    {rows.length ? <><h2 className="font-semibold text-sm mb-2">Needs attention ({rows.length})</h2><ul className="border border-amber-200 rounded-lg divide-y">
      {rows.map((row, i) => <li key={i} className="p-2 text-xs flex flex-wrap gap-2 break-words"><span className="font-mono break-all">{row.repo}{row.issue ? ' #' + row.issue : ''}</span><span>{row.reason}</span><span className="text-muted-foreground">{timeAgo(row.time)}</span>{row.url && <a className="text-brand underline" href={row.url} target="_blank" rel="noreferrer">Open</a>}</li>)}
    </ul></> : <p className="text-sm text-muted-foreground">Nothing needs you</p>}
  </section>
}

export function RepoSection({ board, repoInfo, queue, tab, open, toggle }: { board: RepoBoard; repoInfo: RepoInfoMap; queue: GlobalQueueEntry[]; tab: NonNullable<Preferences['tab']>; open: boolean; toggle: () => void }) {
  const [showAll, setShowAll] = useState(false)
  const positions = new Map(queue.filter(q => q.repo === board.repo).map(q => [q.number, q.position]))
  const items = board.items.map(item => ({ ...item, in_queue: positions.has(item.issue), queue_position: positions.get(item.issue) }))
    .sort((a, b) => (a.queue_position || Infinity) - (b.queue_position || Infinity))
  const active = items.filter(i => i.stage !== 'merged' && i.state !== 'closed')
  const inFlight = active.filter(i => i.run?.live || i.acceptance?.running || i.pr?.state === 'open')
  const lastMerge = items.map(i => i.delivery?.merged_at || '').sort().slice(-1)[0]
  const filtered = tab === 'active' ? active : tab === 'delivered' ? items.filter(i => i.stage === 'merged' && i.delivery?.merged_at && Date.now() - Date.parse(i.delivery.merged_at) <= 7 * 86400000) : items
  let queued = 0
  const visible = filtered.filter(i => !i.in_queue || ++queued <= 6 || showAll)
  const hidden = filtered.length - visible.length
  const flags = [board.auto_merge ? 'auto-merge' : 'manual merge', board.ci_required ? 'CI' : 'CI off', board.acceptance_required ? 'acceptance' : 'acceptance off']
  return <section className="border rounded-xl mb-3 overflow-hidden">
    <button onClick={toggle} aria-expanded={open} className="w-full flex flex-wrap items-center gap-2 p-3 text-left text-xs">
      <ChevronRight className={cn('w-4 h-4 shrink-0', open && 'rotate-90')} />
      <span className="font-mono font-semibold break-all">{board.repo}</span>
      {flags.map(f => <span key={f} className="bg-secondary rounded px-1">{f}</span>)}
      {!board.enabled && <span>disabled</span>}
      <span>{inFlight.length}/{board.max_in_flight} in flight{inFlight.length ? ': ' + [...new Set(inFlight.map(i => STAGE_LABEL[i.stage]))].join(', ') : ''}</span>
      <span>{active.filter(i => i.in_queue).length} queued</span>
      <span className="text-muted-foreground">last merge {timeAgo(lastMerge)}</span>
      {board.stale && <span className="rounded border border-amber-200 bg-amber-50 px-1 text-amber-900">stale</span>}
    </button>
    {board.error && <div role="alert" className="border-t border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 break-words">{board.stale ? 'Showing the last snapshot. Refresh failed: ' : ''}{board.error}</div>}
    {open && <div className="border-t">
      {visible.length ? visible.map(item => <ItemRows key={item.issue} item={item} repo={board.repo} repoInfo={repoInfo} />) : <p className="p-3 text-sm text-muted-foreground">No items in this view.</p>}
      {hidden > 0 && <button className="p-3 text-sm text-brand" onClick={() => setShowAll(true)}>+{hidden} more</button>}
      {showAll && queued > 6 && <button className="p-3 text-sm text-brand" onClick={() => setShowAll(false)}>Show head + next 5</button>}
    </div>}
  </section>
}

function ItemRows({ item, repo, repoInfo }: { item: Item; repo: string; repoInfo: RepoInfoMap }) {
  const [open, setOpen] = useState(false)
  const panelId = `pipeline-${repo.replace(/\W+/g, '-')}-${item.issue}`
  return <div className="border-b last:border-b-0 p-3 text-xs">
    <div className="flex flex-wrap items-center gap-2 min-w-0">
      <button onClick={() => setOpen(o => !o)} aria-expanded={open} aria-controls={panelId} aria-label={`Details for issue ${item.issue}`} className="p-1 rounded hover:bg-secondary"><ChevronRight className={cn('w-4 h-4', open && 'rotate-90')} /></button>
      {item.queue_position && <span className="text-muted-foreground">{item.queue_position}.</span>}
      <a href={issueUrl(repo, item.issue, repoInfo)} className="text-brand font-mono" target="_blank" rel="noreferrer">#{item.issue}</a>
      <span className="flex-1 min-w-0 truncate" title={item.title}>{item.title}</span>
      <StageChip stage={item.stage} />
      <span className="text-muted-foreground">{timeAgo(item.updated_at)}</span>
    </div>
    <p className="mt-1 truncate text-muted-foreground" title={item.why}>{item.why}</p>
    {item.delivery && <p className="mt-1 break-all"><a href={item.delivery.pr_url} className="text-brand">PR #{item.delivery.pr}</a> · {shortSha(item.delivery.merge_commit_sha)} · {item.delivery.merged_at || 'time unavailable'}</p>}
    {open && <div id={panelId} className="mt-3"><ItemDetail item={item} /></div>}
  </div>
}

function ItemDetail({ item }: { item: Item }) {
  const { pr, acceptance: a, delivery, run } = item
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs">
      <DetailBlock title="Evidence">
        {!item.evidence?.length ? <span className="text-muted-foreground">none</span> : (
          <ul className="list-disc pl-4 space-y-0.5">
            {(item.evidence || []).map((e, i) => <li key={i}>{e}</li>)}
          </ul>
        )}
        {run && (
          <p className="mt-1 text-muted-foreground">
            {run.operator} run: {run.status}
            {run.stage ? ` (${run.stage})` : ''} · started {timeAgo(run.started_at)}
            {run.live ? ' · live' : ''}
          </p>
        )}
      </DetailBlock>

      <DetailBlock title="Pull request">
        {pr ? (
          <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-0.5">
            <dt className="text-muted-foreground">PR</dt>
            <dd>
              {pr.url ? (
                <a href={pr.url} target="_blank" rel="noreferrer noopener" className="text-brand hover:underline inline-flex items-center gap-1">
                  #{pr.number} <ExternalLink className="w-3 h-3" />
                </a>
              ) : `#${pr.number}`}{' '}
              <span className="text-muted-foreground">{pr.state}{pr.draft ? ' (draft)' : ''}</span>
            </dd>
            <dt className="text-muted-foreground">Head</dt>
            <dd className="font-mono">{shortSha(pr.head_sha)}{pr.head_branch ? ` · ${pr.head_branch}` : ''}</dd>
            <dt className="text-muted-foreground">CI</dt>
            <dd>{pr.ci || '—'}{pr.ci_error ? <span className="text-red-700"> ({pr.ci_error})</span> : null}</dd>
            <dt className="text-muted-foreground">Mergeable</dt>
            <dd>{pr.mergeable || '—'}</dd>
          </dl>
        ) : <span className="text-muted-foreground">No pull request yet.</span>}
      </DetailBlock>

      {a && (
        <DetailBlock title="Acceptance">
          <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-0.5">
            <dt className="text-muted-foreground">Verdict</dt>
            <dd>
              {a.running ? 'reviewing…' : a.latest_is_infra ? 'no verdict (infrastructure error)' : a.verdict || 'none yet'}
              {a.verdict === 'passed' && !a.valid ? ' (stale)' : ''}
            </dd>
            <dt className="text-muted-foreground">Attempts</dt>
            <dd>{a.attempts} total · {a.infra_attempts}{a.max_infra_attempts ? `/${a.max_infra_attempts}` : ''} infrastructure</dd>
            {a.next_retry_at && (
              <>
                <dt className="text-muted-foreground">Next retry</dt>
                <dd>{timeAgo(a.next_retry_at)} <span className="text-muted-foreground">({a.next_retry_at})</span></dd>
              </>
            )}
            {a.completed_at && (
              <>
                <dt className="text-muted-foreground">Completed</dt>
                <dd>{timeAgo(a.completed_at)}</dd>
              </>
            )}
            {(a.planner_session || a.reviewer_session) && (
              <>
                <dt className="text-muted-foreground">Sessions</dt>
                <dd className="font-mono break-all">planner {a.planner_session || '—'} · reviewer {a.reviewer_session || '—'}</dd>
              </>
            )}
          </dl>
          {a.error && <p className="mt-1 text-red-700">{a.error}</p>}
          {a.summary && <p className="mt-1 whitespace-pre-wrap text-foreground">{a.summary}</p>}
        </DetailBlock>
      )}

      {delivery && (
        <DetailBlock title="Delivery">
          <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-0.5">
            <dt className="text-muted-foreground">Merge commit</dt>
            <dd className="font-mono">{shortSha(delivery.merge_commit_sha)}</dd>
            <dt className="text-muted-foreground">Merged</dt>
            <dd>{delivery.merged_at ? `${timeAgo(delivery.merged_at)} (${delivery.merged_at})` : '—'}</dd>
          </dl>
        </DetailBlock>
      )}
    </div>
  )
}

function DetailBlock({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="bg-card border border-border rounded-lg p-3">
      <h3 className="text-[11px] uppercase tracking-wide text-muted-foreground mb-1.5 font-semibold">{title}</h3>
      {children}
    </div>
  )
}

export function foldEvents(events: PipelineEvent[]) {
  const folded: (PipelineEvent & { count: number; since: string })[] = []
  for (const e of events) {
    const prev = folded[folded.length - 1]
    const identity = (event: PipelineEvent) => JSON.stringify([event.event, event.level, Object.entries(event.fields).sort(([a], [b]) => a.localeCompare(b))])
    if (prev && identity(prev) === identity(e)) { prev.count++; prev.time = e.time }
    else folded.push({ ...e, count: 1, since: e.time })
  }
  return folded
}

export function importantEvent(e: PipelineEvent) {
  return e.level === 'warn' || e.level === 'error' || /harvest_end|automerge_merged|automerge_success|self_upgrade|lock_timeout|queue_(pick|wait|blocked)|queue\/(pick|wait|blocked|skip|park|unpark)/.test(e.event) || ['failed', 'error', 'merged'].includes(e.fields.status) || Number(e.fields.merged) > 0
}

export function EventTimeline({ events, filter, repo, repos, update }: { events: PipelineEvent[]; filter: string; repo: string; repos: string[]; update: (p: Preferences) => void }) {
  const rows = foldEvents(events).filter(e => (!repo || e.fields.repo === repo) && (filter === 'all' || importantEvent(e))).reverse()
  return <details className="border rounded-xl p-3 mb-4">
    <summary className="text-sm font-semibold cursor-pointer">Events ({rows.length})</summary>
    <div className="flex flex-wrap gap-2 my-3 text-xs">
      <label>Show <select aria-label="Event importance" value={filter} onChange={e => update({ events: e.target.value as Preferences['events'] })} className="bg-card border rounded p-1"><option value="important">Important</option><option value="all">All</option></select></label>
      <label>Repo <select aria-label="Event repository" className="max-w-full bg-card border rounded p-1" value={repo} onChange={e => update({ repo: e.target.value })}><option value="">All repositories</option>{repos.map(r => <option key={r}>{r}</option>)}</select></label>
    </div>
    <ol className="divide-y max-h-96 overflow-auto text-xs">
      {rows.map((e, i) => <li key={i} className="py-2 flex flex-wrap gap-2 break-all">
        <time className="text-muted-foreground">{timeAgo(e.time)}</time>
        <span className={cn('font-mono', ['warn', 'error'].includes(e.level) && 'text-amber-700')}>{e.event.replace(/^run\//, '')}{e.fields.issue ? ' #' + e.fields.issue : ''}{e.count > 1 ? ` ×${e.count} since ${new Date(e.since).toLocaleTimeString()}` : ''}</span>
        <span className="text-muted-foreground">{Object.entries(e.fields).map(([k, v]) => `${k}=${v}`).join(' ')}</span>
      </li>)}
    </ol>
    {!rows.length && <p className="text-xs text-muted-foreground">No matching events.</p>}
  </details>
}
