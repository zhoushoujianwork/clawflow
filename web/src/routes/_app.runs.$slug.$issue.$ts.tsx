import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { ChevronLeft, ChevronRight, CheckCircle2, XCircle, SkipForward, Loader2, ExternalLink, ChevronsUp, ChevronsDown, Square } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, issueUrl, useRepoInfoMap } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { Markdown } from '../components/Markdown'

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
  status: 'success' | 'failed' | 'skipped' | 'running' | 'cancelled'
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
  const terminalRef = useRef<HTMLDivElement>(null)
  const isAtBottom = useRef(true)

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

  useEffect(() => {
    if (isAtBottom.current && terminalRef.current) {
      terminalRef.current.scrollTop = terminalRef.current.scrollHeight
    }
  }, [events])

  const handleTerminalScroll = () => {
    const el = terminalRef.current
    if (!el) return
    isAtBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
  }

  const repo = slug.replace(/__/g, '/')
  const issueNum = issue.replace(/^issue-/, '')

  return (
    <div className="max-w-4xl mx-auto px-4 py-6">
      <Link
        to="/repos/$repoName"
        params={{ repoName: encodeURIComponent(repo) }}
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

      {(() => {
        const visible = visibleEvents(events)
        const toolNames = collectToolNames(events)
        const openByDefault = !meta || meta.status === 'running'
        return (
          <details open={openByDefault} className="group">
            <summary className="cursor-pointer select-none flex items-center gap-2 text-sm font-semibold text-foreground hover:text-foreground/80">
              <ChevronRight className="w-4 h-4 transition-transform group-open:rotate-90" />
              Trace
              <span className="font-normal text-muted-foreground">({visible.length} steps)</span>
            </summary>
            <div className="mt-3">
              <div className="flex items-center justify-between px-3 py-1.5 bg-[#2d2d2d] rounded-t-lg border border-[#3d3d3d] border-b-0">
                <span className="text-xs text-gray-400 font-mono">trace</span>
                <div className="flex items-center gap-2">
                  <span className="text-xs text-gray-500">{visible.length} steps</span>
                  <button
                    onClick={() => { if (terminalRef.current) terminalRef.current.scrollTop = 0 }}
                    className="text-gray-500 hover:text-gray-300 cursor-pointer"
                    title="Scroll to top"
                  >
                    <ChevronsUp className="w-3.5 h-3.5" />
                  </button>
                  <button
                    onClick={() => { if (terminalRef.current) terminalRef.current.scrollTop = terminalRef.current.scrollHeight }}
                    className="text-gray-500 hover:text-gray-300 cursor-pointer"
                    title="Scroll to bottom"
                  >
                    <ChevronsDown className="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>
              <div
                ref={terminalRef}
                onScroll={handleTerminalScroll}
                className="h-[500px] overflow-y-auto bg-[#1e1e1e] rounded-b-lg border border-[#3d3d3d] p-3 font-mono text-[13px] leading-relaxed"
              >
                {rawLoading ? (
                  <p className="text-gray-500">Loading…</p>
                ) : visible.length === 0 ? (
                  <p className="text-gray-500">No trace yet.</p>
                ) : (
                  visible.map((ev, i) => (
                    <TerminalLine key={i} ev={ev} toolNames={toolNames} />
                  ))
                )}
              </div>
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
    success:   { cls: 'bg-green-100 text-green-700 border-green-200',  Icon: CheckCircle2 },
    running:   { cls: 'bg-blue-100 text-blue-700 border-blue-200',     Icon: Loader2 },
    failed:    { cls: 'bg-red-100 text-red-700 border-red-200',        Icon: XCircle },
    skipped:   { cls: 'bg-muted text-muted-foreground border-border',  Icon: SkipForward },
    cancelled: { cls: 'bg-amber-50 text-amber-700 border-amber-200',   Icon: Square },
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

function TerminalCollapsible({ text, maxLines = 8 }: { text: string; maxLines?: number }) {
  const [expanded, setExpanded] = useState(false)
  const lines = text.split('\n')
  if (lines.length <= maxLines) {
    return <span className="text-gray-300 whitespace-pre-wrap">{text}</span>
  }
  if (!expanded) {
    return (
      <span className="text-gray-300 whitespace-pre-wrap">
        {lines.slice(0, maxLines).join('\n')}
        {'\n'}
        <button
          onClick={() => setExpanded(true)}
          className="text-cyan-400 hover:text-cyan-300 cursor-pointer"
        >
          ··· {lines.length - maxLines} more lines (click to expand)
        </button>
      </span>
    )
  }
  return (
    <span className="text-gray-300 whitespace-pre-wrap">
      {text}
      {'\n'}
      <button
        onClick={() => setExpanded(false)}
        className="text-cyan-400 hover:text-cyan-300 cursor-pointer"
      >
        ··· (collapse)
      </button>
    </span>
  )
}

function TerminalLine({ ev, toolNames }: { ev: RawEvent; toolNames: Record<string, string> }) {
  if (ev.type === 'assistant' && ev.message?.content) {
    return (
      <>
        {ev.message.content.map((c, i) => {
          if (c.type === 'thinking' && c.thinking) {
            return (
              <div key={i} className="mb-1">
                <span className="text-purple-400 opacity-70">[thinking] </span>
                <span className="text-gray-400 italic">{c.thinking.trim()}</span>
              </div>
            )
          }
          if (c.type === 'text' && c.text) {
            return (
              <div key={i} className="mb-1">
                <span className="text-green-400">[reply] </span>
                <span className="text-gray-200 whitespace-pre-wrap">{c.text}</span>
              </div>
            )
          }
          if (c.type === 'tool_use') {
            const inputStr = prettyValue(c.input ?? {})
            return (
              <div key={i} className="mb-1">
                <span className="text-cyan-400">$ </span>
                <span className="text-cyan-300 font-semibold">{c.name || 'tool'}</span>
                {inputStr.length > 0 && inputStr !== '{}' && (
                  <>
                    {'\n'}
                    <TerminalCollapsible text={inputStr} maxLines={6} />
                  </>
                )}
              </div>
            )
          }
          return null
        })}
      </>
    )
  }

  if (ev.type === 'user' && ev.message?.content) {
    const results = ev.message.content.filter(c => c.type === 'tool_result')
    if (results.length === 0) return null
    return (
      <>
        {results.map((r, i) => {
          const name = (r.tool_use_id && toolNames[r.tool_use_id]) || 'tool'
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
            <div key={i} className="mb-1">
              <span className={r.is_error ? 'text-red-400' : 'text-yellow-400'}>
                ← {name}{r.is_error ? ' (error)' : ''}
              </span>
              {body && (
                <>
                  {'\n'}
                  <TerminalCollapsible text={body} maxLines={8} />
                </>
              )}
            </div>
          )
        })}
      </>
    )
  }

  if (ev.type === 'result') {
    return (
      <div className="mb-1 mt-2 pt-2 border-t border-gray-700">
        <span className={ev.is_error ? 'text-red-400 font-bold' : 'text-green-400 font-bold'}>
          [result] {ev.subtype || ''}
        </span>
        {ev.duration_ms != null && <span className="text-gray-500"> · {Math.round(ev.duration_ms / 1000)}s</span>}
        {ev.total_cost_usd != null && <span className="text-gray-500"> · ${ev.total_cost_usd.toFixed(4)}</span>}
        {ev.result && (
          <>
            {'\n'}
            <span className="text-gray-200 whitespace-pre-wrap">{ev.result}</span>
          </>
        )}
        {!ev.result && (
          <>
            {'\n'}
            <span className="text-gray-500 italic">(empty — no stdout)</span>
          </>
        )}
      </div>
    )
  }

  return null
}
