// Types and pure helpers for the run observation pages (/jobs, /runners).
// Everything here is read-only data shaping; no component state lives here.
import type { RawEvent } from '../components/TraceView'

export type JobStatus = 'queued' | 'running' | 'success' | 'failed' | 'cancelled'

export interface Job {
  id: string
  repo: string
  issue: number
  pr: number
  title?: string
  kind: string
  operator: string
  stage: string
  trigger: string
  status: JobStatus | string
  detail?: string
  worker?: string
  attempt: number
  epoch: number
  tags: string[]
  queued_at: string
  started_at?: string
  ended_at?: string
  elapsed_ms?: number
  waiting_reason?: string
  error_kind?: string
  outcome?: string
  pr_url?: string
  has_logs: boolean
  generated_at: string
}

export interface JobAttempt {
  attempt: number
  epoch: number
  worker: string
  class: string
  status: string
  started_at: string
  ended_at?: string
  elapsed_ms?: number
  error_kind?: string
}

export interface TimelineEvent {
  at: string
  event: string
  worker?: string
  attempt?: number
  detail?: string
}

export interface JobDetail extends Job {
  attempts: JobAttempt[]
  timeline: TimelineEvent[]
  artifacts: { pr_number?: number; pr_url?: string; branch?: string; head_sha?: string; verdict?: string; next_owner?: string }
  summary?: string
  error?: string
  related: Job[]
}

export interface Runner {
  name: string
  online: boolean
  revoked: boolean
  paused: boolean
  tags: string[]
  roles: string[]
  max_parallel: number
  last_seen?: string
  last_heartbeat?: string
  running: Job[]
}

export const STATUS_LABEL: Record<string, string> = {
  queued: 'Queued', running: 'Running', success: 'Succeeded', failed: 'Failed', cancelled: 'Cancelled',
}

export const STAGE_LABEL: Record<string, string> = {
  implement: 'Implement', 'repair-ci': 'Repair CI', 'repair-acceptance': 'Repair (review)', 'repair-feedback': 'Repair (feedback)',
  'repair-conflict': 'Repair (conflict)', repair: 'Repair', acceptance: 'Acceptance', reply: 'Reply',
}

export const TRIGGER_LABEL: Record<string, string> = {
  issue_queued: 'Issue queued', issue_reopened: 'Issue reopened', pr_ready_for_review: 'PR ready for review',
  ci_failed: 'CI failed', acceptance_failed: 'Review failed', pr_feedback: 'PR feedback', merge_conflict: 'Merge conflict',
  development_verification: 'Verification failed', operator: 'Operator', repair: 'Repair', unknown: 'Unknown',
}

export const label = (table: Record<string, string>, key: string) => table[key] ?? key

const TERMINAL = new Set(['success', 'failed', 'cancelled'])
export const isTerminal = (status: string) => TERMINAL.has(status)

/** Parse an ISO time, rejecting Go's zero time and anything unparsable. */
export function parseTime(iso?: string | null): number | undefined {
  if (!iso || iso.startsWith('0001-')) return undefined
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : undefined
}

export function formatDuration(ms?: number | null): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  if (s < 1) return '<1s'
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

/**
 * Elapsed time of the current attempt. A running job counts up from the
 * controller-recorded start, but only while the data is fresh: on a stale or
 * disconnected snapshot the clock stops at the snapshot time, so the page never
 * invents progress it cannot see. A missing start is "unknown".
 */
export function jobElapsed(job: Pick<Job, 'status' | 'started_at' | 'ended_at' | 'elapsed_ms'>, now: number, live: boolean, asOf?: string): number | undefined {
  if (job.status === 'queued') return undefined
  if (job.status !== 'running') {
    if (job.elapsed_ms != null) return job.elapsed_ms
    const start = parseTime(job.started_at), end = parseTime(job.ended_at)
    return start != null && end != null && end >= start ? end - start : undefined
  }
  const start = parseTime(job.started_at)
  if (start == null) return undefined
  const until = live ? now : parseTime(asOf) ?? start
  return Math.max(0, until - start)
}

/** A running job whose lease ran out is not making progress; show why. */
export function detailLabel(detail?: string): string | undefined {
  switch (detail) {
    case 'lease_expired': return 'Lease expired'
    case 'recovery_pending': return 'Waiting for recovery'
    case 'finalizing': return 'Finalizing'
    default: return undefined
  }
}

export function heartbeatState(last: string | undefined, now: number): 'recent' | 'lost' | 'unknown' {
  const t = parseTime(last)
  if (t == null || t > now + 5000) return 'unknown'
  return now - t <= 120_000 ? 'recent' : 'lost'
}

/** Seconds-level relative time, e.g. "12s ago". */
export function ago(iso: string | undefined, now: number): string {
  const t = parseTime(iso)
  if (t == null) return 'never'
  const d = Math.max(0, now - t)
  return d < 1000 ? 'just now' : `${formatDuration(d)} ago`
}

// ---------- live log ----------

