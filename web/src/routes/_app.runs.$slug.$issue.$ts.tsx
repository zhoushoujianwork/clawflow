import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { ChevronLeft, CheckCircle2, XCircle, SkipForward, Loader2, ExternalLink, Terminal, Wrench, MessageSquare } from 'lucide-react'
import { cn } from '../lib/utils'

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
}

/**
 * Raw claude stream-json event. Shape is whatever the model's -p output
 * gave us; we only pattern-match a few types for nice rendering and fall
 * back to a truncated JSON preview for the rest.
 */
interface RawEvent {
  type: string
  subtype?: string
  result?: string
  session_id?: string
  event?: {
    type: string
    content_block?: {
      type: string
      name?: string
      id?: string
      input?: unknown
      text?: string
    }
    delta?: {
      type: string
      text?: string
      partial_json?: string
    }
    index?: number
    message?: unknown
  }
  message?: {
    role?: string
    content?: Array<{ type: string; text?: string; name?: string; input?: unknown }>
  }
  uuid?: string
  tools?: string[]
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

  useEffect(() => {
    fetch(`${basePath}/meta.json`, { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : null))
      .then(setMeta)
      .catch(() => setMeta(null))

    fetch(`${basePath}/events.jsonl`, { cache: 'no-store' })
      .then(r => (r.ok ? r.text() : ''))
      .then(text => {
        const lines = text.split('\n').filter(Boolean)
        const parsed: RawEvent[] = []
        for (const line of lines) {
          try {
            parsed.push(JSON.parse(line))
          } catch {
            // skip malformed lines
          }
        }
        setEvents(parsed)
      })
      .finally(() => setRawLoading(false))
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
          <p className="text-xs text-muted-foreground mt-1 font-mono">
            {meta.repo} · {new Date(meta.started_at).toLocaleString()}
            {meta.ended_at && ` · ${durationStr(meta.started_at, meta.ended_at)}`}
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
          <p className="text-xs text-muted-foreground mt-1">meta.json not found — raw events below</p>
        </div>
      )}

      {meta?.summary && (
        <section className="mb-6">
          <h2 className="text-sm font-semibold text-foreground mb-2">Summary</h2>
          <div className="bg-card border border-border rounded-lg p-4 text-sm whitespace-pre-wrap font-mono">
            {meta.summary}
          </div>
        </section>
      )}

      {meta?.error && (
        <section className="mb-6">
          <h2 className="text-sm font-semibold text-red-600 mb-2">Error</h2>
          <div className="bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900 rounded-lg p-4 text-sm whitespace-pre-wrap font-mono text-red-700 dark:text-red-400">
            {meta.error}
          </div>
        </section>
      )}

      <section>
        <h2 className="text-sm font-semibold text-foreground mb-2">
          Event timeline <span className="text-muted-foreground font-normal">
            ({visibleEvents(events).length} of {events.length} events)
          </span>
        </h2>
        {rawLoading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : events.length === 0 ? (
          <p className="text-sm text-muted-foreground">No events found.</p>
        ) : (
          <div className="space-y-2">
            {visibleEvents(events).map((ev, i) => (
              <EventCard key={i} ev={ev} />
            ))}
          </div>
        )}
      </section>
    </div>
  )
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
 * visibleEvents filters out the noisy incremental stream_event deltas
 * (every text keystroke and input_json fragment) because the aggregated
 * `assistant` and `result` events already carry the full content. Keeping
 * them would be 200+ redundant rows. Also drops transient status pings.
 */
function visibleEvents(events: RawEvent[]): RawEvent[] {
  return events.filter(ev => {
    if (ev.type === 'stream_event') return false
    if (ev.type === 'system' && ev.subtype === 'status') return false
    if (ev.type === 'rate_limit_event') return false
    return true
  })
}

/**
 * EventCard: the richer per-event renderer. Handles the most common
 * stream-json shapes we see in practice:
 *   - system/init: the session bootstrap with tool list
 *   - assistant message with text content: rendered as a chat-style block
 *   - user message with tool_result: tool response back to the model
 *   - result: the final answer + usage summary
 * Everything else falls back to a truncated JSON snippet so nothing is
 * ever hidden.
 */
