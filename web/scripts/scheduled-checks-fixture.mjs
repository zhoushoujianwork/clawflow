export function scheduledChecksFixture(kind) {
  if (kind === 'none') return { checks: [], runs: [] }
  const disabled = kind === 'disabled'
  const failed = disabled || kind === 'failed'
  const check = { id: 'morning-check', enabled: !disabled, failures: disabled ? 3 : failed ? 1 : 0, last_status: kind === 'overlap' ? 'skipped' : failed ? 'failed' : 'success', last_summary: '', last_run_at: '2026-10-30T06:00:00Z', cron: '* * * * *', timezone: 'UTC', owner: 'fixture', ops: 2, timeout_seconds: 300, prompt: 'PRIVATE_PROMPT_DO_NOT_RENDER' }
  const run = { id: 'result-1', check_id: check.id, started_at: kind === 'stale' ? '2026-10-29T06:00:00Z' : check.last_run_at, status: check.last_status, summary: kind === 'overlap' ? 'previous invocation still running' : failed ? 'Latest failure evidence <script>unsafe()</script>' : 'Observation complete', usage: kind === 'healthy' ? { cost_basis: 'offload_reported', cost_unavailable: false, cost_is_estimate: true, total_cost_usd: 0.01, tokens_unavailable: true } : null, actions: { secret: 'PRIVATE_ACTION_DO_NOT_RENDER' } }
  return { checks: [check], runs: [run] }
}
