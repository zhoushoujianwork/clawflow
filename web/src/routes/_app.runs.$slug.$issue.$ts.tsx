import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { ChevronLeft, ChevronRight, CheckCircle2, XCircle, SkipForward, Loader2, ExternalLink, Wrench, MessageSquare, Brain } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, issueUrl, useRepoInfoMap } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'

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
  status: 'success' | 'failed' | 'skipped' | 'running'
  summary?: string
  pr_url?: string
  error?: string
  usage?: Usage
}

/**
 * Content block inside an assistant or user message. The shapes we care about:
 *   - { type: "thinking", thinking: "…" }   — model's chain-of-thought
 *   - { type: "text", text: "…" }           — model's reply text
 *   - { type: "tool_use", id, name, input } — tool call from the model
 *   - { type: "tool_result", tool_use_id, content, is_error } — runner's reply
 * Anything else is ignored at render time.
 */
interface ContentBlock {
  type: string
  text?: string
  thinking?: string
  name?: string
  id?: string
  input?: unknown
  tool_use_id?: string
  content?: unknown
  is_error?: boolean
}

/**
 * Raw claude stream-json event. We only render a handful of shapes — the
 * incremental stream_event deltas are dropped entirely because the
 * aggregated `assistant` and `result` events carry the full content.
 */
interface RawEvent {
  type: string
  subtype?: string
  result?: string
  session_id?: string
  message?: {
    role?: string
    content?: ContentBlock[]
  }
  uuid?: string
  is_error?: boolean
  duration_ms?: number
  total_cost_usd?: number
}

export const Route = createFileRoute('/_app/runs/$slug/$issue/$ts')({
  component: RunDetail,
})