function EventCard({ ev }: { ev: RawEvent }) {
  // 1) System init — show once at the top, collapsed.
  if (ev.type === 'system' && ev.subtype === 'init') {
    return (
      <div className="bg-secondary/40 border border-border rounded-lg px-3 py-2 text-xs">
        <div className="flex items-center gap-1.5 text-muted-foreground">
          <Terminal className="w-3 h-3" />
          <span>session init</span>
          {ev.tools && <span className="font-mono">· {ev.tools.length} tools</span>}
          {ev.session_id && <span className="font-mono">· {ev.session_id.slice(0, 8)}</span>}
        </div>
      </div>
    )
  }

  // 2) Assistant message with text content.
  if (ev.type === 'assistant' && ev.message?.content) {
    const texts = ev.message.content.filter(c => c.type === 'text' && c.text).map(c => c.text as string)
    const toolUses = ev.message.content.filter(c => c.type === 'tool_use')
    return (
      <div className="bg-card border border-border rounded-lg p-3">
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground mb-2">
          <MessageSquare className="w-3 h-3" />
          <span>assistant</span>
        </div>
        {texts.length > 0 && (
          <div className="text-sm whitespace-pre-wrap mb-2 font-mono text-foreground">{texts.join('\n\n')}</div>
        )}
        {toolUses.map((t, i) => (
          <div key={i} className="text-xs text-muted-foreground font-mono flex items-center gap-1 mt-1">
            <Wrench className="w-3 h-3" /> {t.name}
            {t.input != null && (
              <span className="truncate">{JSON.stringify(t.input).slice(0, 100)}</span>
            )}
          </div>
        ))}
      </div>
    )
  }

  // 3) User message — tool_result coming back to the model.
  if (ev.type === 'user' && ev.message?.content) {
    const results = ev.message.content.filter(c => c.type === 'tool_result')
    return (
      <div className="bg-amber-50/40 dark:bg-amber-950/10 border border-amber-200 dark:border-amber-900/50 rounded-lg p-3 text-xs">
        <div className="flex items-center gap-1.5 text-amber-700 dark:text-amber-400 font-mono mb-1">
          <Wrench className="w-3 h-3" />
          <span>tool result ({results.length})</span>
        </div>
        {results.map((r, i) => {
          // tool_result.content is often a string or an array of text blocks;
          // render conservatively so we don't crash on shape surprises.
          const raw = typeof r.text === 'string' ? r.text : JSON.stringify((r as { content?: unknown }).content ?? r, null, 0)
          const shown = raw.length > 400 ? raw.slice(0, 400) + '…' : raw
          return (
            <pre key={i} className="text-[11px] font-mono whitespace-pre-wrap text-muted-foreground mt-1">
              {shown}
            </pre>
          )
        })}
      </div>
    )
  }

  // 4) Final result.
  if (ev.type === 'result') {
    return (
      <div className={cn(
        'border rounded-lg p-3 text-sm font-mono whitespace-pre-wrap',
        ev.is_error
          ? 'bg-red-50 dark:bg-red-950/30 border-red-200 dark:border-red-900 text-red-700 dark:text-red-400'
          : 'bg-green-50 dark:bg-green-950/20 border-green-200 dark:border-green-900 text-foreground'
      )}>
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground mb-2 font-sans">
          <CheckCircle2 className="w-3 h-3" />
          <span>result {ev.subtype || ''}</span>
          {ev.duration_ms != null && <span>· {Math.round(ev.duration_ms)}ms</span>}
          {ev.total_cost_usd != null && <span>· ${ev.total_cost_usd.toFixed(4)}</span>}
        </div>
        {ev.result}
      </div>
    )
  }

  // 5) Fallback — truncated JSON preview.
  return (
    <div className="bg-card border border-border rounded-lg px-3 py-1.5 text-[11px] font-mono text-muted-foreground flex items-center gap-2">
      <span className="shrink-0 text-primary/60">{ev.type}{ev.event?.type ? `.${ev.event.type}` : ''}</span>
      <span className="truncate">{JSON.stringify(ev).slice(0, 200)}</span>
    </div>
  )
}
