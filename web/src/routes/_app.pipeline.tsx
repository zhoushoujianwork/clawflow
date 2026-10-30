import { ActivityTimeline } from '../components/ActivityTimeline'
export { EventTimeline, foldEvents, importantEvent } from '../components/ActivityTimeline'
import { ExecutionCapacity } from '../components/ExecutionCapacity'
import { Attention } from '../components/Attention'
export { Attention } from '../components/Attention'
import { WorkSummary, type WorkFilter, type WorkItem, type SourceHealth } from '../components/WorkSummary'
import { WorkDetail, workDetailKey, type ExecutionAttempt } from '../components/WorkDetail'
import { WorkSearchControls } from '../components/WorkSearchControls'
import { workSearch, type WorkSearch } from '../lib/workSearch'
import type { ProjectEntry } from '../lib/projectMembership'
import { WorkList } from '../components/WorkList'
import { ScheduledChecksPanel, useScheduledChecks } from "../components/ScheduledChecksPanel"
import { createFileRoute } from '@tanstack/react-router'
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
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

export interface WorkerPool {
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

type Preferences = WorkSearch & { detail?: string; work?: WorkFilter; tab?: 'active' | 'delivered' | 'all'; expanded?: string; events?: 'important' | 'all'; repo?: string; activityIssue?: string }
export function preferences(s: Record<string, unknown>): Preferences {
  // The router JSON-decodes hand-written deep links before search validation.
  const detail = Array.isArray(s.detail) && s.detail.length === 2 && typeof s.detail[0] === 'string' && Number.isInteger(s.detail[1]) && s.detail[1] > 0
    ? workDetailKey(s.detail[0], s.detail[1]) : s.detail
  return {
    ...workSearch(s),
    detail: typeof detail === 'string' && detail.length < 512 ? detail : undefined,
    work: ['running', 'waiting', 'attention', 'delivered'].includes(String(s.work)) ? s.work as WorkFilter : undefined,
    tab: s.tab === 'active' || s.tab === 'delivered' || s.tab === 'all' ? s.tab : undefined,
    expanded: typeof s.expanded === 'string' ? s.expanded : undefined,
    events: s.events === 'important' || s.events === 'all' ? s.events : undefined,
    repo: typeof s.repo === 'string' ? s.repo : undefined,
    activityIssue: typeof s.activityIssue === 'string' && /^\d{1,10}$/.test(s.activityIssue) ? s.activityIssue : undefined,
  }
}
// Visible tab labels double as accessible names, so they are real words, not CSS-capitalized ids.
const PIPELINE_TAB_LABELS = { active: 'Active', running: 'Running', waiting: 'Waiting', delivered: 'Delivered' } as const

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
  const scheduled = useScheduledChecks()
  useDocumentTitle('Work')
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  function update(next: Preferences) {
    void navigate({ search: { ...search, ...next }, resetScroll: false })
  }
  const [projects, setProjects] = useState<ProjectEntry[]>([])
  const [projectError, setProjectError] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    fetch('/data/projects.json', { cache: 'no-store', signal: controller.signal })
      .then(async r => { if (!r.ok) throw new Error('Projects unavailable'); return r.json() })
      .then(data => {
        if (!Array.isArray(data)) throw new Error('Invalid projects')
        setProjects(data.filter((p: ProjectEntry) => typeof p?.name === 'string' && Array.isArray(p.repos)))
      })
      .catch(() => { if (!controller.signal.aborted) setProjectError(true) })
    return () => controller.abort()
  }, [])

  const repoInfo = useRepoInfoMap()
  const [board, setBoard] = useState<Board | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  const scrollAnchor = useRef<{ key?: string; top: number; y: number }>()
  useLayoutEffect(() => {
    const anchor = scrollAnchor.current
    if (!anchor) return
    scrollAnchor.current = undefined
    const row = [...document.querySelectorAll<HTMLElement>('[data-work-key]')].find(el => el.dataset.workKey === anchor.key)
    window.scrollTo(0, row ? window.scrollY + row.getBoundingClientRect().top - anchor.top : anchor.y)
  }, [board])

  const load = useCallback(async () => {
    setRefreshing(true)
    try {
      const r = await fetch('/api/pipeline', { cache: 'no-store' })
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.error || `HTTP ${r.status}`)
      }
      const next = (await r.json()) as Board
      const row = [...document.querySelectorAll<HTMLElement>('[data-work-key]')].find(el => el.getBoundingClientRect().bottom > 0)
      scrollAnchor.current = { key: row?.dataset.workKey, top: row?.getBoundingClientRect().top || 0, y: window.scrollY }
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
      <ScheduledChecksPanel data={scheduled.data} error={scheduled.error} />

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
            {!board.worker_pool && <p>{board.worker_pool_error || (board.worker_pool === null ? 'Coordinator disabled' : 'Worker source unavailable')}</p>}
          </details>
          <ExecutionCapacity board={board} failed={!!error} />
          <Attention board={board} schedules={scheduled.data} repoInfo={repoInfo} />
          <div className="flex flex-wrap gap-2 mb-3" role="tablist" aria-label="Pipeline items">
            {(['active', 'running', 'waiting', 'delivered'] as const).map(tab => {
              const selected = search.work || (search.tab === 'delivered' ? 'delivered' : 'active')
              return <button key={tab} role="tab" aria-selected={selected === tab} onClick={() => update({ tab: tab === 'delivered' ? 'delivered' : 'active', work: tab === 'active' ? undefined : tab })} className={cn('px-3 py-1 rounded border text-sm', selected === tab && 'bg-secondary')}>{PIPELINE_TAB_LABELS[tab]}</button>
            })}
          </div>
          <WorkSearchControls search={search} repos={board.repos} projects={projects} projectError={projectError} update={update} />
          {board.repos.length === 0 ? (
            <div className="bg-card border border-border rounded-xl p-10 text-center">
              <p role="status" className="text-sm">0 results</p>
              <p className="text-base font-semibold text-foreground mb-1">{board.generated_at ? 'No repository is tracked yet' : 'Preparing pipeline snapshot…'}</p>
              {board.generated_at && <p className="text-sm text-muted-foreground">
                Enable a repository with <code>clawflow repo enable</code> to see its eligible work.
              </p>}
            </div>
          ) : (
            <WorkList repos={board.repos} queue={board.global_queue || []} repoInfo={repoInfo}
              query={search} projects={projects}
              filter={search.work || (search.tab === 'delivered' ? 'delivered' : 'active')}
              onOpen={(item, repo) => update({ detail: workDetailKey(repo, item.issue) })}
              renderDetails={(item, repo) => <><ItemDetail item={item} /><PilotTimeline actions={itemPilotActions(board.pilot_actions || [], repo, item)} /></>} />
          )}
          <ActivityTimeline board={board} filter={search.events || 'important'} repo={search.repo || ''} issue={search.activityIssue || ''} update={update} />
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
