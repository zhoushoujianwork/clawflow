import { useEffect, useState } from 'react'
import { useReadOnly } from '../lib/session'

export type Check = {
  id: string; cron: string; timezone: string; owner: string
  enabled: boolean; failures: number; last_status: string; last_summary: string
  last_run_at: string | null; ops: number; timeout_seconds: number
}
export type CheckRun = {
  id: string; check_id: string; started_at: string; ended_at?: string; status: string; summary: string
  usage?: { cost_basis?: string; cost_unavailable?: boolean; total_cost_usd?: number; cost_is_estimate?: boolean; tokens_unavailable?: boolean; input_tokens?: number; output_tokens?: number; cache_read_input_tokens?: number; cache_creation_input_tokens?: number } | null
}
export type ChecksData = { checks: Check[]; runs: CheckRun[] }
export const resultAnchor = (id: string) => `schedule-${encodeURIComponent(id)}`
export const resultLink = (id: string) => `/pipeline#${resultAnchor(id)}`
export function latestCheckRun(data: ChecksData, id: string) {
  return data.runs.filter(r => r.check_id === id).sort((a, b) => (Date.parse(b.started_at) || 0) - (Date.parse(a.started_at) || 0) || b.id.localeCompare(a.id))[0]
}
// LastStatus may describe an overlapping skip rather than the last completed run.
export function checkNeedsAttention(check: Check) {
  return check.last_status === 'failed' || (!check.enabled && check.failures >= 3) ||
    check.failures > 0
}
export function useScheduledChecks() {
  const [data, setData] = useState<ChecksData>({ checks: [], runs: [] })
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    const load = async () => {
      try {
        const r = await fetch('/api/pilot/schedules', { cache: 'no-store' })
        if (!r.ok) throw new Error('Scheduled observations unavailable')
        const next: ChecksData = await r.json()
        if (active) { setData(next); setError('') }
      } catch { if (active) setError('Scheduled observations unavailable; previous results may be stale.') }
    }
    void load()
    const timer = window.setInterval(() => void load(), 30000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])
  return { data, error }
}

export function ScheduledChecksPanel({ data, error = '', administration = false }: { data: ChecksData; error?: string; administration?: boolean }) {
  const readonly = useReadOnly()
  useEffect(() => {
    const reveal = () => {
      const target = document.getElementById(window.location.hash.slice(1))
      if (target instanceof HTMLDetailsElement && target.id.startsWith('schedule-')) target.open = true
    }
    reveal()
    window.addEventListener('hashchange', reveal)
    return () => window.removeEventListener('hashchange', reveal)
  }, [data])
  if (!data.checks.length) return error ? <p role="status" className="text-xs text-muted-foreground">{error}</p> : null
  const checks = [...new Map(data.checks.map(check => [check.id, check])).values()]
  return <section id="scheduled-checks" className="my-4 rounded-lg border border-border p-4 min-w-0 [overflow-wrap:anywhere]" aria-label="Scheduled observations">
    <h2 className="font-semibold">{administration ? 'Steward · Scheduled checks' : 'Scheduled observations'}</h2>
    <p className="text-xs text-muted-foreground">{checks.length} checks · {checks.filter(c => c.enabled).length} enabled · Observations, excluded from delivered issues.</p>
    {error && <p role="status">{error}</p>}
    {!readonly && !administration && <a className="text-xs underline" href="/settings#scheduled-checks">Manage with Steward / administration</a>}
    {administration && <p className="text-sm my-2">Ask Steward to create, update or remove a check. CLI: clawflow pilot schedule add / list / show / enable / disable / remove. Changes use existing authorization and policy gates.</p>}
    <ul>{checks.map(check => {
      const run = latestCheckRun(data, check.id)
      // Old evidence is dated explicitly; cadence alone cannot prove a missed run.
      const stale = !!run && !!check.last_run_at && Date.parse(run.started_at) < Date.parse(check.last_run_at)
      const usage = run?.usage
      const tokensKnown = usage?.tokens_unavailable === false &&
        [usage.input_tokens, usage.output_tokens, usage.cache_read_input_tokens, usage.cache_creation_input_tokens].every(v => typeof v === 'number' && Number.isFinite(v) && v >= 0)
      const tokens = tokensKnown ? `${usage!.input_tokens} input + ${usage!.cache_read_input_tokens} cached + ${usage!.cache_creation_input_tokens} cache write / ${usage!.output_tokens} output` : 'unknown'
      const costKnown = usage?.cost_unavailable === false && typeof usage.total_cost_usd === 'number' &&
        Number.isFinite(usage.total_cost_usd) && usage.total_cost_usd >= 0 && !!usage.cost_basis
      const cost = costKnown ? `${usage!.total_cost_usd} USD${usage!.cost_is_estimate ? ' (estimate)' : ''}` : 'unknown'
      return <li key={check.id} className="mt-2 text-sm">
        <span>{check.id} · {check.enabled ? 'Enabled' : 'Disabled'} · {check.last_status || 'Never run'}</span>
        {!check.enabled && check.failures >= 3 && <span> · Disabled after three failures</span>}
        {administration && <p>{check.cron} · {check.timezone} · owner: {check.owner} · ops: {check.ops} · timeout: {check.timeout_seconds}s</p>}
        {run ? <details id={resultAnchor(check.id)} className="mt-1 scroll-mt-4">
          <summary><a className="underline" href={resultLink(check.id)} onClick={event => { const details = event.currentTarget.closest('details'); if (details) details.open = true }}>Latest result</a> · {run.started_at} · {run.status}{stale ? ' · Stale result: newer run not in available history' : ''}</summary>
          <p className="whitespace-pre-wrap">{run.summary || 'Summary unavailable'}</p>
          <p className="text-xs text-muted-foreground">Recorded usage source: {usage?.cost_basis || 'unknown'} · Profile/model: unknown · Tokens: {tokens} · Cost: {cost}</p>
        </details> : <p className="text-xs">Latest result unavailable · Last observation: {check.last_run_at || 'unknown'} · {check.last_summary || 'No recorded summary'}</p>}
      </li>
    })}</ul>
  </section>
}

export function ScheduledChecksAdministration() {
  const { data, error } = useScheduledChecks()
  return <ScheduledChecksPanel data={data} error={error} administration />
}