function RunDetail() {
  const { slug, issue, ts } = Route.useParams()
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
    <div className="max-w-4xl mx-auto px-4 py-6">
      <Link
        to="/dashboard"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground mb-4"
      >
        <ChevronLeft className="w-3.5 h-3.5" /> Dashboard
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

      {(() => {
        const visible = visibleEvents(events)
        const toolNames = collectToolNames(events)
        // While the operator is still running there's no conclusion yet, so
        // open the trace by default — the user is actively waiting on it.
        // Terminal runs get a collapsed trace so the conclusion stays the
        // first thing on screen.
        const openByDefault = !meta || meta.status === 'running'
        return (
          <details open={openByDefault} className="group">
            <summary className="cursor-pointer select-none flex items-center gap-2 text-sm font-semibold text-foreground hover:text-foreground/80">
              <ChevronRight className="w-4 h-4 transition-transform group-open:rotate-90" />
              Trace
              <span className="font-normal text-muted-foreground">({visible.length} steps)</span>
            </summary>
            <div className="mt-3">
              {rawLoading ? (
                <p className="text-sm text-muted-foreground">Loading…</p>
              ) : visible.length === 0 ? (
                <p className="text-sm text-muted-foreground">No trace yet.</p>
              ) : (
                <div className="space-y-2">
                  {visible.map((ev, i) => (
                    <EventCard key={i} ev={ev} toolNames={toolNames} />
                  ))}
                </div>
              )}
            </div>
          </details>
        )
      })()}
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
        <h2 className="text-sm font-semibold text-red-600 mb-2">Conclusion · error</h2>
        <div className="bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900 rounded-lg p-4 text-sm whitespace-pre-wrap font-mono text-red-700 dark:text-red-400">
          {meta.error}
        </div>
      </section>
    )
  }

  if (meta.summary) {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold text-foreground mb-2">Conclusion</h2>
        <div className="bg-green-50/70 dark:bg-green-950/20 border border-green-200 dark:border-green-900/60 rounded-lg p-4 text-sm whitespace-pre-wrap font-mono text-foreground">
          {meta.summary}
        </div>
      </section>
    )
  }

  if (meta.status === 'skipped') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold text-foreground mb-2">Conclusion · skipped</h2>
        <div className="bg-muted/40 border border-border rounded-lg p-4 text-sm text-muted-foreground">
          Operator returned no stdout, so no comment was posted on the issue. Expand the trace below to see what the model did.
        </div>
      </section>
    )
  }

  if (meta.status === 'running') {
    return (
      <section className="mb-6">
        <h2 className="text-sm font-semibold text-foreground mb-2">Conclusion</h2>
        <div className="bg-blue-50/60 dark:bg-blue-950/20 border border-blue-200 dark:border-blue-900/60 rounded-lg p-4 text-sm text-muted-foreground inline-flex items-center gap-2">
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
      <h2 className="text-sm font-semibold text-foreground mb-2">Usage</h2>
      <div className="bg-card border border-border rounded-lg p-4">
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
          <div className="mt-3 border-t border-border pt-3">
            <table className="w-full text-xs tabular-nums">
              <thead className="text-muted-foreground">
                <tr>
                  <th className="text-left font-semibold pb-1">model</th>
                  <th className="text-right font-semibold pb-1">cost</th>
                  <th className="text-right font-semibold pb-1">input</th>
                  <th className="text-right font-semibold pb-1">output</th>
                </tr>
              </thead>
              <tbody>
                {models.map(([name, m]) => (
                  <tr key={name} className="text-foreground">
                    <td className="py-0.5 font-mono">{name}</td>
                    <td className="py-0.5 text-right">${m.cost_usd.toFixed(4)}</td>
                    <td className="py-0.5 text-right text-muted-foreground">
                      {m.input_tokens.toLocaleString()}
                    </td>
                    <td className="py-0.5 text-right text-muted-foreground">
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
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className={cn('font-medium', highlight ? 'text-primary' : 'text-foreground')}>
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
    success: { cls: 'bg-green-100 text-green-700 border-green-200', Icon: CheckCircle2 },
    running: { cls: 'bg-blue-100 text-blue-700 border-blue-200', Icon: Loader2 },
    failed:  { cls: 'bg-red-100 text-red-700 border-red-200', Icon: XCircle },
    skipped: { cls: 'bg-muted text-muted-foreground border-border', Icon: SkipForward },
  }[status]
  const Icon = cfg.Icon
  return (
    <span className={cn('inline-flex items-center gap-1 border px-1.5 py-0.5 rounded text-[11px] font-semibold', cfg.cls)}>
      <Icon className={cn('w-3 h-3', status === 'running' && 'animate-spin')} />
      {status}
    </span>
  )
}

function durationStr(start: string, end: string) {
  const ms = new Date(end).getTime() - new Date(start).getTime()
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m${s % 60}s`
}

/**
 * visibleEvents narrows the raw stream-json log to the events that carry
 * semantic content — thinking, replies, tool calls/results, and the final
 * result. Everything else (token-level deltas, system init, status pings,
 * rate-limit notices) is metadata for the runtime and adds no signal for
 * a human reading the trace.
 */
function visibleEvents(events: RawEvent[]): RawEvent[] {
  return events.filter(ev => {
    if (ev.type === 'assistant') {
      return (ev.message?.content || []).some(c =>
        c.type === 'thinking' || c.type === 'text' || c.type === 'tool_use'
      )
    }
    if (ev.type === 'user') {
      return (ev.message?.content || []).some(c => c.type === 'tool_result')
    }
    if (ev.type === 'result') return true
    return false
  })
}

/**
 * Build a tool_use_id → tool name map by scanning every assistant tool_use.
 * tool_result events only carry the id back, so we look up the name here to
 * label the result block with something meaningful instead of a UUID.
 */
function collectToolNames(events: RawEvent[]): Record<string, string> {
  const m: Record<string, string> = {}
  for (const ev of events) {
    if (ev.type !== 'assistant' || !ev.message?.content) continue
    for (const c of ev.message.content) {
      if (c.type === 'tool_use' && c.id && c.name) {
        m[c.id] = c.name
      }
    }
  }
  return m
}

/**
 * Pretty-print a value that may already be a string. Falls back to JSON
 * with 2-space indent so deeply nested tool inputs/outputs stay readable.
 */
function prettyValue(v: unknown): string {
  if (typeof v === 'string') return v
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

/**
 * Wrap long content in <details> so the overview stays compact. Short
 * content renders directly without the expand toggle.
 */
function CollapsibleBlock({ text, threshold = 280 }: { text: string; threshold?: number }) {
  if (text.length <= threshold) {
    return <pre className="text-[11px] font-mono whitespace-pre-wrap text-muted-foreground">{text}</pre>
  }
  return (
    <details className="text-[11px] font-mono">
      <summary className="cursor-pointer text-muted-foreground hover:text-foreground select-none">
        {text.slice(0, threshold).replace(/\s+/g, ' ').trim()}… <span className="text-primary/70">show more</span>
      </summary>
      <pre className="whitespace-pre-wrap text-muted-foreground mt-1">{text}</pre>
    </details>
  )
}

/**
 * EventCard renders one logical step of the trace. Three flavors:
 *   - assistant: thinking / reply / tool_use blocks
 *   - user:     tool_result blocks (collapsible)
 *   - result:   the final summary line + body
 * Anything visibleEvents doesn't filter is one of these by construction,
 * so there is no JSON-dump fallback — that was the noise the user
 * complained about.
 */
function EventCard({ ev, toolNames }: { ev: RawEvent; toolNames: Record<string, string> }) {
  if (ev.type === 'assistant' && ev.message?.content) {
    return (
      <div className="space-y-2">
        {ev.message.content.map((c, i) => {
          if (c.type === 'thinking' && c.thinking) {
            return (
              <div key={i} className="bg-purple-50/50 dark:bg-purple-950/10 border border-purple-200/60 dark:border-purple-900/40 rounded-lg p-3">
                <div className="flex items-center gap-1.5 text-xs text-purple-700 dark:text-purple-400 mb-1">
                  <Brain className="w-3 h-3" />
                  <span>thinking</span>
                </div>
                <div className="text-sm whitespace-pre-wrap text-foreground/80 italic leading-relaxed">{c.thinking.trim()}</div>
              </div>
            )
          }
          if (c.type === 'text' && c.text) {
            return (
              <div key={i} className="bg-card border border-border rounded-lg p-3">
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground mb-1">
                  <MessageSquare className="w-3 h-3" />
                  <span>reply</span>
                </div>
                <div className="text-sm whitespace-pre-wrap text-foreground">{c.text}</div>
              </div>
            )
          }
          if (c.type === 'tool_use') {
            const inputStr = prettyValue(c.input ?? {})
            return (
              <div key={i} className="bg-secondary/40 border border-border rounded-lg p-3">
                <div className="flex items-center gap-1.5 text-xs font-mono mb-1">
                  <Wrench className="w-3 h-3 text-blue-600" />
                  <span className="text-blue-700 dark:text-blue-400">{c.name || 'tool'}</span>
                  <span className="text-muted-foreground">→</span>
                </div>
                <CollapsibleBlock text={inputStr} threshold={200} />
              </div>
            )
          }
          return null
        })}
      </div>
    )
  }

  if (ev.type === 'user' && ev.message?.content) {
    const results = ev.message.content.filter(c => c.type === 'tool_result')
    if (results.length === 0) return null
    return (
      <div className="space-y-2">
        {results.map((r, i) => {
          const name = (r.tool_use_id && toolNames[r.tool_use_id]) || 'tool'
          // tool_result.content is sometimes a string, sometimes an array of
          // text blocks. Normalize to a single string.
          let body: string
          if (typeof r.content === 'string') {
            body = r.content
          } else if (Array.isArray(r.content)) {
            body = r.content
              .map(p => (p && typeof p === 'object' && 'text' in p ? String((p as { text: unknown }).text) : prettyValue(p)))
              .join('\n')
          } else {
            body = prettyValue(r.content)
          }
          return (
            <div
              key={i}
              className={cn(
                'border rounded-lg p-3',
                r.is_error
                  ? 'bg-red-50/60 dark:bg-red-950/20 border-red-200 dark:border-red-900/50'
                  : 'bg-amber-50/40 dark:bg-amber-950/10 border-amber-200 dark:border-amber-900/50',
              )}
            >
              <div className={cn(
                'flex items-center gap-1.5 text-xs font-mono mb-1',
                r.is_error ? 'text-red-700 dark:text-red-400' : 'text-amber-700 dark:text-amber-400',
              )}>
                <Wrench className="w-3 h-3" />
                <span>← {name}{r.is_error ? ' (error)' : ''}</span>
              </div>
              <CollapsibleBlock text={body} />
            </div>
          )
        })}
      </div>
    )
  }

  if (ev.type === 'result') {
    return (
      <div className={cn(
        'border rounded-lg p-3 text-sm whitespace-pre-wrap',
        ev.is_error
          ? 'bg-red-50 dark:bg-red-950/30 border-red-200 dark:border-red-900 text-red-700 dark:text-red-400'
          : 'bg-green-50 dark:bg-green-950/20 border-green-200 dark:border-green-900 text-foreground',
      )}>
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground mb-2 font-sans">
          <CheckCircle2 className="w-3 h-3" />
          <span>result {ev.subtype || ''}</span>
          {ev.duration_ms != null && <span>· {Math.round(ev.duration_ms / 1000)}s</span>}
          {ev.total_cost_usd != null && <span>· ${ev.total_cost_usd.toFixed(4)}</span>}
        </div>
        {ev.result || <span className="text-muted-foreground italic">(empty — operator returned no stdout, so no comment was posted)</span>}
      </div>
    )
  }

  return null
}
