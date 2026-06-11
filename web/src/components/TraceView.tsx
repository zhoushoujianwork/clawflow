import { useEffect, useRef, useState } from 'react'
import {
  Check,
  CheckCircle2,
  ChevronRight,
  ChevronsDown,
  ChevronsUp,
  Loader2,
  XCircle,
} from 'lucide-react'
import { cn } from '../lib/utils'
import { Markdown } from './Markdown'

/**
 * Content block inside an assistant or user message. The shapes we care about:
 *   - { type: "thinking", thinking: "…" }   — model's chain-of-thought
 *   - { type: "text", text: "…" }           — model's reply text
 *   - { type: "tool_use", id, name, input } — tool call from the model
 *   - { type: "tool_result", tool_use_id, content, is_error } — runner's reply
 * Anything else is ignored at render time.
 */
export interface ContentBlock {
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
export interface RawEvent {
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

type ToolStep = {
  kind: 'tool'
  name: string
  input: unknown
  result?: { body: string; isError: boolean }
}

type TraceStep =
  | { kind: 'thinking'; text: string }
  | { kind: 'reply'; text: string }
  | ToolStep
  | {
      kind: 'final'
      isError: boolean
      subtype?: string
      durationMs?: number
      costUsd?: number
      text?: string
    }

function resultBody(content: unknown): string {
  if (typeof content === 'string') return content
  if (Array.isArray(content)) {
    return content
      .map(p =>
        p && typeof p === 'object' && 'text' in p
          ? String((p as { text: unknown }).text)
          : prettyValue(p)
      )
      .join('\n')
  }
  return prettyValue(content)
}

/**
 * Flatten the raw event stream into render-ready steps. tool_result events
 * are folded into their originating tool_use step (matched by tool_use_id)
 * so a call and its output read as one expandable row instead of two
 * disconnected lines.
 */
function buildSteps(events: RawEvent[]): TraceStep[] {
  const steps: TraceStep[] = []
  const pendingTools = new Map<string, ToolStep>()

  for (const ev of events) {
    if (ev.type === 'assistant' && ev.message?.content) {
      for (const c of ev.message.content) {
        if (c.type === 'thinking' && c.thinking?.trim()) {
          steps.push({ kind: 'thinking', text: c.thinking.trim() })
        } else if (c.type === 'text' && c.text) {
          steps.push({ kind: 'reply', text: c.text })
        } else if (c.type === 'tool_use') {
          const step: ToolStep = { kind: 'tool', name: c.name || 'tool', input: c.input ?? {} }
          steps.push(step)
          if (c.id) pendingTools.set(c.id, step)
        }
      }
    } else if (ev.type === 'user' && ev.message?.content) {
      for (const c of ev.message.content) {
        if (c.type !== 'tool_result') continue
        const body = resultBody(c.content)
        const target = c.tool_use_id ? pendingTools.get(c.tool_use_id) : undefined
        if (target && !target.result) {
          target.result = { body, isError: !!c.is_error }
        } else {
          // orphan result (no matching tool_use seen) — still show it
          steps.push({ kind: 'tool', name: 'tool', input: null, result: { body, isError: !!c.is_error } })
        }
      }
    } else if (ev.type === 'result') {
      steps.push({
        kind: 'final',
        isError: !!ev.is_error,
        subtype: ev.subtype,
        durationMs: ev.duration_ms,
        costUsd: ev.total_cost_usd,
        text: ev.result,
      })
    }
  }
  return steps
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
 * Pretty-print a tool input for the expanded view. Well-known multiline
 * string fields (command, content, new_string, etc.) are rendered raw so
 * embedded \n/\t sequences appear as real whitespace instead of literal
 * escape characters from JSON.stringify.
 */
function prettyInput(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v

  if (typeof v === 'object' && !Array.isArray(v)) {
    const obj = v as Record<string, unknown>
    const MULTILINE_KEYS = ['command', 'content', 'new_string', 'old_string', 'description', 'text', 'body']
    const parts: string[] = []
    for (const [key, val] of Object.entries(obj)) {
      if (MULTILINE_KEYS.includes(key) && typeof val === 'string') {
        parts.push(`${key}: ${val}`)
      } else {
        try {
          parts.push(`${key}: ${JSON.stringify(val, null, 2)}`)
        } catch {
          parts.push(`${key}: ${String(val)}`)
        }
      }
    }
    return parts.join('\n')
  }

  try {
    return JSON.stringify(v, null, 2).replace(/\\n/g, '\n').replace(/\\t/g, '\t')
  } catch {
    return String(v)
  }
}

/**
 * One-line preview of a tool call shown next to the tool-name chip, e.g.
 * the shell command for Bash or the file path for Edit/Read. Falls back to
 * the first string field, then compact JSON.
 */
function toolSummary(input: unknown): string {
  if (input == null) return ''
  if (typeof input === 'string') return input.replace(/\s+/g, ' ')
  if (typeof input !== 'object') return String(input)
  const obj = input as Record<string, unknown>
  const PREFERRED = ['command', 'file_path', 'path', 'pattern', 'query', 'url', 'prompt', 'description']
  for (const k of PREFERRED) {
    const v = obj[k]
    if (typeof v === 'string' && v) return v.replace(/\s+/g, ' ')
  }
  const first = Object.values(obj).find(v => typeof v === 'string' && v)
  if (typeof first === 'string') return first.replace(/\s+/g, ' ')
  try {
    const s = JSON.stringify(obj)
    return s === '{}' ? '' : s
  } catch {
    return ''
  }
}

function firstLine(text: string): string {
  return text.split('\n', 1)[0]
}

export function TraceView({
  events,
  loading,
  running,
  openByDefault,
}: {
  events: RawEvent[]
  loading: boolean
  running: boolean
  openByDefault: boolean
}) {
  const listRef = useRef<HTMLDivElement>(null)
  const isAtBottom = useRef(true)
  const steps = buildSteps(events)

  useEffect(() => {
    if (isAtBottom.current && listRef.current) {
      listRef.current.scrollTop = listRef.current.scrollHeight
    }
  }, [steps.length])

  const handleScroll = () => {
    const el = listRef.current
    if (!el) return
    isAtBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
  }

  return (
    <details open={openByDefault} className="group min-w-0">
      <summary className="cursor-pointer select-none flex items-center gap-2 text-sm font-semibold text-foreground hover:text-foreground/80">
        <ChevronRight className="w-4 h-4 transition-transform group-open:rotate-90" />
        Trace
        <span className="font-normal text-muted-foreground">({steps.length} steps)</span>
      </summary>
      <div
        className="mt-3 rounded-lg overflow-hidden"
        style={{ border: '1px solid hsl(var(--border))', background: 'hsl(var(--bg-primary))' }}
      >
        <div
          className="flex items-center justify-between px-3 py-2"
          style={{ borderBottom: '1px solid hsl(var(--border))', background: 'hsl(var(--bg-secondary))' }}
        >
          <span className="text-xs font-medium inline-flex items-center gap-1.5" style={{ color: 'hsl(var(--text-low))' }}>
            {running && <Loader2 className="w-3 h-3 animate-spin" />}
            {steps.length} steps
          </span>
          <div className="flex items-center gap-2">
            <button
              onClick={() => { if (listRef.current) listRef.current.scrollTop = 0 }}
              className="hover:opacity-70 cursor-pointer"
              style={{ color: 'hsl(var(--text-low))' }}
              title="Scroll to top"
            >
              <ChevronsUp className="w-3.5 h-3.5" />
            </button>
            <button
              onClick={() => { if (listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight }}
              className="hover:opacity-70 cursor-pointer"
              style={{ color: 'hsl(var(--text-low))' }}
              title="Scroll to bottom"
            >
              <ChevronsDown className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
        <div
          ref={listRef}
          onScroll={handleScroll}
          className="max-h-[560px] overflow-y-auto overflow-x-hidden min-w-0 px-1.5 py-1"
        >
          {loading ? (
            <p className="text-sm px-2 py-3" style={{ color: 'hsl(var(--text-low))' }}>Loading…</p>
          ) : steps.length === 0 ? (
            <p className="text-sm px-2 py-3" style={{ color: 'hsl(var(--text-low))' }}>No trace yet.</p>
          ) : (
            steps.map((step, i) => (
              <StepRow
                key={i}
                step={step}
                isLast={i === steps.length - 1}
                running={running}
              />
            ))
          )}
        </div>
      </div>
    </details>
  )
}

/** Small colored tag with the tool name, tinted by result state. */
function ToolChip({ name, isError }: { name: string; isError: boolean }) {
  const hue = isError ? '--error' : '--success'
  return (
    <span
      className="shrink-0 px-1.5 py-px rounded text-[11px] font-semibold font-mono"
      style={{ background: `hsl(var(${hue}) / 0.12)`, color: `hsl(var(${hue}))` }}
    >
      {name}
    </span>
  )
}

function StepRow({ step, isLast, running }: { step: TraceStep; isLast: boolean; running: boolean }) {
  const [open, setOpen] = useState(false)

  if (step.kind === 'final') {
    const Icon = step.isError ? XCircle : CheckCircle2
    return (
      <div className="mx-0.5 my-1 pt-1.5" style={{ borderTop: '1px solid hsl(var(--border))' }}>
        <button
          onClick={() => setOpen(o => !o)}
          className="w-full flex items-center gap-2 px-1.5 py-1 rounded text-left hover:bg-accent/50 cursor-pointer min-w-0"
        >
          <Icon
            className="w-3.5 h-3.5 shrink-0"
            style={{ color: step.isError ? 'hsl(var(--error))' : 'hsl(var(--success))' }}
          />
          <span className="text-[13px] font-medium truncate" style={{ color: 'hsl(var(--text-high))' }}>
            {step.subtype || 'result'}
          </span>
          <span className="text-xs shrink-0" style={{ color: 'hsl(var(--text-low))' }}>
            {step.durationMs != null && `${Math.round(step.durationMs / 1000)}s`}
            {step.costUsd != null && ` · $${step.costUsd.toFixed(4)}`}
          </span>
          <span className="flex-1" />
          <ChevronRight
            className={cn('w-3.5 h-3.5 shrink-0 transition-transform', open && 'rotate-90')}
            style={{ color: 'hsl(var(--text-low))' }}
          />
        </button>
        {open && (
          <div className="ml-7 mr-2 mb-2 mt-1">
            {step.text ? (
              <div
                className="rounded p-3 text-sm"
                style={{ background: 'hsl(var(--bg-secondary))', border: '1px solid hsl(var(--border))' }}
              >
                <Markdown>{step.text}</Markdown>
              </div>
            ) : (
              <p className="text-xs italic" style={{ color: 'hsl(var(--text-low))' }}>
                (empty — no stdout)
              </p>
            )}
          </div>
        )}
      </div>
    )
  }

  if (step.kind === 'thinking' || step.kind === 'reply') {
    const isThinking = step.kind === 'thinking'
    return (
      <div className="min-w-0">
        <button
          onClick={() => setOpen(o => !o)}
          className="w-full flex items-center gap-2 px-1.5 py-1 rounded text-left hover:bg-accent/50 cursor-pointer min-w-0"
        >
          <Check className="w-3.5 h-3.5 shrink-0" style={{ color: 'hsl(var(--text-low) / 0.7)' }} />
          <span
            className={cn('flex-1 min-w-0 truncate text-[13px] italic')}
            style={{ color: isThinking ? 'hsl(var(--text-low))' : 'hsl(var(--text-normal))' }}
          >
            {firstLine(step.text)}
          </span>
          <ChevronRight
            className={cn('w-3.5 h-3.5 shrink-0 transition-transform', open && 'rotate-90')}
            style={{ color: 'hsl(var(--text-low))' }}
          />
        </button>
        {open && (
          <div className="ml-7 mr-2 mb-2 mt-0.5">
            {isThinking ? (
              <p
                className="text-[13px] italic whitespace-pre-wrap break-words"
                style={{ color: 'hsl(var(--text-low))' }}
              >
                {step.text}
              </p>
            ) : (
              <div
                className="rounded p-3 text-sm"
                style={{ background: 'hsl(var(--bg-secondary))', border: '1px solid hsl(var(--border))' }}
              >
                <Markdown>{step.text}</Markdown>
              </div>
            )}
          </div>
        )}
      </div>
    )
  }

  // tool step
  const isError = !!step.result?.isError
  const inProgress = !step.result && isLast && running
  const summary = toolSummary(step.input)
  const inputStr = prettyInput(step.input)

  return (
    <div className="min-w-0">
      <button
        onClick={() => setOpen(o => !o)}
        className="w-full flex items-center gap-2 px-1.5 py-1 rounded text-left hover:bg-accent/50 cursor-pointer min-w-0"
      >
        {inProgress ? (
          <Loader2 className="w-3.5 h-3.5 shrink-0 animate-spin" style={{ color: 'hsl(var(--_info))' }} />
        ) : (
          <Check
            className="w-3.5 h-3.5 shrink-0"
            style={{ color: isError ? 'hsl(var(--error))' : 'hsl(var(--text-low) / 0.7)' }}
          />
        )}
        <ToolChip name={step.name} isError={isError} />
        <span
          className="flex-1 min-w-0 truncate text-[13px] font-mono"
          style={{ color: 'hsl(var(--text-normal))' }}
        >
          {summary}
        </span>
        <ChevronRight
          className={cn('w-3.5 h-3.5 shrink-0 transition-transform', open && 'rotate-90')}
          style={{ color: 'hsl(var(--text-low))' }}
        />
      </button>
      {open && (
        <div className="ml-7 mr-2 mb-2 mt-0.5 space-y-1.5 min-w-0">
          {inputStr && inputStr !== '{}' && (
            <pre
              className="rounded p-2 text-xs font-mono whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
              style={{
                background: 'hsl(var(--bg-secondary))',
                border: '1px solid hsl(var(--border))',
                color: 'hsl(var(--text-normal))',
              }}
            >
              {inputStr}
            </pre>
          )}
          {step.result && step.result.body && (
            <pre
              className="rounded p-2 text-xs font-mono whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
              style={{
                background: 'hsl(var(--bg-secondary))',
                border: isError
                  ? '1px solid hsl(var(--error) / 0.5)'
                  : '1px solid hsl(var(--border))',
                color: isError ? 'hsl(var(--error))' : 'hsl(var(--text-low))',
              }}
            >
              {step.result.body}
            </pre>
          )}
          {!step.result && !inProgress && (
            <p className="text-xs italic" style={{ color: 'hsl(var(--text-low))' }}>
              (no result recorded)
            </p>
          )}
        </div>
      )}
    </div>
  )
}
