import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { ChevronLeft, CheckCircle2, XCircle, SkipForward, Loader2, ExternalLink, Square } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, issueUrl, useRepoInfoMap } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { Markdown } from '../components/Markdown'
import { workDetailKey } from '../components/WorkDetail'
import { TraceView, type RawEvent } from '../components/TraceView'

interface ModelUsage {
  input_tokens: number | null
  output_tokens: number | null
  cache_read_input_tokens: number | null
  cache_creation_input_tokens: number | null
  cost_usd: number | null
}

interface Usage {
  cost_is_estimate?: boolean
  cost_basis?: string
  cost_unavailable?: boolean
  tokens_unavailable?: boolean
  duration_ms?: number | null
  num_turns?: number | null
  total_cost_usd: number | null
  input_tokens: number | null
  output_tokens: number | null
  cache_read_input_tokens: number | null
  cache_creation_input_tokens: number | null
  model_usage?: Record<string, ModelUsage>
  // Tokens summed from per-message usage because the run was killed before
  // claude's terminal result event. Cost is unknown on this path (issue #322).
  estimated?: boolean
  estimated_reason?: string
}

export interface RunMeta {
  attempt_id?: string
  offload_run_id?: string
  caller_run_id?: string
  session_id?: string
  profile?: string
  model?: string
  machine?: string
  execution_status?: string
  exit_code?: number | null
  error_kind?: string
  elapsed_ms?: number | null
  availability?: { cost?: boolean; tokens?: boolean }
  operator: string
  repo: string
  issue_number: number
  issue_title?: string
  started_at: string
  ended_at?: string
  status: 'success' | 'failed' | 'skipped' | 'running' | 'cancelled' | 'no-marker' | 'disallowed-outcome' | 'marker-recovered' | 'skipped-empty' | 'cost-limit'
  summary?: string
  pr_url?: string
  error?: string
  usage?: Usage
}

export const Route = createFileRoute('/_app/runs/$slug/$issue/$ts')({
  component: RunDetail,
})

