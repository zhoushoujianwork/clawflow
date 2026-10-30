import { useCallback, useEffect, useState } from 'react'
import { useConfigChanged } from '../lib/configEvents'
import type { Board } from '../routes/_app.pipeline'

type ExecutionReadiness = {
  profile: string
  role_profiles?: Record<string, string>
  pilot_profile?: string
  codex_sandbox?: string
  readiness: string
  message: string
}

type ObservedExecution = Board['runtime']['execution']

// Only project supported metadata; never render the full API response.
export function ExecutionView({ execution, observed, error, observedError }: {
  execution: ExecutionReadiness | null
  observed: ObservedExecution | null
  error: string
  observedError: string
}) {
  return <section aria-label="Execution" className="rounded-xl border border-border bg-card p-5 space-y-3 [overflow-wrap:anywhere]">
    <h2 className="font-semibold">Execution</h2>
    <p className="text-sm text-muted-foreground">Offload owns credentials and models. Configure them locally with <code>offload config</code>; select the machine’s profile in <code>settings.execution.profile</code> or <code>CLAWFLOW_OFFLOAD_PROFILE</code>.</p>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {execution ? <div className="text-sm space-y-2">
      <p>Configured profile (effective on this machine): <code>{execution.profile || 'Not configured'}</code></p>
      <p>Configuration readiness: {execution.readiness}. Live execution is unverified by this check.</p>
    </div> : !error && <p className="text-sm text-muted-foreground">Loading execution settings…</p>}
    <div className="text-sm space-y-2">
      <p>Last observed run profile: <code>{observed?.profile || 'Not recorded / unavailable'}</code></p>
      <p className="text-xs text-muted-foreground">From one latest recorded execution, independent of today’s configuration. This does not verify CI, acceptance or delivery.</p>
      {observedError && <p role="alert" className="text-destructive">{observedError}</p>}
    </div>
    <details className="text-xs space-y-2">
      <summary className="cursor-pointer">Advanced execution diagnostics</summary>
      {execution && <div className="space-y-2 mt-2">
        <p>{execution.message}</p>
        {Object.entries(execution.role_profiles || {}).map(([role, profile]) => <p key={role}>Configured {role} profile: <code>{profile}</code></p>)}
        {execution.pilot_profile && <p>Effective Pilot profile: <code>{execution.pilot_profile}</code></p>}
        {execution.codex_sandbox && <p>Codex sandbox: <code>{execution.codex_sandbox}</code></p>}
      </div>}
      {observed && <p>Recorded backend: {observed.backend || 'Not recorded'} · model: {observed.model || 'Not recorded'} · tier: {observed.service_tier || 'Not recorded'}</p>}
    </details>
  </section>
}

export function ExecutionSection() {
  const [execution, setExecution] = useState<ExecutionReadiness | null>(null)
  const [observed, setObserved] = useState<ObservedExecution | null>(null)
  const [error, setError] = useState('')
  const [observedError, setObservedError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const reload = useCallback(() => setRefresh(n => n + 1), [])
  useConfigChanged(reload)
  useEffect(() => {
    const controller = new AbortController()
    setExecution(null); setObserved(null); setError(''); setObservedError('')
    const read = async (url: string) => {
      const r = await fetch(url, { cache: 'no-store', signal: controller.signal })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      return r.json()
    }
    // A history outage must not hide current configuration (or vice versa).
    void read('/api/providers')
      .then(value => { if (!controller.signal.aborted) setExecution(value) })
      .catch(() => { if (!controller.signal.aborted) setError('Could not read execution readiness.') })
    void read('/api/pipeline')
      .then(value => { if (!controller.signal.aborted) setObserved(value.runtime?.execution ?? null) })
      .catch(() => { if (!controller.signal.aborted) setObservedError('Recorded execution metadata is unavailable.') })
    return () => controller.abort()
  }, [refresh])
  return <div className="space-y-2">
    <ExecutionView execution={execution} observed={observed} error={error} observedError={observedError} />
    <button type="button" onClick={reload} className="text-xs px-2 py-1 rounded border border-border hover:bg-secondary/50">Refresh execution status</button>
  </div>
}
