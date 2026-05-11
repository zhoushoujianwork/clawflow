import { useEffect } from 'react'
import { X } from 'lucide-react'
import { cn } from '../lib/utils'

// PilotDuty mirrors snapshot.PilotDuty (Go) for one of the four
// actionable duties Pilot reports on each wake. Status vocabulary is
// fixed; anything else gets normalized to "ok" by the runner.
export interface PilotDuty {
  status: 'ok' | 'action_taken' | 'flagged' | 'error' | string
  actions?: string[]
  note?: string
}

// PilotIssueDigest is the passive review duty — counts come from the
// runner (always accurate); summary is the prose Pilot wrote.
export interface PilotIssueDigest {
  since_hours?: number
  new?: number
  closed?: number
  labeled?: number
  commented?: number
  summary?: string
}

export interface PilotDuties {
  pr_triage: PilotDuty
  monitoring: PilotDuty
  doc_sync: PilotDuty
  issue_digest: PilotIssueDigest
  backlog_hygiene: PilotDuty
}

export interface PilotRun {
  project: string
  started_at: string
  ended_at?: string
  status: string
  result?: string
  error?: string
  summary?: string
  duties?: PilotDuties
  verdict?: string
  usage?: {
    duration_ms: number
    num_turns: number
    total_cost_usd: number
    input_tokens: number
    output_tokens: number
  }
}

// DUTY_KEYS / DUTY_LABELS / dutyStatusColour are the constant per-duty
// UI metadata, exported so every Pilot surface (project page card,
// /pilot-runs page, modal) renders them identically.
export const DUTY_KEYS: Array<keyof Omit<PilotDuties, 'issue_digest'>> = [
  'pr_triage',
  'monitoring',
  'doc_sync',
  'backlog_hygiene',
]

export const DUTY_LABELS: Record<keyof PilotDuties, string> = {
  pr_triage: 'PR triage',
  monitoring: 'Log monitoring',
  doc_sync: 'Doc sync',
  issue_digest: 'Issue digest',
  backlog_hygiene: 'Backlog hygiene',
}

export function dutyStatusColour(status: string): string {
  switch (status) {
    case 'action_taken':
      return 'bg-blue-100 text-blue-700 border-blue-200 dark:bg-blue-950/40 dark:text-blue-300 dark:border-blue-900'
    case 'flagged':
      return 'bg-amber-100 text-amber-800 border-amber-200 dark:bg-amber-950/40 dark:text-amber-300 dark:border-amber-900'
    case 'error':
      return 'bg-red-100 text-red-700 border-red-200 dark:bg-red-950/40 dark:text-red-300 dark:border-red-900'
    case 'ok':
    default:
      return 'bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950/30 dark:text-emerald-400 dark:border-emerald-900'
  }
}

