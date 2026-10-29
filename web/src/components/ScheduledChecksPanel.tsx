import { useEffect, useState } from 'react'

type Check = {
  id: string; cron: string; timezone: string; prompt: string; owner: string
  enabled: boolean; failures: number; last_status: string; last_summary: string
  last_run_at: string | null; ops: number; timeout_seconds: number
}
type Run = {
  id: string; check_id: string; started_at: string; status: string; summary: string
  actions: unknown; usage: unknown
}

export function ScheduledChecksPanel() {
  const [data, setData] = useState<{ checks: Check[]; runs: Run[] }>({ checks: [], runs: [] })
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    const load = async () => {
      try {
        const r = await fetch('/api/pilot/schedules', { cache: 'no-store' })
        if (!r.ok) throw new Error('Scheduled checks unavailable')
        const next = await r.json()
        if (active) { setData(next); setError('') }
      } catch (e) { if (active) setError(e instanceof Error ? e.message : String(e)) }
    }
    void load()
    const timer = window.setInterval(() => void load(), 30000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])
  return <section className="my-4 rounded-lg border border-border p-4" aria-label="Scheduled checks">
    <h2 className="font-semibold">Scheduled checks</h2>
    {error ? <p role="alert">{error}</p> : data.checks.length === 0 ? <p className="text-sm text-muted-foreground">No scheduled checks.</p> : data.checks.map(check =>
      <details key={check.id} className="mt-3 text-sm">
        <summary>{check.id} · {check.enabled ? 'Enabled' : 'Disabled'} · {check.last_status || 'Never run'}
          {check.failures >= 3 && <strong className="text-red-600"> · Disabled after three failures</strong>}
        </summary>
        <p className="mt-2 whitespace-pre-wrap">{check.prompt}</p>
        <p>{check.cron} · {check.timezone} · owner: {check.owner} · ops: {check.ops} · timeout: {check.timeout_seconds}s</p>
        <p>Last run: {check.last_run_at ? new Date(check.last_run_at).toLocaleString() : 'Never'} · {check.last_summary}</p>
        {data.runs.filter(run => run.check_id === check.id).map(run => <details key={run.id} className="mt-2 pl-3">
          <summary>{new Date(run.started_at).toLocaleString()} · {run.status} · {run.summary}</summary>
          <pre className="overflow-auto whitespace-pre-wrap">{JSON.stringify({ actions: run.actions, usage: run.usage ?? 'unavailable' }, null, 2)}</pre>
        </details>)}
      </details>)}
  </section>
}
