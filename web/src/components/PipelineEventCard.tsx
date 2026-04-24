import { useState } from 'react'
import {
  Brain,
  Wrench,
  Terminal,
  Sparkles,
  Zap,
  CheckCircle2,
  Clock,
  MessageSquare,
  ChevronDown,
  ChevronRight,
  AlertCircle,
  Loader2,
} from 'lucide-react'
import { cn } from '../lib/utils'

// ── Types ────────────────────────────────────────────────────────────────────

export interface LogEntry {
  level: 'info' | 'warn' | 'error'
  message: string
  timestamp: string
  raw_event?: unknown
}

type ContentBlock =
  | { type: 'thinking'; thinking: string }
  | { type: 'text'; text: string }
  | { type: 'tool_use'; id: string; name: string; input: Record<string, unknown> }
  | { type: 'tool_result'; tool_use_id: string; content?: unknown; is_error?: boolean }

interface RawEvent {
  type?: string
  subtype?: string
  message?: {
    role?: string
    content?: ContentBlock[]
    usage?: { input_tokens?: number; output_tokens?: number; cache_read_input_tokens?: number }
    model?: string
  }
  model?: string
  cwd?: string
  tools?: string[]
  skills?: string[]
  mcp_servers?: unknown[]
  permissionMode?: string
  session_id?: string
  summary?: string
  description?: string
  task_type?: string
  task_id?: string
  status?: string
  rate_limit_info?: {
    status?: string
    rateLimitType?: string
    resetsAt?: number
    isUsingOverage?: boolean
  }
}

// ── Shared card chrome ───────────────────────────────────────────────────────

interface CardShellProps {
  icon: React.ReactNode
  title: React.ReactNode
  meta?: React.ReactNode
  accent: string // tailwind border-l color e.g. 'border-l-blue-500'
  iconBg: string // e.g. 'bg-blue-500/10 text-blue-400'
  timestamp: string
  children?: React.ReactNode
  rawEvent?: unknown
  showRaw?: boolean
}

function CardShell({ icon, title, meta, accent, iconBg, timestamp, children, rawEvent, showRaw }: CardShellProps) {
  return (
    <div className={cn('mb-2 rounded-md border border-slate-800 border-l-2 bg-slate-900/60', accent)}>
      <div className="flex items-start gap-2.5 px-3 py-2">
        <div className={cn('shrink-0 w-6 h-6 rounded flex items-center justify-center mt-0.5', iconBg)}>
          {icon}
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-baseline gap-2 flex-wrap">
            <div className="text-[13px] font-medium text-slate-200">{title}</div>
            {meta && <div className="text-[11px] text-slate-500">{meta}</div>}
            <div className="ml-auto text-[10px] text-slate-600 font-mono">
              {new Date(timestamp).toLocaleTimeString()}
            </div>
          </div>
          {children && <div className="mt-1.5">{children}</div>}
        </div>
      </div>
      {showRaw && rawEvent ? (
        <pre className="mx-3 mb-2 px-2 py-1.5 bg-slate-950 border border-slate-800 rounded text-[10px] text-slate-400 whitespace-pre-wrap break-all overflow-x-auto">
          {JSON.stringify(rawEvent, null, 2)}
        </pre>
      ) : null}
    </div>
  )
}

// ── Collapsible block (thinking / long tool output) ──────────────────────────