function RunDetail() {
  const { slug, issue, ts } = Route.useParams()
  useDocumentTitle(`${slug}#${issue}`)
  // Absolute path from the server root. `./data/...` would break here
  // because the current URL is /runs/slug/issue/ts and the dashboard
  // would try to fetch /runs/slug/issue/data/... instead of /data/...
  const basePath = `/data/runs/${slug}/${issue}/${ts}`

  const [meta, setMeta] = useState<RunMeta | null>(null)
  const [events, setEvents] = useState<RawEvent[]>([])
  const [rawLoading, setRawLoading] = useState(true)
  const [tracePath, setTracePath] = useState<string | null>(null)
  const traceEnabled = tracePath === basePath
  const repoMap = useRepoInfoMap()

  useEffect(() => {
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const controller = new AbortController()
    setMeta(null)
    setEvents([])
    setRawLoading(true)

    const refetch = async () => {
      const [metaRes, evRes] = await Promise.all([
        fetch(`/api/runs?path=${encodeURIComponent(basePath.slice(6))}`, { cache: 'no-store', signal: controller.signal }).then(r => (r.ok ? r.json() : [])).then(rows => rows[0] ?? null).catch(() => null),
        traceEnabled ? fetch(`${basePath}/events.jsonl`, { cache: 'no-store', signal: controller.signal }).then(r => (r.ok ? r.text() : '')).catch(() => '') : Promise.resolve(''),
      ])
      if (cancelled) return

      const lines = evRes.split('\n').filter(Boolean)
      const parsed: RawEvent[] = []
      for (const line of lines) {
        try {
          const event = JSON.parse(line)
          if (event && typeof event === 'object' && typeof event.type === 'string') parsed.push(event)
        } catch {
          // skip malformed lines
        }
      }
      setMeta(metaRes)
      setEvents(parsed)
      setRawLoading(false)

      // While the run is in flight (no meta.json yet, or status === 'running'),
      // poll for updates. Stops automatically once the runner finalizes
      // meta.json with a terminal status.
      const stillRunning = !metaRes || metaRes.status === 'running'
      if (stillRunning && !cancelled) {
        timer = setTimeout(refetch, 1500)
      }
    }

    refetch()
    return () => {
      cancelled = true
      controller.abort()
      clearTimeout(timer)
    }
  }, [basePath, traceEnabled])

  const repo = slug.replace(/__/g, '/')
  const issueNum = issue.replace(/^issue-/, '')

  return (
    <div className="max-w-4xl mx-auto px-4 py-6 min-w-0 [overflow-wrap:anywhere]">
      <Link
        to="/repos/$"
        params={{ _splat: repo }}
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground mb-4"
      >
        <ChevronLeft className="w-3.5 h-3.5" /> <span className="font-mono">{repo}</span>
      </Link>

      {meta ? (
        <div className="mb-6">
          <div className="flex items-center gap-2 mb-2">
            <StatusBadge status={meta.status} />
            <span className="text-xs text-muted-foreground font-mono">{meta.operator}</span>
          </div>
          <h1 className="text-xl font-bold text-foreground">
            #{meta.issue_number} · {meta.issue_title || '(no title)'}
          </h1>
          <p className="text-xs text-muted-foreground mt-1 font-mono flex items-center gap-1 flex-wrap">
            <VcsIcon repo={meta.repo} map={repoMap} className="w-3.5 h-3.5 shrink-0" />
            <a
              href={repoUrl(meta.repo, repoMap)}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
            >
              {meta.repo} <ExternalLink className="w-3 h-3" />
            </a>
            <span>·</span>
            <a
              href={issueUrl(meta.repo, meta.issue_number, repoMap)}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
            >
              #{meta.issue_number} <ExternalLink className="w-3 h-3" />
            </a>
            <span>·</span>
            <span>{new Date(meta.started_at).toLocaleString()}</span>
            {meta.ended_at && (
              <>
                <span>·</span>
                <span>{elapsedEvidence(meta)}</span>
              </>
            )}
          </p>
          {meta.pr_url && (
            <a
              href={meta.pr_url}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-foreground hover:underline mt-2"
            >
              View PR <ExternalLink className="w-3 h-3" />
            </a>
          )}
        </div>
      ) : (
        <div className="mb-6">
          <h1 className="text-xl font-bold text-foreground">
            #{issueNum} · {repo}
          </h1>
          <p className="text-xs text-muted-foreground mt-1 font-mono flex items-center gap-1 flex-wrap">
            <VcsIcon repo={repo} map={repoMap} className="w-3.5 h-3.5 shrink-0" />
            <a
              href={repoUrl(repo, repoMap)}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
            >
              {repo} <ExternalLink className="w-3 h-3" />
            </a>
            <span>·</span>
            <a
              href={issueUrl(repo, Number(issueNum), repoMap)}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
            >
              #{issueNum} <ExternalLink className="w-3 h-3" />
            </a>
            <span>·</span>
            <span>meta.json not found — raw events below</span>
          </p>
        </div>
      )}

      <Link to="/pipeline" search={{ detail: workDetailKey(repo, Number(issueNum)) }} className="text-sm underline">Back to Work details</Link>
      <ExecutionEvidence meta={meta} />
      <ConclusionPanel meta={meta} />

      <UsagePanel meta={meta} />

      {!traceEnabled && <button className="border rounded p-2" onClick={() => setTracePath(basePath)}>Load execution events / legacy transcript</button>}
      {traceEnabled && <TraceView
        events={events}
        loading={rawLoading}
        running={!meta || meta.status === 'running'}
        openByDefault={true}
      />}
    </div>
  )
}

export function elapsedEvidence(meta: RunMeta): string {
  if (meta.elapsed_ms != null && Number.isFinite(meta.elapsed_ms) && meta.elapsed_ms >= 0) return `${msToShort(meta.elapsed_ms)} (stored Offload elapsed)`
  const start = Date.parse(meta.started_at), end = Date.parse(meta.ended_at || '')
  if (!meta.started_at.startsWith('0001-') && Number.isFinite(start) && Number.isFinite(end) && end >= start) return `${msToShort(end - start)} (fallback: run timestamps)`
  if (!meta.offload_run_id && !meta.execution_status && meta.usage?.duration_ms != null && Number.isFinite(meta.usage.duration_ms) && meta.usage.duration_ms >= 0) return `${msToShort(meta.usage.duration_ms)} (fallback: legacy usage)`
  return 'Not reported'
}

