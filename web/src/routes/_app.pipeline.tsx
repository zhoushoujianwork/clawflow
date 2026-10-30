import { Attention } from '../components/Attention'
export { Attention } from '../components/Attention'
import { WorkSummary, type WorkFilter, type WorkItem, type SourceHealth } from '../components/WorkSummary'
import { WorkDetail, workDetailKey, type ExecutionAttempt } from '../components/WorkDetail'
import { WorkList } from '../components/WorkList'
import { ScheduledChecksPanel } from "../components/ScheduledChecksPanel"
import { createFileRoute } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { ExternalLink, Loader2, RefreshCw } from 'lucide-react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { cn } from '../lib/utils'
import { useRepoInfoMap } from '../lib/vcsUrls'

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
  updated_at?: string
  head_sha?: string
  ci?: string
  ci_error?: string
  mergeable?: string
}

interface AcceptanceInfo {
  unstructured?: boolean
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

export interface Item extends WorkItem {
  executions?: ExecutionAttempt[]
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

export interface RepoBoard {
  hosting_platform?: string
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

export interface PipelineEvent {
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

export interface PilotAction {
  created_issue?: number
  run_id: string
  actor: string
  action: { id: string; type: string; repo: string; issue?: number; pr?: number; body?: string; head?: string; followups?: string[] }
  outcome: string
  at: string
  reason?: string
}

interface WorkerPool {
  generated_at: string
  workers: { name: string; tags: string[]; roles: string[]; live: boolean; last_seen: string | null; last_heartbeat: string | null; running_tasks: string[] }[]
  tasks: { id: string; repo: string; issue: number; pr: number; kind: string; state: string; stage: string; waiting_reason?: string; application_error?: string; application_attempts: number; tags: string[]; priority: number; worker: string; lease_expires_at: string | null; attempt: number }[]
  throughput: { since: string; done: number; failed: number }
}

interface APIUsage {
 captured_at: string
 calls_by_stage: Record<string, number>
 budgets: Record<string, {limit: number; remaining: number; reset: string; backoff: string}>
}
export interface Board {
  sources?: SourceHealth[]
 vcs_usage?: APIUsage
  worker_pool?: WorkerPool | null
  worker_pool_error?: string
  pilot_actions?: PilotAction[]
  pilot_error?: string
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

type Preferences = { detail?: string; work?: WorkFilter; tab?: 'active' | 'delivered' | 'all'; expanded?: string; events?: 'important' | 'all'; repo?: string }
export function preferences(s: Record<string, unknown>): Preferences {
  return {
    detail: typeof s.detail === 'string' && s.detail.length < 512 ? s.detail : undefined,
    work: ['running', 'waiting', 'attention', 'delivered'].includes(String(s.work)) ? s.work as WorkFilter : undefined,
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

function PipelinePage() {
  useDocumentTitle('Work')
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const [restored, setRestored] = useState(false)
  useEffect(() => {
    if (restored) return
    setRestored(true)
    try {
      const stored = preferences(JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}'))
      delete stored.detail
      if (stored.tab === 'all') stored.tab = 'active'
      if (stored.events === 'all') stored.events = 'important'
      const next = Object.fromEntries(Object.entries({ ...stored, ...Object.fromEntries(Object.entries(search).filter(([, v]) => v !== undefined)) }))
      void navigate({ search: next, replace: true })
    } catch { /* Storage may be disabled. URL remains usable. */ }
  }, [restored, search, navigate])
  function update(next: Preferences) {
    const value = { ...search, ...next }
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...value, detail: undefined, tab: value.tab === 'all' ? 'active' : value.tab, events: value.events === 'all' ? 'important' : value.events })) } catch { /* URL still persists. */ }
    void navigate({ search: value })
  }

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

