import { useEffect, useState } from 'react'

type ExecutionReadiness = {
  engine: string
  profile: string
  role_profiles?: Record<string, string>
  codex_sandbox?: string
  readiness: string
  message: string
}

export function ProvidersSection() {
  const [execution, setExecution] = useState<ExecutionReadiness | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    fetch('/api/providers', { cache: 'no-store' })
      .then(async r => { if (!r.ok) throw new Error('Could not read execution readiness'); return r.json() as Promise<ExecutionReadiness> })
      .then(value => { if (active) setExecution(value) })
      .catch(err => { if (active) setError(String(err)) })
    return () => { active = false }
  }, [])
  return <section className="rounded-xl border border-border bg-card p-5 space-y-3">
    <h2 className="font-semibold">Offload execution</h2>
    <p className="text-sm text-muted-foreground">Configure credentials and models with <code>offload config</code>. Select the machine’s profile in <code>settings.execution.profile</code> or <code>CLAWFLOW_OFFLOAD_PROFILE</code>.</p>
    {error && <p className="text-sm text-destructive">{error}</p>}
    {execution ? <div className="text-sm space-y-2">
      <p>Profile: <code>{execution.profile || 'Not configured'}</code></p>
      {Object.entries(execution.role_profiles || {}).map(([role, profile]) => <p key={role}>{role}: <code>{profile}</code></p>)}
      {execution.codex_sandbox && <p>Codex sandbox: <code>{execution.codex_sandbox}</code></p>}
      <p>{execution.readiness}: {execution.message}</p>
    </div> : !error && <p className="text-sm text-muted-foreground">Loading execution settings…</p>}
  </section>
}
