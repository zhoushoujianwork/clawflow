import { CheckCircle2, Clock, Loader2, Square, XCircle } from 'lucide-react'
import { cn } from '../lib/utils'
import { STATUS_LABEL } from '../lib/jobs'

const STYLE: Record<string, { cls: string; Icon: typeof Clock }> = {
  queued: { cls: 'bg-muted text-muted-foreground border-border', Icon: Clock },
  running: { cls: 'bg-blue-100 text-blue-700 border-blue-200', Icon: Loader2 },
  success: { cls: 'bg-green-100 text-green-700 border-green-200', Icon: CheckCircle2 },
  failed: { cls: 'bg-red-100 text-red-700 border-red-200', Icon: XCircle },
  cancelled: { cls: 'bg-amber-50 text-amber-700 border-amber-200', Icon: Square },
}

/**
 * Task status badge. The spinner turns only while the data is live: on a stale
 * or disconnected snapshot a "running" job keeps its label but stops animating.
 * Success means the task finished, not that CI, review or merge did.
 */
export function JobStatusBadge({ status, live = true, detail }: { status: string; live?: boolean; detail?: string }) {
  const { cls, Icon } = STYLE[status] ?? STYLE.queued
  return (
    <span className={cn('inline-flex items-center gap-1 border px-1.5 py-0.5 rounded text-[11px] font-semibold whitespace-nowrap', cls)} data-status={status}>
      <Icon className={cn('w-3 h-3', status === 'running' && live && !detail && 'animate-spin')} aria-hidden="true" />
      {STATUS_LABEL[status] ?? status}
    </span>
  )
}

export function FreshnessBanner({ live, error, hasData }: { live: boolean; error: string; hasData: boolean }) {
  if (live && !error) return null
  return (
    <p role="status" className="text-xs rounded border border-amber-200 bg-amber-50 text-amber-800 px-3 py-2 mb-3">
      {hasData ? 'Connection lost — showing the last data received; durations and spinners are paused.' : 'Waiting for the first response…'}
      {error ? ` (${error})` : ''}
    </p>
  )
}