export function ExecutionEvidence({ meta }: { meta: RunMeta | null }) {
  if (!meta) return null
  const known = (v: unknown) => v == null || v === '' ? 'Not reported' : String(v)
  return <section aria-label="Execution evidence" className="my-4 border rounded p-3 min-w-0 [overflow-wrap:anywhere]">
    <h2 className="font-semibold">Execution evidence · {meta.offload_run_id || meta.execution_status ? 'persisted Offload metadata' : 'historical record; source not recorded'}</h2>
    <p>Attempt: {known(meta.attempt_id)} · Offload run: {known(meta.offload_run_id)} · caller: {known(meta.caller_run_id)}</p>
    <p>Session: {known(meta.session_id)} · machine: {known(meta.machine)}</p>
    <p>Profile: {known(meta.profile)} · model: {known(meta.model)}</p>
    <p>Execution: {known(meta.execution_status)} · exit: {known(meta.exit_code)} · error: {known(meta.error_kind)}</p>
    <p>Elapsed: {elapsedEvidence(meta)}</p>
    <p className="text-xs">Per-attempt evidence; resumed sessions are not aggregated. Execution status is separate from ClawFlow outcome and delivery.</p>
  </section>
}

/**
 * ConclusionPanel surfaces the run's outcome above the trace so the reader
 * doesn't have to scroll. Four shapes, picked in priority order:
 *   - error    → red panel with the runner's error message
 *   - summary  → the operator's stdout (== the comment posted on the issue)
 *   - skipped  → muted "skipped, no output" hint
 *   - running  → blue "in progress" hint
 * If meta hasn't loaded yet we skip the panel entirely; the trace below is
 * still useful while we wait.
 */
function ConclusionPanel({ meta }: { meta: RunMeta | null }) {
  if (!meta) return null

  if (meta.error) {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--error))' }}>
          Conclusion · error
        </h2>
        <div
          className="rounded-lg p-4 text-sm whitespace-pre-wrap font-mono"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            borderLeft: '3px solid hsl(var(--error))',
            color: 'hsl(var(--text-high))',
          }}
        >
          {meta.error}
        </div>
      </section>
    )
  }

  if (meta.summary) {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
          Conclusion
        </h2>
        {/* Use the project's bg-secondary surface (changes correctly
            in dark mode) instead of green-50/dark:green-950 — the
            green tints fight with markdown's own chip colors and
            produced unreadable contrast in some dark-mode shades. A
            success-tinted left border keeps the "this run succeeded"
            visual cue without wrapping the whole content in green. */}
        <div
          className="rounded-lg p-4"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            borderLeft: '3px solid hsl(var(--success))',
          }}
        >
          <Markdown>{meta.summary}</Markdown>
        </div>
      </section>
    )
  }

  if (meta.status === 'skipped') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
          Conclusion · skipped
        </h2>
        <div
          className="rounded-lg p-4 text-sm"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            color: 'hsl(var(--text-low))',
          }}
        >
          Operator returned no stdout, so no comment was posted on the issue. Expand the trace below to see what the model did.
        </div>
      </section>
    )
  }

  if (meta.status === 'no-marker') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--error))' }}>
          Conclusion · no outcome marker
        </h2>
        <div
          className="rounded-lg p-4 text-sm whitespace-pre-wrap"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            borderLeft: '3px solid hsl(var(--error))',
            color: 'hsl(var(--text-high))',
          }}
        >
          <p className="mb-2">The operator produced output but omitted the <code>{'<!-- clawflow:outcome=… -->'}</code> marker. No comment was posted and no label was applied to the issue.</p>
          <p className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>This run counts toward the circuit breaker. After {'{maxConsecutiveFailures}'} consecutive occurrences the issue will be labeled <code>agent-failed</code>.</p>
          {meta.summary && <pre className="mt-3 text-xs overflow-auto">{meta.summary}</pre>}
        </div>
      </section>
    )
  }

  if (meta.status === 'disallowed-outcome') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--error))' }}>
          Conclusion · disallowed outcome label
        </h2>
        <div
          className="rounded-lg p-4 text-sm whitespace-pre-wrap"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            borderLeft: '3px solid hsl(var(--error))',
            color: 'hsl(var(--text-high))',
          }}
        >
          <p className="mb-2">The operator emitted an outcome marker whose label is not in its declared <code>outcomes</code> list. The comment was posted, but no terminal label was applied to the issue.</p>
          {meta.error && <p className="mb-2 text-xs" style={{ color: 'hsl(var(--text-low))' }}>{meta.error}</p>}
          <p className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>This run counts toward the circuit breaker (issue #326).</p>
          {meta.summary && <pre className="mt-3 text-xs overflow-auto">{meta.summary}</pre>}
        </div>
      </section>
    )
  }

  if (meta.status === 'skipped-empty') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
          Conclusion · empty output
        </h2>
        <div
          className="rounded-lg p-4 text-sm"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            borderLeft: '3px solid hsl(var(--warning, var(--border)))',
            color: 'hsl(var(--text-low))',
          }}
        >
          The executor exited cleanly but produced no output. No comment was posted and no label was applied. This run counts toward the circuit breaker.
        </div>
      </section>
    )
  }

  if (meta.status === 'cancelled') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
          Conclusion · cancelled
        </h2>
        <div
          className="rounded-lg p-4 text-sm inline-flex items-center gap-2"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            borderLeft: '3px solid #d97706',
            color: 'hsl(var(--text-normal))',
          }}
        >
          <Square className="w-4 h-4 text-amber-600" /> Cancelled by user — trace below shows progress up to the point of cancellation.
        </div>
      </section>
    )
  }

  if (meta.status === 'running') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
          Conclusion
        </h2>
        <div
          className="rounded-lg p-4 text-sm inline-flex items-center gap-2"
          style={{
            background: 'hsl(var(--bg-secondary))',
            border: '1px solid hsl(var(--border))',
            borderLeft: '3px solid hsl(var(--_info))',
            color: 'hsl(var(--text-normal))',
          }}
        >
          <Loader2 className="w-4 h-4 animate-spin" /> In progress — trace is updating live below.
        </div>
      </section>
    )
  }

  return null
}