  const selected = (() => {
    try {
      const [repo, issue] = JSON.parse(search.detail || 'null')
      return typeof repo === 'string' && Number.isInteger(issue) && issue > 0 ? { repo, issue } : null
    } catch { return null }
  })()
  const selectedItem = board?.repos.find(r => r.repo === selected?.repo)?.items.find(i => i.issue === selected?.issue)
  return (
    <div className="max-w-6xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-4 flex-wrap gap-3">
        <div>
          <h1 id="work-page-title" tabIndex={-1} className="text-2xl font-bold text-foreground">Work</h1>
          <p className="text-xs text-muted-foreground mt-1">
            Execution and delivery across tracked repositories. Read-only.
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground tabular-nums">
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

      {selected && <WorkDetail key={search.detail} repo={selected.repo} issue={selected.issue} item={selectedItem} onClose={() => update({ detail: undefined })}>
        <p className="text-xs my-2">Pipeline snapshot: {board?.generated_at || 'unknown'}{board?.stale ? ' · stale' : ''}</p>
        {selectedItem && <><ItemDetail item={selectedItem} /><PilotTimeline actions={itemPilotActions(board?.pilot_actions || [], selected.repo, selectedItem)} /></>}
      </WorkDetail>}
      <WorkSummary repos={board?.repos} sources={board?.sources} generatedAt={board?.generated_at} stale={board?.stale} refreshing={refreshing || board?.refreshing} failed={!!error} filter={search.work} onFilter={work => update({ work, tab: work === 'delivered' ? 'delivered' : 'active' })} />
      <ScheduledChecksPanel />

      {loading && !board ? (
        <p className="text-sm text-muted-foreground text-center py-8 flex items-center justify-center gap-2">
          <Loader2 className="w-4 h-4 animate-spin" /> Loading…
        </p>
      ) : board ? (
        <>
          <details className="mb-4 border rounded-lg p-3 text-sm">
            <summary className="cursor-pointer">System status</summary>
            {error && <p role="alert" className="text-red-800 break-words">Could not refresh work: {error}</p>}
            <RuntimeHeader runtime={board.runtime} />
            <APIUsagePanel usage={board.vcs_usage} />
          </details>
          <WorkerPoolPanel pool={board.worker_pool} error={board.worker_pool_error} />
          <Attention board={board} repoInfo={repoInfo} />
          <div className="flex flex-wrap gap-2 mb-3" role="tablist" aria-label="Pipeline items">
            {(['active', 'running', 'waiting', 'delivered'] as const).map(tab => {
              const selected = search.work || (search.tab === 'delivered' ? 'delivered' : 'active')
              return <button key={tab} role="tab" aria-selected={selected === tab} onClick={() => update({ tab: tab === 'delivered' ? 'delivered' : 'active', work: tab === 'active' ? undefined : tab })} className={cn('px-3 py-1 rounded border text-sm capitalize', selected === tab && 'bg-secondary')}>{tab}</button>
            })}
          </div>
          {board.repos.length === 0 ? (
            <div className="bg-card border border-border rounded-xl p-10 text-center">
              <p className="text-base font-semibold text-foreground mb-1">{board.generated_at ? 'No repository is tracked yet' : 'Preparing pipeline snapshot…'}</p>
              {board.generated_at && <p className="text-sm text-muted-foreground">
                Enable a repository with <code>clawflow repo enable</code> to see its eligible work.
              </p>}
            </div>
          ) : (
            <WorkList repos={board.repos} queue={board.global_queue || []} repoInfo={repoInfo}
              filter={search.work || (search.tab === 'delivered' ? 'delivered' : 'active')}
              onOpen={(item, repo) => update({ detail: workDetailKey(repo, item.issue) })}
              renderDetails={(item, repo) => <><ItemDetail item={item} /><PilotTimeline actions={itemPilotActions(board.pilot_actions || [], repo, item)} /></>} />
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
    <span>Latest recorded executor (one execution only): profile {runtime.execution.profile || 'unavailable'} · backend: {runtime.execution.backend || 'unavailable'}{runtime.execution.model && <> · model: {runtime.execution.model}</>}{runtime.execution.service_tier && <> · tier: {runtime.execution.service_tier}</>}</span>
    <span>Agent slots: {s.agent_slots_used ?? 'unknown'} / {s.agent_slots_total ?? 'unknown'}</span>
    <span>{s.paused ? 'paused' : s.status} {s.status === 'running' ? (s.started_at ? '· started ' + timeAgo(s.started_at) : '· age unavailable') : ''}</span>
    <span>Last pass: {s.last_result || 'unavailable'} {s.last_ended_at && timeAgo(s.last_ended_at)}</span>
    <span className="rounded bg-secondary px-1">Read-only</span>
    {!runtime.execution.ready && <span className="text-red-700">{runtime.execution.error}</span>}
  </div>
}

export function ItemDetail({ item }: { item: Item }) {
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
        <p>Source: VCS PR/CI · observed PR update: {pr?.updated_at || 'unknown'} · CI observation time: unknown</p>
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
          <p>Source: ClawFlow independent acceptance · completed: {a.completed_at || 'unknown'}</p>
          <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-0.5">
            <dt className="text-muted-foreground">Verdict</dt>
            <dd>
              {a.running ? 'reviewing…' : a.latest_is_infra ? 'no verdict (infrastructure error)' : a.verdict || 'none yet'}
              {a.unstructured ? ' (unstructured findings)' : ''}
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

      {!a && <DetailBlock title="Acceptance">Source: ClawFlow independent acceptance · verdict and timestamp unknown</DetailBlock>}
      {!delivery && <DetailBlock title="Delivery">Source: VCS merge evidence · No merge recorded. Timestamp unknown.</DetailBlock>}
      {delivery && (
        <DetailBlock title="Delivery">
          <p>Source: VCS merge evidence · timestamp: {delivery.merged_at || 'unknown'}</p>
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

function pilotSummary(a: PilotAction) {
  return `Pilot ${a.action.type} · ${a.outcome}${a.action.body ? ': ' + a.action.body : ''}${a.reason ? ' · ' + a.reason : ''}`
}

export function PilotTimeline({ actions }: { actions: PilotAction[] }) {
  if (!actions.length) return null
  return <DetailBlock title="Pilot timeline"><ol className="space-y-2">
    {actions.map(a => <li key={a.run_id + '/' + a.action.id} className="whitespace-pre-wrap break-words">
      <time>{a.at}</time> · {pilotSummary(a)}
      <div className="text-muted-foreground">{a.actor} · run {a.run_id}{a.action.head && <> · reviewed head <code>{a.action.head}</code></>}{a.action.followups?.length ? ' · follow-ups: ' + a.action.followups.join(', ') : ''}</div>
    </li>)}
  </ol></DetailBlock>
}

export function itemPilotActions(actions: PilotAction[], repo: string, item: Item) {
  return actions.filter(a => a.action.repo === repo &&
    ((a.action.issue && a.action.issue === item.issue) || (a.created_issue && a.created_issue === item.issue) || (a.action.pr && a.action.pr === item.pr?.number)))
}

function WorkerPoolPanel({ pool, error }: { pool?: WorkerPool | null; error?: string }) {
  return <section aria-label="Worker pool" className="border rounded-xl p-3 mb-4 text-xs">
    <h2 className="font-semibold text-sm mb-2">Worker pool · Read-only</h2>
    {error ? <p role="alert">{error}</p> : !pool ? <p className="text-muted-foreground">Coordinator disabled</p> : <>
      <p className="mb-3">Last hour: {pool.throughput.done} done · {pool.throughput.failed} failed · updated {timeAgo(pool.generated_at)}</p>
      <p className="text-muted-foreground mb-2">Live means an authenticated worker request within 120 seconds. Heartbeats renew task leases.</p>
      <div className="overflow-x-auto"><table className="w-full text-left">
        <caption className="text-left font-semibold">Workers ({pool.workers.length})</caption>
        <thead><tr>{['Name', 'Tags', 'Roles', 'Status', 'Last seen', 'Last heartbeat', 'Running tasks'].map(h => <th key={h} className="p-2">{h}</th>)}</tr></thead>
        <tbody>{pool.workers.map(w => <tr key={w.name} className="border-t">
          <td className="p-2">{w.name}</td><td className="p-2">{w.tags.join(', ') || '—'}</td><td className="p-2">{w.roles.join(', ') || '—'}</td>
          <td className="p-2">{w.live ? 'live' : 'stale'}</td><td className="p-2" title={w.last_seen || ''}>{timeAgo(w.last_seen || undefined)}</td>
          <td className="p-2" title={w.last_heartbeat || ''}>{timeAgo(w.last_heartbeat || undefined)}</td>
          <td className="p-2 break-all">{w.running_tasks.join(', ') || '—'}</td>
        </tr>)}</tbody>
      </table></div>
      {!pool.workers.length && <p className="py-2 text-muted-foreground">No registered workers.</p>}
      <div className="overflow-x-auto mt-3"><table className="w-full text-left">
        <caption className="text-left font-semibold">Tasks ({pool.tasks.length})</caption>
        <thead><tr>{['Task / repository', 'Kind', 'State / stage', 'Tags', 'Priority', 'Worker', 'Lease expiry', 'Retries'].map(h => <th key={h} className="p-2">{h}</th>)}</tr></thead>
        <tbody>{pool.tasks.map(t => <tr key={t.id} className="border-t">
          <td className="p-2 break-all" title={t.id}>{t.id.slice(0, 12)} · {t.repo} {t.pr ? `PR #${t.pr}` : `#${t.issue}`}</td>
          <td className="p-2">{t.kind}</td><td className="p-2">{t.state}{t.stage !== t.state ? ` / ${t.stage}` : ''}{t.waiting_reason && <p>{t.waiting_reason}</p>}{t.application_error && <p className="text-red-600">结果应用失败（{t.application_attempts}/3）：{t.application_error}</p>}</td>
          <td className="p-2">{t.tags.join(', ') || '—'}</td><td className="p-2">{t.priority}</td><td className="p-2">{t.worker || '—'}</td>
          <td className="p-2">{t.lease_expires_at ? new Date(t.lease_expires_at).toLocaleString() : '—'}</td><td className="p-2">{t.attempt}</td>
        </tr>)}</tbody>
      </table></div>
      {!pool.tasks.length && <p className="py-2 text-muted-foreground">No tasks.</p>}
    </>}
  </section>
}

export function APIUsagePanel({ usage }: { usage?: APIUsage }) {
  if (!usage) return null
  return <section aria-label="GitHub API budget" className="border rounded p-3 mb-3 text-sm">
    <p>GitHub API · 最近调度轮次 {new Date(usage.captured_at).toLocaleString()}</p>
    {Object.entries(usage.budgets).map(([host, b]) => <p key={host}>
      {host} · 剩余 {b.remaining}/{b.limit} · 重置 {new Date(b.reset).toLocaleString()}
      {Date.parse(b.backoff) > Date.now() && ` · 退避至 ${new Date(b.backoff).toLocaleString()}`}
    </p>)}
    <p>各阶段请求：{Object.entries(usage.calls_by_stage).map(([stage, calls]) => `${stage}: ${calls}`).join(' · ') || '0'}</p>
  </section>
}
