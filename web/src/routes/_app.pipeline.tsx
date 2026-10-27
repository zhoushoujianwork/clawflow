import { createFileRoute } from '@tanstack/react-router'
import { useCallback, useEffect, useMemo, useState } from 'react'
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

interface QueueDecision {
  action: 'pick' | 'wait' | 'blocked'
  issue?: number
  reason?: string
  at: string
}

interface QueueEntry {
  position: number
  number: number
  title: string
  state: string
  action?: string
  reason?: string
}

interface Queue {
  issue: number
  entries: QueueEntry[]
  decision?: QueueDecision
  error?: string
}

interface RepoBoard {
  repo: string
  enabled: boolean
  platform: string
  auto_merge: boolean
  ci_required: boolean
  acceptance_required: boolean
  queue_issue?: number
  queue?: Queue
  items: Item[]
  error?: string
}

interface PipelineEvent {
  time: string
  level: string
  event: string
  fields: Record<string, string>
}

interface Board {
  generated_at: string
  repos: RepoBoard[]
  events: PipelineEvent[]
  runtime: {
    version: string
    scheduler: { status: string; interval_minutes: number; paused: boolean; next_fire_unix_ms?: number }
    execution: { ready: boolean; error?: string }
  }
}

export const Route = createFileRoute('/_app/pipeline')({
  component: PipelinePage,
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
  rejected: 'Rejected',
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
      setBoard((await r.json()) as Board)
      setError(null)
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
            Issue → PR → CI → acceptance → merge, per repository with auto-merge or a work queue. Read-only.
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground tabular-nums">
          {board && <span>updated {timeAgo(board.generated_at)}</span>}
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
          {board.repos.length === 0 ? (
            <div className="bg-card border border-border rounded-xl p-10 text-center">
              <p className="text-base font-semibold text-foreground mb-1">No repository is tracked yet</p>
              <p className="text-sm text-muted-foreground">
                Enable <code className="px-1.5 py-0.5 bg-secondary rounded text-xs font-mono">auto_merge</code> or set a{' '}
                <code className="px-1.5 py-0.5 bg-secondary rounded text-xs font-mono">queue_issue</code> with{' '}
                <code className="px-1.5 py-0.5 bg-secondary rounded text-xs font-mono">clawflow repo set</code>.
              </p>
            </div>
          ) : (
            board.repos.map(r => <RepoSection key={r.repo} board={r} repoInfo={repoInfo} />)
          )}
          <EventTimeline events={board.events} />
        </>
      ) : null}
    </div>
  )
}

function RuntimeHeader({ runtime }: { runtime: Board['runtime'] }) {
  const s = runtime.scheduler
  const next = s.next_fire_unix_ms ? new Date(s.next_fire_unix_ms).toISOString() : undefined
  const schedulerText = s.status === 'running' ? 'run in progress' : s.paused ? 'paused' : s.interval_minutes > 0 ? `every ${s.interval_minutes}m` : 'manual only'
  return (
    <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 mb-5" aria-label="Runtime">
      <Stat label="Version" value={runtime.version || '—'} mono />
      <Stat
        label="Scheduler"
        value={schedulerText}
        hint={next && !s.paused && s.status !== 'running' ? `next ${timeAgo(next)}` : undefined}
      />
      <Stat
        label="Execution"
        value={runtime.execution.ready ? 'ready' : 'not ready'}
        tone={runtime.execution.ready ? 'ok' : 'bad'}
        hint={runtime.execution.ready ? undefined : runtime.execution.error}
      />
    </div>
  )
}

function Stat({ label, value, hint, mono, tone }: { label: string; value: string; hint?: string; mono?: boolean; tone?: 'ok' | 'bad' }) {
  return (
    <div className="bg-card border border-border rounded-xl p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div
        className={cn(
          'text-lg font-semibold mt-0.5 break-all',
          mono && 'font-mono text-base',
          tone === 'ok' && 'text-green-700',
          tone === 'bad' && 'text-red-700',
          !tone && 'text-foreground',
        )}
      >
        {value}
      </div>
      {hint && <div className="text-[11px] text-muted-foreground mt-0.5">{hint}</div>}
    </div>
  )
}