export interface LogEntry { id: number; invocation: string; event: JobLogEvent }
export interface JobLogEvent {
  seq: number
  ts?: string
  type: string
  role?: string
  text?: string
  tool_call_id?: string
  tool_name?: string
  arguments?: string
  output?: string
  is_error?: boolean
  exit_code?: number
  reason?: string
  status?: string
  error_kind?: string
}
export interface LogStreamInfo {
  invocation: string
  offload_run_id?: string
  availability: string
  reason?: string
  state: string
  completeness?: string
  event_count: number
  dropped_events?: number
  limited?: boolean
}
export interface LogFrame {
  task_id: string
  task_state?: string
  epoch: number
  epochs: { epoch: number; attempt: number; worker: string; status: string }[]
  active: boolean
  availability: string
  streams: LogStreamInfo[]
  entries: LogEntry[]
  next_after: number
}

/** Why a log is empty, in words a reader can act on. */
export function availabilityNote(a: string, reason?: string): string {
  const base: Record<string, string> = {
    not_captured: 'No output has been captured yet.',
    not_yet_available: 'Output is not available yet.',
    no_captured_content: 'The execution produced no capturable output.',
    capture_disabled: 'Output capture was disabled for this execution.',
    expired: 'Output has expired and was removed.',
    unreadable: 'Output could not be read.',
    unsupported_backend: 'This backend does not provide output.',
    unsupported_version: 'The recorded output format is not supported.',
    unsupported_offload: 'The installed Offload cannot capture output.',
    not_found: 'No output was found for this execution.',
    ambiguous: 'Output could not be matched to this execution.',
    identity_mismatch: 'Output did not match this execution.',
  }
  const text = base[a]
  if (!text) return ''
  return reason ? `${text} (${reason})` : text
}

function parseJSON(text?: string): unknown {
  if (!text) return {}
  try { return JSON.parse(text) } catch { return text }
}

/**
 * Convert stored Offload job-log events into the Claude stream-json shape that
 * TraceView already renders, so the live log reuses its steps, tool pairing,
 * scrolling and escaping instead of a second renderer. Tool calls and results
 * are paired by tool_call_id; gaps and backend errors become visible replies.
 */
export function logToTraceEvents(entries: { event: JobLogEvent }[]): RawEvent[] {
  const out: RawEvent[] = []
  for (const { event: e } of entries) {
    if (!e || typeof e.type !== 'string') continue
    const timestamp = e.ts
    switch (e.type) {
      case 'message': {
        const text = e.text ?? ''
        if (!text) break
        const role = e.role === 'user' ? 'user' : 'assistant'
        if (role === 'user') {
          out.push({ type: 'assistant', timestamp, message: { role: 'assistant', content: [{ type: 'text', text: `[prompt] ${text}` }] } })
        } else {
          out.push({ type: 'assistant', timestamp, message: { role: 'assistant', content: [{ type: e.role === 'reasoning' || e.role === 'thinking' ? 'thinking' : 'text', ...(e.role === 'reasoning' || e.role === 'thinking' ? { thinking: text } : { text }) }] } })
        }
        break
      }
      case 'tool_call':
        out.push({ type: 'assistant', timestamp, message: { role: 'assistant', content: [{ type: 'tool_use', id: e.tool_call_id || `seq-${e.seq}`, name: e.tool_name || 'tool', input: parseJSON(e.arguments) }] } })
        break
      case 'tool_result':
        out.push({ type: 'user', timestamp, message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: e.tool_call_id || '', content: e.output ?? '', is_error: !!e.is_error }] } })
        break
      case 'backend_error':
        out.push({ type: 'assistant', timestamp, message: { role: 'assistant', content: [{ type: 'text', text: `Backend error${e.error_kind ? ` · ${e.error_kind}` : ''}${e.text ? `: ${e.text}` : ''}` }] } })
        break
      case 'gap':
        out.push({ type: 'assistant', timestamp, message: { role: 'assistant', content: [{ type: 'text', text: `Output gap${e.reason ? ` · ${e.reason}` : ''} — some events were not captured` }] } })
        break
      case 'attempt_start':
        out.push({ type: 'assistant', timestamp, message: { role: 'assistant', content: [{ type: 'text', text: 'Attempt started' }] } })
        break
      case 'execution_end':
        out.push({ type: 'result', timestamp, is_error: !!e.status && e.status !== 'ok', subtype: e.status || 'finished', result: e.error_kind || undefined })
        break
    }
  }
  return out
}

/** Entries of a frame merged into what the viewer already has, by cursor id. */
export function mergeEntries(have: LogEntry[], incoming: LogEntry[]): LogEntry[] {
  if (incoming.length === 0) return have
  const last = have.length ? have[have.length - 1].id : 0
  const fresh = incoming.filter(e => e.id > last)
  return fresh.length ? [...have, ...fresh] : have
}

/** Reconnect delay: 1s, 2s, 4s … capped at 15s. */
export function reconnectDelay(attempt: number): number {
  return Math.min(15_000, 1000 * 2 ** Math.max(0, attempt))
}
