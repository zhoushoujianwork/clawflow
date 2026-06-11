import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { ChevronLeft, CheckCircle2, XCircle, SkipForward, Loader2, ExternalLink, Square } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, issueUrl, useRepoInfoMap } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { Markdown } from '../components/Markdown'
import { TraceView, type RawEvent } from '../components/TraceView'

interface ModelUsage {
  input_tokens: number
  output_tokens: number
  cache_read_input_tokens: number
  cache_creation_input_tokens: number
  cost_usd: number
}

interface Usage {
  duration_ms: number
  num_turns: number
  total_cost_usd: number
  input_tokens: number
  output_tokens: number
  cache_read_input_tokens: number
  cache_creation_input_tokens: number
  model_usage?: Record<string, ModelUsage>
}

interface RunMeta {
  operator: string
  repo: string
  issue_number: number
  issue_title?: string
  started_at: string
  ended_at?: string
  status: 'success' | 'failed' | 'skipped' | 'running' | 'cancelled' | 'no-marker' | 'skipped-empty'
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
  const repoMap = useRepoInfoMap()

  useEffect(() => {
    let cancelled = false

    const refetch = async () => {
      const [metaRes, evRes] = await Promise.all([
        fetch(`${basePath}/meta.json`, { cache: 'no-store' }).then(r => (r.ok ? r.json() : null)).catch(() => null),
        fetch(`${basePath}/events.jsonl`, { cache: 'no-store' }).then(r => (r.ok ? r.text() : '')).catch(() => ''),
      ])
      if (cancelled) return

      const lines = evRes.split('\n').filter(Boolean)
      const parsed: RawEvent[] = []
      for (const line of lines) {
        try {
          parsed.push(JSON.parse(line))
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
        setTimeout(refetch, 1500)
      }
    }

    refetch()
    return () => {
      cancelled = true
    }
  }, [basePath])

  const repo = slug.replace(/__/g, '/')
  const issueNum = issue.replace(/^issue-/, '')

  return (
    <div className="max-w-4xl mx-auto px-4 py-6 min-w-0">
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
                <span>{durationStr(meta.started_at, meta.ended_at)}</span>
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

      <ConclusionPanel meta={meta} />

      <UsagePanel meta={meta} />

      <TraceView
        events={events}
        loading={rawLoading}
        running={!meta || meta.status === 'running'}
        openByDefault={!meta || meta.status === 'running'}
      />
    </div>
  )
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
          Claude exited cleanly but produced no output. No comment was posted and no label was applied. This run counts toward the circuit breaker.
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
function UsagePanel({ meta }: { meta: RunMeta | null }) {
  if (!meta || !meta.usage) return null
  const u = meta.usage

  const models = u.model_usage
    ? Object.entries(u.model_usage).sort((a, b) => b[1].cost_usd - a[1].cost_usd)
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
          <Stat label="cost" value={`$${u.total_cost_usd.toFixed(4)}`} highlight />
          <Stat label="duration" value={msToShort(u.duration_ms)} />
          <Stat label="turns" value={String(u.num_turns)} />
          <Stat label="input" value={u.input_tokens.toLocaleString()} />
          <Stat label="output" value={u.output_tokens.toLocaleString()} />
          {u.cache_read_input_tokens > 0 && (
            <Stat label="cache read" value={u.cache_read_input_tokens.toLocaleString()} />
          )}
        </div>
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
                      ${m.cost_usd.toFixed(4)}
                    </td>
                    <td className="py-0.5 text-right" style={{ color: 'hsl(var(--text-low))' }}>
                      {m.input_tokens.toLocaleString()}
                    </td>
                    <td className="py-0.5 text-right" style={{ color: 'hsl(var(--text-low))' }}>
                      {m.output_tokens.toLocaleString()}
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

function msToShort(ms: number): string {
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
    'skipped-empty': { cls: 'bg-orange-50 text-orange-600 border-orange-200',  Icon: SkipForward },
  }[status] ?? { cls: 'bg-muted text-muted-foreground border-border', Icon: SkipForward }
  const Icon = cfg.Icon
  return (
    <span className={cn('inline-flex items-center gap-1 border px-1.5 py-0.5 rounded text-[11px] font-semibold', cfg.cls)}>
      <Icon className={cn('w-3 h-3', status === 'running' && 'animate-spin')} />
      {status}
    </span>
  )
}

function durationStr(start: string, end: string) {
  // See dashboard's durationStr — same defense against zero-time
  // ended_at sneaking in from older snapshots.
  const tStart = new Date(start).getTime()
  const tEnd = new Date(end).getTime()
  if (!isFinite(tStart) || !isFinite(tEnd) || tEnd < 946684800000) return ''
  const ms = tEnd - tStart
  if (ms < 0) return ''
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m${s % 60}s`
}

