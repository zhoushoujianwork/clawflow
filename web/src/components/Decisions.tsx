import { useCallback, useEffect, useState } from 'react'

export type DecisionKind = 'needs_confirmation' | 'owner_skipped' | 'acceptance_blocked' | 'repair_stopped' | 'execution_config'

export interface Decision {
  id: string
  kind: DecisionKind
  repo?: string
  issue?: number
  pr?: number
  title: string
  reason: string
  action: string
  url?: string
  issue_url?: string
}

export interface DecisionGap { source: string; reason: string }

export interface DecisionReport {
  generated_at?: string
  decisions: Decision[]
  incomplete?: DecisionGap[]
}

export const DECISIONS_REFRESH_MS = 30_000

export const KIND_LABELS: Record<DecisionKind, string> = {
  needs_confirmation: 'Needs confirmation',
  owner_skipped: 'Skipped by owner',
  acceptance_blocked: 'Acceptance blocked',
  repair_stopped: 'Repair stopped',
  execution_config: 'Execution setup',
}

// Only http(s) links are rendered; anything else is shown as text.
export function safeHref(url?: string): string | undefined {
  return url && /^https?:\/\//i.test(url) ? url : undefined
}

function subject(d: Decision): string {
  if (d.repo && d.pr) return `${d.repo} PR #${d.pr}`
  if (d.repo && d.issue) return `${d.repo} #${d.issue}`
  return d.repo || ''
}

// Pure view: the fetch state is owned by useDecisions so the markup is testable.
export function DecisionsView({ report, error }: { report: DecisionReport | null; error?: string | null }) {
  const gaps = report?.incomplete || []
  return <section className="mb-4 min-w-0" aria-label="Needs your decision">
    {error && <p role="alert" className="text-xs text-red-800 break-words mb-2">Could not refresh decisions: {error}{report ? ' · showing last known list' : ''}</p>}
    {!report ? (!error && <p className="text-sm text-muted-foreground">Loading decisions…</p>) : <>
      {report.decisions.length ? <>
        <h2 className="font-semibold text-sm mb-2">Needs your decision ({report.decisions.length})</h2>
        <ul className="border rounded-lg divide-y min-w-0">{report.decisions.map(d => <li key={d.id} data-decision-id={d.id} data-decision-kind={d.kind} className="p-3 text-xs min-w-0 [overflow-wrap:anywhere]">
          <div className="flex flex-wrap gap-2"><strong>{d.title}</strong><span>{KIND_LABELS[d.kind] || d.kind}</span>{subject(d) && <span className="text-muted-foreground">{subject(d)}</span>}</div>
          <p className="mt-1">{d.reason}</p>
          <p className="mt-1"><span className="text-muted-foreground">What to do: </span>{d.action}</p>
          <div className="flex flex-wrap gap-3 mt-2">
            {safeHref(d.url) && <a className="text-brand underline" href={safeHref(d.url)} target="_blank" rel="noreferrer">{d.pr ? 'View PR' : d.issue ? 'View issue' : 'Open guide'}</a>}
            {safeHref(d.issue_url) && d.issue_url !== d.url && <a className="text-brand underline" href={safeHref(d.issue_url)} target="_blank" rel="noreferrer">View issue</a>}
          </div>
        </li>)}</ul>
      </> : <p className="text-sm text-muted-foreground">Nothing needs your decision</p>}
      {gaps.length > 0 && <div className="text-xs text-muted-foreground mt-2">
        <p>Not fully read, so these may hide items:</p>
        <ul className="list-disc pl-4">{gaps.map(g => <li key={g.source}>{g.source}: {g.reason}</li>)}</ul>
      </div>}
    </>}
  </section>
}

export function useDecisions() {
  const [report, setReport] = useState<DecisionReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const r = await fetch('/api/decisions', { cache: 'no-store', signal })
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.error || `HTTP ${r.status}`)
      }
      const next = await r.json() as DecisionReport
      if (!next || !Array.isArray(next.decisions)) throw new Error('Invalid decisions response')
      setReport(next)
      setError(null)
    } catch (e) {
      if (signal?.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    const id = window.setInterval(() => void load(controller.signal), DECISIONS_REFRESH_MS)
    return () => { controller.abort(); window.clearInterval(id) }
  }, [load])
  return { report, error }
}

export function Decisions() {
  const { report, error } = useDecisions()
  return <DecisionsView report={report} error={error} />
}