export function PilotRunDetailModal({ run, onClose }: { run: PilotRun; onClose: () => void }) {
  useEffect(() => {
    const handleEsc = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', handleEsc)
    return () => document.removeEventListener('keydown', handleEsc)
  }, [onClose])

  const duration = run.ended_at
    ? Math.round((new Date(run.ended_at).getTime() - new Date(run.started_at).getTime()) / 1000)
    : null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm"
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="relative w-full max-w-2xl max-h-[80vh] mx-4 bg-card border border-border rounded-xl shadow-2xl flex flex-col overflow-hidden">
        <div className="flex items-center justify-between px-5 py-4 border-b border-border shrink-0">
          <div className="flex items-center gap-2">
            <span
              className={cn(
                'inline-block w-2.5 h-2.5 rounded-full shrink-0',
                run.status === 'success' ? 'bg-emerald-500' : run.status === 'failed' ? 'bg-red-500' : 'bg-amber-500 animate-pulse',
              )}
            />
            <h2 className="text-sm font-semibold text-foreground">Pilot Run Detail</h2>
            <span className="text-xs text-muted-foreground tabular-nums">
              {new Date(run.started_at).toLocaleString()}
            </span>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-muted-foreground hover:text-foreground transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
          <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
            {duration !== null && (
              <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                Duration: {duration}s
              </span>
            )}
            {run.usage && (
              <>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Cost: ${run.usage.total_cost_usd.toFixed(3)}
                </span>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Turns: {run.usage.num_turns}
                </span>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Input: {(run.usage.input_tokens / 1000).toFixed(1)}k tokens
                </span>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Output: {(run.usage.output_tokens / 1000).toFixed(1)}k tokens
                </span>
              </>
            )}
          </div>

          {run.result && (
            <div>
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Verdict</h3>
              <div className="text-sm text-foreground bg-secondary/40 rounded-lg px-4 py-3 whitespace-pre-wrap">
                {run.result.replace(/^PILOT-RESULT:\s*/, '')}
              </div>
            </div>
          )}

          {run.duties && (
            <div>
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Duties</h3>
              <div className="space-y-2">
                {DUTY_KEYS.map(key => {
                  const duty = run.duties![key]
                  if (!duty) return null
                  return (
                    <div key={key} className="flex items-start gap-3 px-3 py-2 bg-secondary/30 rounded-lg">
                      <span
                        className={cn(
                          'inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium border shrink-0 tabular-nums',
                          dutyStatusColour(duty.status),
                        )}
                      >
                        {duty.status}
                      </span>
                      <div className="flex-1 min-w-0">
                        <div className="text-xs font-semibold text-foreground">{DUTY_LABELS[key]}</div>
                        {duty.actions && duty.actions.length > 0 && (
                          <ul className="mt-1 space-y-0.5 text-xs text-foreground/80">
                            {duty.actions.map((a, i) => (
                              <li key={i} className="flex gap-1.5">
                                <span className="text-muted-foreground">·</span>
                                <span className="font-mono">{a}</span>
                              </li>
                            ))}
                          </ul>
                        )}
                        {duty.note && (
                          <div className="mt-1 text-xs text-muted-foreground italic">{duty.note}</div>
                        )}
                      </div>
                    </div>
                  )
                })}

                {run.duties.issue_digest && (
                  <div className="px-3 py-2.5 bg-secondary/30 rounded-lg">
                    <div className="flex items-center gap-2 mb-1.5">
                      <span className="text-xs font-semibold text-foreground">{DUTY_LABELS.issue_digest}</span>
                      {run.duties.issue_digest.since_hours ? (
                        <span className="text-[10px] text-muted-foreground tabular-nums">since {run.duties.issue_digest.since_hours}h</span>
                      ) : null}
                    </div>
                    <div className="flex items-center gap-2 text-[11px] mb-1.5 flex-wrap">
                      <span className="px-1.5 py-0.5 rounded bg-background border border-border tabular-nums">
                        +{run.duties.issue_digest.new ?? 0} new
                      </span>
                      <span className="px-1.5 py-0.5 rounded bg-background border border-border tabular-nums">
                        −{run.duties.issue_digest.closed ?? 0} closed
                      </span>
                      {(run.duties.issue_digest.labeled ?? 0) > 0 && (
                        <span className="px-1.5 py-0.5 rounded bg-background border border-border tabular-nums">
                          {run.duties.issue_digest.labeled} labeled
                        </span>
                      )}
                      {(run.duties.issue_digest.commented ?? 0) > 0 && (
                        <span className="px-1.5 py-0.5 rounded bg-background border border-border tabular-nums">
                          {run.duties.issue_digest.commented} commented
                        </span>
                      )}
                    </div>
                    {run.duties.issue_digest.summary && (
                      <div className="text-xs text-foreground/85 whitespace-pre-wrap leading-relaxed">
                        {run.duties.issue_digest.summary}
                      </div>
                    )}
                  </div>
                )}
              </div>
            </div>
          )}

          {run.summary && (
            <div>
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">
                {run.duties ? 'Details' : 'Summary'}
              </h3>
              <div className="text-sm text-foreground bg-secondary/40 rounded-lg px-4 py-3 whitespace-pre-wrap">
                {run.summary}
              </div>
            </div>
          )}

          {run.error && (
            <div>
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Error</h3>
              <div className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-4 py-3 font-mono whitespace-pre-wrap dark:bg-red-950/30 dark:border-red-900">
                {run.error}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