function Collapsible({ preview, full, open: initial = false }: { preview: string; full?: string; open?: boolean }) {
  const [open, setOpen] = useState(initial)
  const hasMore = full && full !== preview && full.length > preview.length
  return (
    <div className="text-[12px] text-slate-400 leading-relaxed">
      <div className="whitespace-pre-wrap break-words font-[inherit]">
        {open && full ? full : preview}
      </div>
      {hasMore && (
        <button
          onClick={() => setOpen((v) => !v)}
          className="mt-1 inline-flex items-center gap-0.5 text-[11px] text-slate-500 hover:text-slate-300"
        >
          {open ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
          {open ? 'collapse' : 'expand'}
        </button>
      )}
    </div>
  )
}

// ── Individual cards ─────────────────────────────────────────────────────────

function SystemInitCard({ event, timestamp, showRaw }: { event: RawEvent; timestamp: string; showRaw: boolean }) {
  const counts = [
    event.tools?.length && `${event.tools.length} tools`,
    event.skills?.length && `${event.skills.length} skills`,
    event.mcp_servers?.length && `${event.mcp_servers.length} MCP`,
  ].filter(Boolean).join(' · ')

  return (
    <CardShell
      icon={<Sparkles className="w-3.5 h-3.5" />}
      iconBg="bg-indigo-500/15 text-indigo-300"
      accent="border-l-indigo-500"
      title="Session started"
      meta={event.model || 'claude'}
      timestamp={timestamp}
      rawEvent={event}
      showRaw={showRaw}
    >
      <div className="text-[11px] text-slate-500 space-y-0.5">
        {event.cwd && <div className="font-mono truncate">📂 {event.cwd}</div>}
        {counts && <div>{counts}</div>}
        {event.permissionMode && <div>mode: <span className="text-slate-400">{event.permissionMode}</span></div>}
      </div>
    </CardShell>
  )
}

function TaskStartedCard({ event, timestamp, showRaw }: { event: RawEvent; timestamp: string; showRaw: boolean }) {
  return (
    <CardShell
      icon={<Zap className="w-3.5 h-3.5" />}
      iconBg="bg-amber-500/15 text-amber-300"
      accent="border-l-amber-500"
      title={event.description || 'Subagent task'}
      meta={event.task_type}
      timestamp={timestamp}
      rawEvent={event}
      showRaw={showRaw}
    />
  )
}

function TaskNotificationCard({ event, timestamp, showRaw }: { event: RawEvent; timestamp: string; showRaw: boolean }) {
  const ok = event.status === 'completed'
  return (
    <CardShell
      icon={ok ? <CheckCircle2 className="w-3.5 h-3.5" /> : <AlertCircle className="w-3.5 h-3.5" />}
      iconBg={ok ? 'bg-emerald-500/15 text-emerald-300' : 'bg-red-500/15 text-red-300'}
      accent={ok ? 'border-l-emerald-500' : 'border-l-red-500'}
      title={`Task ${event.status ?? 'finished'}: ${event.summary ?? event.description ?? ''}`}
      timestamp={timestamp}
      rawEvent={event}
      showRaw={showRaw}
    />
  )
}

function TaskProgressCard({ event, timestamp, showRaw }: { event: RawEvent; timestamp: string; showRaw: boolean }) {
  const text = event.summary ?? event.description ?? 'progress'
  return (
    <CardShell
      icon={<Loader2 className="w-3.5 h-3.5 animate-spin" />}
      iconBg="bg-amber-500/10 text-amber-300/80"
      accent="border-l-amber-500/40"
      title={<span className="text-slate-400 font-normal">{text}</span>}
      timestamp={timestamp}
      rawEvent={event}
      showRaw={showRaw}
    />
  )
}

function ThinkingCard({ text, timestamp, showRaw, rawEvent }: { text: string; timestamp: string; showRaw: boolean; rawEvent: unknown }) {
  const preview = text.length > 280 ? text.slice(0, 280) + '…' : text
  return (
    <CardShell
      icon={<Brain className="w-3.5 h-3.5" />}
      iconBg="bg-purple-500/15 text-purple-300"
      accent="border-l-purple-500"
      title="Thinking"
      timestamp={timestamp}
      rawEvent={rawEvent}
      showRaw={showRaw}
    >
      <div className="italic">
        <Collapsible preview={preview} full={text} />
      </div>
    </CardShell>
  )
}

function AssistantTextCard({ text, model, timestamp, showRaw, rawEvent }: { text: string; model?: string; timestamp: string; showRaw: boolean; rawEvent: unknown }) {
  return (
    <CardShell
      icon={<MessageSquare className="w-3.5 h-3.5" />}
      iconBg="bg-sky-500/15 text-sky-300"
      accent="border-l-sky-500"
      title="Assistant"
      meta={model}
      timestamp={timestamp}
      rawEvent={rawEvent}
      showRaw={showRaw}
    >
      <Collapsible preview={text.length > 400 ? text.slice(0, 400) + '…' : text} full={text} />
    </CardShell>
  )
}

function ToolUseCard({ name, input, timestamp, showRaw, rawEvent }: { name: string; input: Record<string, unknown>; timestamp: string; showRaw: boolean; rawEvent: unknown }) {
  // Summarize the most useful input field for common tools
  const summary = (() => {
    if (name === 'Bash' && typeof input.command === 'string') return input.command
    if ((name === 'Read' || name === 'Edit' || name === 'Write') && typeof input.file_path === 'string') return input.file_path
    if (name === 'Grep' && typeof input.pattern === 'string') return `pattern: ${input.pattern}`
    if (name === 'Glob' && typeof input.pattern === 'string') return input.pattern
    if (name === 'WebFetch' && typeof input.url === 'string') return input.url
    // fallback: stringify up to ~120 chars
    const s = JSON.stringify(input)
    return s.length > 140 ? s.slice(0, 140) + '…' : s
  })()

  const isShell = name === 'Bash'
  return (
    <CardShell
      icon={isShell ? <Terminal className="w-3.5 h-3.5" /> : <Wrench className="w-3.5 h-3.5" />}
      iconBg="bg-orange-500/15 text-orange-300"
      accent="border-l-orange-500"
      title={name}
      timestamp={timestamp}
      rawEvent={rawEvent}
      showRaw={showRaw}
    >
      <code className="block text-[11.5px] font-mono text-slate-300 bg-slate-950/70 px-2 py-1 rounded break-all whitespace-pre-wrap">
        {summary}
      </code>
    </CardShell>
  )
}

function ToolResultCard({ content, isError, timestamp, showRaw, rawEvent }: { content: unknown; isError?: boolean; timestamp: string; showRaw: boolean; rawEvent: unknown }) {
  const text = (() => {
    if (typeof content === 'string') return content
    if (Array.isArray(content)) {
      return content.map((c) => (typeof c === 'object' && c && 'text' in c ? (c as { text: string }).text : JSON.stringify(c))).join('\n')
    }
    return content ? JSON.stringify(content) : ''
  })()
  const preview = text.length > 320 ? text.slice(0, 320) + '…' : text
  return (
    <CardShell
      icon={isError ? <AlertCircle className="w-3.5 h-3.5" /> : <Terminal className="w-3.5 h-3.5" />}
      iconBg={isError ? 'bg-red-500/15 text-red-300' : 'bg-slate-500/15 text-slate-300'}
      accent={isError ? 'border-l-red-500' : 'border-l-slate-600'}
      title={isError ? 'Tool error' : 'Tool output'}
      timestamp={timestamp}
      rawEvent={rawEvent}
      showRaw={showRaw}
    >
      <pre className="text-[11.5px] font-mono text-slate-300 bg-slate-950/70 px-2 py-1 rounded whitespace-pre-wrap break-all max-h-48 overflow-y-auto">
        <Collapsible preview={preview} full={text} />
      </pre>
    </CardShell>
  )
}

function RateLimitCard({ event, timestamp, showRaw }: { event: RawEvent; timestamp: string; showRaw: boolean }) {
  const info = event.rate_limit_info
  const resets = info?.resetsAt ? new Date(info.resetsAt * 1000) : null
  const ok = info?.status === 'allowed'
  return (
    <CardShell
      icon={<Clock className="w-3.5 h-3.5" />}
      iconBg={ok ? 'bg-teal-500/15 text-teal-300' : 'bg-yellow-500/15 text-yellow-300'}
      accent={ok ? 'border-l-teal-500' : 'border-l-yellow-500'}
      title={`Rate limit: ${info?.status ?? 'unknown'}`}
      meta={info?.rateLimitType}
      timestamp={timestamp}
      rawEvent={event}
      showRaw={showRaw}
    >
      {resets && (
        <div className="text-[11px] text-slate-500">
          resets {resets.toLocaleString()}
          {info?.isUsingOverage ? ' · overage active' : ''}
        </div>
      )}
    </CardShell>
  )
}

function PlainLogLine({ entry }: { entry: LogEntry }) {
  return (
    <div className="mb-1 font-mono text-xs">
      <span className="text-slate-500 mr-2">{new Date(entry.timestamp).toLocaleTimeString()}</span>
      <span className={cn('mr-1.5', entry.level === 'info' ? 'text-green-400' : entry.level === 'warn' ? 'text-yellow-400' : 'text-red-400')}>
        [{entry.level}]
      </span>
      <span className="text-slate-200 whitespace-pre-wrap break-all">{entry.message}</span>
    </div>
  )
}

// ── Sub-agent group (task_started → task_progress* → task_notification) ─────

function eventTaskId(entry: LogEntry): string | undefined {
  const ev = entry.raw_event as RawEvent | undefined
  if (!ev || ev.type !== 'system') return undefined
  if (ev.subtype !== 'task_started' && ev.subtype !== 'task_progress' && ev.subtype !== 'task_notification') return undefined
  return ev.task_id
}

function SubagentGroup({ entries, showRaw }: { entries: LogEntry[]; showRaw: boolean }) {
  const [open, setOpen] = useState(false)
  const started = entries[0].raw_event as RawEvent
  const last = entries[entries.length - 1].raw_event as RawEvent
  const finished = last.subtype === 'task_notification'
  const ok = finished && last.status === 'completed'

  const progressCount = entries.filter((e) => (e.raw_event as RawEvent | undefined)?.subtype === 'task_progress').length
  const latestProgress = [...entries].reverse().find((e) => (e.raw_event as RawEvent | undefined)?.subtype === 'task_progress')
  const latestText = (latestProgress?.raw_event as RawEvent | undefined)?.summary
    ?? (latestProgress?.raw_event as RawEvent | undefined)?.description

  const Icon = !finished ? Loader2 : ok ? CheckCircle2 : AlertCircle
  const iconBg = !finished
    ? 'bg-amber-500/15 text-amber-300'
    : ok
      ? 'bg-emerald-500/15 text-emerald-300'
      : 'bg-red-500/15 text-red-300'
  const accent = !finished
    ? 'border-l-amber-500'
    : ok
      ? 'border-l-emerald-500'
      : 'border-l-red-500'

  return (
    <div className={cn('mb-2 rounded-md border border-slate-800 border-l-2 bg-slate-900/60', accent)}>
      <button
        onClick={() => setOpen((v) => !v)}
        className="w-full text-left flex items-start gap-2.5 px-3 py-2 hover:bg-slate-800/40 transition-colors"
      >
        <div className={cn('shrink-0 w-6 h-6 rounded flex items-center justify-center mt-0.5', iconBg)}>
          <Icon className={cn('w-3.5 h-3.5', !finished && 'animate-spin')} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-baseline gap-2 flex-wrap">
            <div className="text-[13px] font-medium text-slate-200 truncate">
              {started.description || 'Subagent task'}
            </div>
            {started.task_type && <div className="text-[11px] text-slate-500">{started.task_type}</div>}
            <div className="text-[11px] text-slate-500">· {progressCount} step{progressCount === 1 ? '' : 's'}</div>
            <div className="ml-auto flex items-center gap-1 text-[10px] text-slate-600 font-mono">
              {open ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
              {new Date(entries[0].timestamp).toLocaleTimeString()}
            </div>
          </div>
          {!open && latestText && (
            <div className="mt-1 text-[12px] text-slate-400 truncate">{latestText}</div>
          )}
          {!open && finished && last.summary && (
            <div className="mt-1 text-[12px] text-slate-400 break-words">{last.summary}</div>
          )}
        </div>
      </button>
      {open && (
        <div className="px-3 pb-2 pl-9 border-t border-slate-800/60">
          {entries.map((entry, i) => (
            <PipelineEventCard key={i} entry={entry} showRaw={showRaw} />
          ))}
        </div>
      )}
    </div>
  )
}

export function PipelineEventList({ entries, showRaw }: { entries: LogEntry[]; showRaw: boolean }) {
  // Walk the log linearly, folding task_started…task_notification (matched by
  // task_id) into a single SubagentGroup. Anything between with the same
  // task_id is part of the sub-agent run; events without a task_id fall
  // through as standalone cards.
  const out: React.ReactNode[] = []
  let i = 0
  while (i < entries.length) {
    const ev = entries[i].raw_event as RawEvent | undefined
    if (ev?.type === 'system' && ev.subtype === 'task_started' && ev.task_id) {
      const taskId = ev.task_id
      const group: LogEntry[] = [entries[i]]
      let j = i + 1
      while (j < entries.length) {
        const tid = eventTaskId(entries[j])
        if (tid !== taskId) break
        group.push(entries[j])
        const nev = entries[j].raw_event as RawEvent
        j += 1
        if (nev.subtype === 'task_notification') break
      }
      out.push(<SubagentGroup key={`g-${i}`} entries={group} showRaw={showRaw} />)
      i = j
      continue
    }
    out.push(<PipelineEventCard key={i} entry={entries[i]} showRaw={showRaw} />)
    i += 1
  }
  return <>{out}</>
}

// ── Dispatcher ───────────────────────────────────────────────────────────────

export function PipelineEventCard({ entry, showRaw }: { entry: LogEntry; showRaw: boolean }) {
  const event = entry.raw_event as RawEvent | undefined

  if (!event || !event.type) {
    return <PlainLogLine entry={entry} />
  }

  if (event.type === 'system') {
    if (event.subtype === 'init') return <SystemInitCard event={event} timestamp={entry.timestamp} showRaw={showRaw} />
    if (event.subtype === 'task_started') return <TaskStartedCard event={event} timestamp={entry.timestamp} showRaw={showRaw} />
    if (event.subtype === 'task_progress') return <TaskProgressCard event={event} timestamp={entry.timestamp} showRaw={showRaw} />
    if (event.subtype === 'task_notification') return <TaskNotificationCard event={event} timestamp={entry.timestamp} showRaw={showRaw} />
    return <PlainLogLine entry={entry} />
  }

  if (event.type === 'rate_limit_event') {
    return <RateLimitCard event={event} timestamp={entry.timestamp} showRaw={showRaw} />
  }

  if (event.type === 'assistant' && event.message?.content) {
    // An assistant message may contain multiple blocks — render one card per block.
    return (
      <>
        {event.message.content.map((block, i) => {
          if (block.type === 'thinking') {
            return <ThinkingCard key={i} text={block.thinking} timestamp={entry.timestamp} showRaw={showRaw} rawEvent={event} />
          }
          if (block.type === 'text') {
            return <AssistantTextCard key={i} text={block.text} model={event.message?.model} timestamp={entry.timestamp} showRaw={showRaw} rawEvent={event} />
          }
          if (block.type === 'tool_use') {
            return <ToolUseCard key={i} name={block.name} input={block.input} timestamp={entry.timestamp} showRaw={showRaw} rawEvent={event} />
          }
          return <PlainLogLine key={i} entry={entry} />
        })}
      </>
    )
  }

  if (event.type === 'user' && event.message?.content) {
    return (
      <>
        {event.message.content.map((block, i) => {
          if (block.type === 'tool_result') {
            return <ToolResultCard key={i} content={block.content} isError={block.is_error} timestamp={entry.timestamp} showRaw={showRaw} rawEvent={event} />
          }
          return <PlainLogLine key={i} entry={entry} />
        })}
      </>
    )
  }

  return <PlainLogLine entry={entry} />
}