function RepoSection({ board, repoInfo }: { board: RepoBoard; repoInfo: RepoInfoMap }) {
  const flags = [
    board.auto_merge && 'auto-merge',
    board.ci_required && 'CI required',
    board.acceptance_required && 'acceptance required',
  ].filter(Boolean) as string[]
  return (
    <section className="mb-6">
      <div className="flex items-center gap-2 flex-wrap mb-2">
        <a
          href={repoUrl(board.repo, repoInfo)}
          target="_blank"
          rel="noreferrer noopener"
          className="text-sm font-semibold text-foreground hover:underline font-mono"
        >
          {board.repo}
        </a>
        {!board.enabled && <span className="text-[11px] px-1.5 py-0.5 rounded bg-secondary text-muted-foreground border border-border">disabled</span>}
        {flags.map(f => (
          <span key={f} className="text-[11px] px-1.5 py-0.5 rounded bg-secondary text-muted-foreground">
            {f}
          </span>
        ))}
      </div>

      {board.error && (
        <div role="alert" className="mb-2 px-3 py-2 rounded-lg border border-amber-200 bg-amber-50 text-xs text-amber-900">
          {board.error}
        </div>
      )}

      {board.queue && <QueuePanel queue={board.queue} repo={board.repo} repoInfo={repoInfo} />}

      {board.items.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-4 text-sm text-muted-foreground">
          Nothing in flight. Items appear here when an issue is queued, has a managed PR, or ran recently.
        </div>
      ) : (
        <div className="bg-card border border-border rounded-xl overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-secondary/30 text-xs uppercase text-muted-foreground">
              <tr>
                <th className="w-8" aria-label="Expand" />
                <th className="px-3 py-2 text-left font-semibold">Issue</th>
                <th className="px-3 py-2 text-left font-semibold">Stage</th>
                <th className="px-3 py-2 text-left font-semibold">Why</th>
                <th className="px-3 py-2 text-right font-semibold">Updated</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {board.items.map(it => (
                <ItemRows key={it.issue} item={it} repo={board.repo} repoInfo={repoInfo} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function QueuePanel({ queue, repo, repoInfo }: { queue: Queue; repo: string; repoInfo: RepoInfoMap }) {
  const d = queue.decision
  const open = queue.entries.filter(e => e.state === 'open')
  return (
    <div className="bg-card border border-border rounded-xl p-3 mb-2">
      <div className="flex items-center gap-2 flex-wrap text-xs">
        <span className="font-semibold text-foreground">Queue</span>
        <a href={issueUrl(repo, queue.issue, repoInfo)} target="_blank" rel="noreferrer noopener" className="text-brand hover:underline font-mono">
          #{queue.issue}
        </a>
        <span className="text-muted-foreground">{open.length} open</span>
        {d ? (
          <span
            data-decision={d.action}
            className={cn(
              'px-1.5 py-0.5 rounded border font-medium',
              d.action === 'pick' ? GREEN : d.action === 'blocked' ? RED : AMBER,
            )}
          >
            {d.action}
            {d.issue ? ` #${d.issue}` : ''}
            {d.reason ? `: ${d.reason}` : ''}
          </span>
        ) : (
          <span className="text-muted-foreground">no decision logged yet</span>
        )}
        {d && <span className="text-muted-foreground">{timeAgo(d.at)}</span>}
      </div>
      {queue.error && <p className="text-xs text-red-700 mt-1">Queue unreadable: {queue.error}</p>}
      {open.length > 0 && (
        <ol className="mt-2 space-y-0.5 text-xs">
          {open.map(e => (
            <li key={e.number} className="flex items-center gap-2">
              <span className="w-5 text-right text-muted-foreground tabular-nums">{e.position}.</span>
              <a href={issueUrl(repo, e.number, repoInfo)} target="_blank" rel="noreferrer noopener" className="font-mono text-brand hover:underline">
                #{e.number}
              </a>
              <span className="truncate text-foreground">{e.title}</span>
              {e.action && (
                <span className="text-muted-foreground shrink-0">
                  {e.action}
                  {e.reason ? `: ${e.reason}` : ''}
                </span>
              )}
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}

function ItemRows({ item, repo, repoInfo }: { item: Item; repo: string; repoInfo: RepoInfoMap }) {
  const [open, setOpen] = useState(false)
  const panelId = `pipeline-${repo.replace(/\W+/g, '-')}-${item.issue}`
  return (
    <>
      <tr className="hover:bg-secondary/20">
        <td className="pl-2">
          <button
            onClick={() => setOpen(o => !o)}
            aria-expanded={open}
            aria-controls={panelId}
            aria-label={`${open ? 'Collapse' : 'Expand'} details for issue ${item.issue}`}
            className="p-1 rounded hover:bg-secondary"
          >
            <ChevronRight className={cn('w-4 h-4 transition-transform', open && 'rotate-90')} />
          </button>
        </td>
        <td className="px-3 py-2 align-top">
          <div className="flex items-center gap-2">
            {item.in_queue && item.queue_position ? (
              <span className="text-[11px] text-muted-foreground tabular-nums" title="queue position">
                {item.queue_position}.
              </span>
            ) : null}
            <a href={issueUrl(repo, item.issue, repoInfo)} target="_blank" rel="noreferrer noopener" className="font-mono text-brand hover:underline">
              #{item.issue}
            </a>
            <span className="text-foreground truncate max-w-[22rem]" title={item.title}>
              {item.title}
            </span>
          </div>
        </td>
        <td className="px-3 py-2 align-top">
          <StageChip stage={item.stage} />
        </td>
        <td className="px-3 py-2 align-top text-muted-foreground">{item.why}</td>
        <td className="px-3 py-2 align-top text-right text-muted-foreground tabular-nums whitespace-nowrap">{timeAgo(item.updated_at)}</td>
      </tr>
      {open && (
        <tr id={panelId} className="bg-secondary/10">
          <td />
          <td colSpan={4} className="px-3 py-3">
            <ItemDetail item={item} />
          </td>
        </tr>
      )}
    </>
  )
}

function ItemDetail({ item }: { item: Item }) {
  const { pr, acceptance: a, delivery, run } = item
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs">
      <DetailBlock title="Evidence">
        {item.evidence.length === 0 ? <span className="text-muted-foreground">none</span> : (
          <ul className="list-disc pl-4 space-y-0.5">
            {item.evidence.map((e, i) => <li key={i}>{e}</li>)}
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

function EventTimeline({ events }: { events: PipelineEvent[] }) {
  // Newest first, as a reader scans from the top.
  const rows = useMemo(() => [...events].reverse(), [events])
  return (
    <section className="mb-5">
      <h2 className="text-sm font-semibold text-foreground mb-2">Recent runner events</h2>
      <div className="bg-card border border-border rounded-xl overflow-hidden">
        {rows.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">No pipeline events in run.log yet.</p>
        ) : (
          <ol className="divide-y divide-border text-xs max-h-96 overflow-y-auto">
            {rows.map((e, i) => (
              <li key={`${e.time}-${i}`} className="px-3 py-1.5 flex gap-3">
                <time dateTime={e.time} title={e.time} className="text-muted-foreground tabular-nums whitespace-nowrap w-16 shrink-0">
                  {timeAgo(e.time)}
                </time>
                <span className={cn('font-mono shrink-0', e.level === 'warn' || e.level === 'error' ? 'text-amber-700' : 'text-foreground')}>
                  {e.event.replace(/^run\//, '')}
                </span>
                <span className="text-muted-foreground break-all">
                  {Object.entries(e.fields).map(([k, v]) => `${k}=${v}`).join(' ')}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </section>
  )
}