/**
 * UsagePanel renders a compact "what did this run cost" sidecar between the
 * Conclusion and Trace sections. Hidden entirely when meta.usage is absent
 * (run still in flight, or pre-feature data on disk that hasn't been
 * backfilled yet).
 */
export function UsagePanel({ meta }: { meta: RunMeta | null }) {
  if (!meta) return null
  if (!meta.usage) return <section className="mb-6 text-sm"><h2 className="font-semibold">Usage</h2><p>Cost: Not reported · Tokens: Not reported</p></section>
  const u = meta.usage

  const models = u.model_usage
    ? Object.entries(u.model_usage).sort((a, b) => (b[1].cost_usd ?? 0) - (a[1].cost_usd ?? 0))
    : []

  return (
    <section className="mb-6">
      <h2 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
        Usage
      </h2>
      <div
        className="rounded-lg p-4"
        style={{
          background: 'hsl(var(--bg-secondary))',
          border: '1px solid hsl(var(--border))',
        }}
      >
        <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm tabular-nums">
          <Stat
            label="cost"
            value={u.total_cost_usd == null || u.estimated || u.cost_unavailable || meta.availability?.cost === false ? 'Not reported' : `$${u.total_cost_usd.toFixed(4)}${u.cost_is_estimate ? ' (estimate)' : ''}`}
            highlight
          />
          <Stat label="duration" value={elapsedEvidence(meta)} />
          <Stat label="turns" value={meta.offload_run_id || meta.execution_status || u.num_turns == null ? 'Not reported' : String(u.num_turns)} />
          <Stat label="input" value={(u.tokens_unavailable || meta.availability?.tokens === false ? "Not reported" : u.input_tokens?.toLocaleString() ?? "Not reported")} />
          <Stat label="output" value={(u.tokens_unavailable || meta.availability?.tokens === false ? "Not reported" : u.output_tokens?.toLocaleString() ?? "Not reported")} />
          {(u.cache_read_input_tokens ?? 0) > 0 && (
            <Stat label="cache read" value={(u.tokens_unavailable || meta.availability?.tokens === false ? "Not reported" : u.cache_read_input_tokens?.toLocaleString() ?? "Not reported")} />
          )}
        </div>
        {u.cost_basis && <p className="text-xs mt-2">Cost basis: {u.cost_basis}</p>}
        {models.length > 0 && (
          <div
            className="mt-3 pt-3"
            style={{ borderTop: '1px solid hsl(var(--border))' }}
          >
            <table className="w-full text-xs tabular-nums">
              <thead style={{ color: 'hsl(var(--text-low))' }}>
                <tr>
                  <th className="text-left font-semibold pb-1">model</th>
                  <th className="text-right font-semibold pb-1">cost</th>
                  <th className="text-right font-semibold pb-1">input</th>
                  <th className="text-right font-semibold pb-1">output</th>
                </tr>
              </thead>
              <tbody>
                {models.map(([name, m]) => (
                  <tr key={name} style={{ color: 'hsl(var(--text-normal))' }}>
                    <td className="py-0.5 font-mono">{name}</td>
                    <td className="py-0.5 text-right" style={{ color: 'hsl(var(--text-high))' }}>
                      {m.cost_usd == null ? "Not reported" : `$${m.cost_usd.toFixed(4)}`}
                    </td>
                    <td className="py-0.5 text-right" style={{ color: 'hsl(var(--text-low))' }}>
                      {(m.input_tokens?.toLocaleString() ?? "Not reported")}
                    </td>
                    <td className="py-0.5 text-right" style={{ color: 'hsl(var(--text-low))' }}>
                      {(m.output_tokens?.toLocaleString() ?? "Not reported")}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </section>
  )
}

function Stat({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
        {label}
      </span>
      {/* highlight = the cost value, the row's headline number.
          Use --brand (orange) so it actually pops against the
          panel's bg-secondary surface. The non-highlighted variant
          uses --text-high — the regular tailwind `text-foreground`
          maps to --text-normal which is too soft on this bg. */}
      <span
        className="font-medium"
        style={{ color: highlight ? 'hsl(var(--brand))' : 'hsl(var(--text-high))' }}
      >
        {value}
      </span>
    </div>
  )
}

function msToShort(ms?: number | null): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return 'Not reported'
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h${m % 60}m`
}

function StatusBadge({ status }: { status: RunMeta['status'] }) {
  const cfg = {
    success:         { cls: 'bg-green-100 text-green-700 border-green-200',    Icon: CheckCircle2 },
    running:         { cls: 'bg-blue-100 text-blue-700 border-blue-200',       Icon: Loader2 },
    failed:          { cls: 'bg-red-100 text-red-700 border-red-200',          Icon: XCircle },
    skipped:         { cls: 'bg-muted text-muted-foreground border-border',    Icon: SkipForward },
    cancelled:       { cls: 'bg-amber-50 text-amber-700 border-amber-200',     Icon: Square },
    'no-marker':     { cls: 'bg-orange-100 text-orange-700 border-orange-200', Icon: XCircle },
    // Marker present, label outside the operator's whitelist (issue #326).
    'disallowed-outcome': { cls: 'bg-orange-100 text-orange-700 border-orange-200', Icon: XCircle },
    // Salvaged run (issue #307): completed, but with an inferred outcome label.
    'marker-recovered': { cls: 'bg-amber-100 text-amber-800 border-amber-200',  Icon: CheckCircle2 },
    'skipped-empty': { cls: 'bg-orange-50 text-orange-600 border-orange-200',  Icon: SkipForward },
    // Billing cap (issue #308): refused before any work happened, and stays
    // that way until the provider's window resets — amber "waiting", not red.
    'cost-limit':    { cls: 'bg-amber-100 text-amber-800 border-amber-200',    Icon: Square },
  }[status] ?? { cls: 'bg-muted text-muted-foreground border-border', Icon: SkipForward }
  const Icon = cfg.Icon
  return (
    <span className={cn('inline-flex items-center gap-1 border px-1.5 py-0.5 rounded text-[11px] font-semibold', cfg.cls)}>
      <Icon className={cn('w-3 h-3', status === 'running' && 'animate-spin')} />
      {status}
    </span>
  )
}
