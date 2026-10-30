import type { Board, WorkerPool } from '../routes/_app.pipeline'

// Match the coordinator's 120-second request/lease window; never extend it on refresh.
const recent = (at: string | null | undefined, now: number, ttl: number) => {
  const time = Date.parse(at || '')
  return Number.isFinite(time) && time <= now + 5000 && now - time <= ttl
}
type Worker = WorkerPool['workers'][number]
const workerConnection = (w: Worker, now: number) => w.live && recent(w.last_seen, now, 120_000)
function heartbeat(w: Worker, now: number) {
  if (!w.last_heartbeat || !Number.isFinite(Date.parse(w.last_heartbeat)) || Date.parse(w.last_heartbeat) > now + 5000) return 'heartbeat unknown'
  return recent(w.last_heartbeat, now, 120_000) ? 'heartbeat recent' : 'heartbeat lost'
}

export function ExecutionCapacity({ board, failed = false, now = Date.now() }: { board: Board; failed?: boolean; now?: number }) {
  const pool = board.worker_pool
  const poolFresh = !failed && !board.worker_pool_error && !!pool && recent(pool.generated_at, now, 120_000)
  // Runtime is refreshed independently of delivery snapshots. A failed HTTP refresh
  // invalidates its current capacity claim; delivery staleness alone does not.
  const slots = board.runtime.scheduler
  const slotsKnown = !failed && Number.isInteger(slots.agent_slots_used) && slots.agent_slots_used >= 0 && Number.isInteger(slots.agent_slots_total) && slots.agent_slots_total > 0
  const connected = poolFresh ? pool!.workers.filter(w => workerConnection(w, now)).length : 'unknown'
  const queued = poolFresh ? pool!.tasks.filter(t => t.state === 'pending').length : 'unknown'
  const items = board.repos.flatMap(r => r.items)
  const localSources = board.sources?.filter(s => s.source === 'execution' && !s.repo) || []
  const localKnown = !failed && localSources.some(s => !s.stale && !s.error_category && recent(s.last_success, now, 30_000))
  // Count only managed work items attributed to this Offload source machine.
  // This is neither a slot count nor a list of remote worker jobs.
  const localItems = items.filter(i => i.state !== 'closed' && i.stage !== 'merged' && i.executions?.some(e =>
    e.status === 'running' && !e.provenance.stale && e.provenance.source === 'offload_live' &&
    !!e.machine && e.machine !== 'unknown' && e.machine === e.provenance.source_machine &&
    recent(e.provenance.generated_at, now, 30_000) && recent(e.provenance.observed_at, now, 30_000))).length
  return <details aria-label="Execution capacity" className="mb-4 text-xs border rounded-lg p-3">
    <summary className="cursor-pointer flex flex-wrap gap-x-4 gap-y-1">
      <span className="font-semibold">Capacity</span>
      <span>Local agent slots: {slotsKnown ? slots.agent_slots_used + ' / ' + slots.agent_slots_total : 'unknown'}{slotsKnown && slots.agent_slots_used >= slots.agent_slots_total ? ' · full' : ''}</span>
      <span>Coordinator queued tasks: {queued}</span>
      <span>Connected workers: {connected}</span>
    </summary>
    <div className="mt-3 space-y-2">
      <p>Local active work items (Offload): {localKnown ? localItems : 'unknown'}. Managed items only; local Offload does not enumerate remote jobs.</p>
      <p>Local agent slots are machine-wide occupied / configured slots. Queued tasks are coordinator tasks in pending state; connected workers are authenticated requests within 120 seconds, not agent slots. Remote slot capacity: unknown.</p>
      {(!poolFresh || failed) && <p>Worker source stale or unavailable; current worker and queue counts unknown.</p>}
      <WorkerDetails pool={pool} error={board.worker_pool_error} fresh={poolFresh} now={now} />
    </div>
  </details>
}

function observedTime(iso?: string): string {
  const time = Date.parse(iso || '')
  return Number.isFinite(time) ? iso! : 'unknown'
}

function WorkerDetails({ pool, error, fresh, now }: { pool?: WorkerPool | null; error?: string; fresh: boolean; now: number }) {
  return <section aria-label="Worker diagnostics" className="border rounded-xl p-3 mb-4 text-xs">
    {error ? <p role="status">{error}</p> : !pool ? <p className="text-muted-foreground">Worker source unavailable</p> : <>
      <p className="mb-3">Last hour: {pool.throughput.done} done · {pool.throughput.failed} failed · updated {observedTime(pool.generated_at)}</p>
      <p className="text-muted-foreground mb-2">Connection evidence comes from authenticated worker requests; heartbeat evidence comes from the last recorded heartbeat. A missing heartbeat is unknown, and a heartbeat older than 120 seconds is lost. Retained tables describe their snapshot, not current execution.</p>
      <div className="overflow-x-auto"><table className="w-full text-left">
        <caption className="text-left font-semibold">Workers ({pool.workers.length})</caption>
        <thead><tr>{['Name', 'Tags', 'Roles', 'Status', 'Last seen', 'Last heartbeat', 'Running tasks'].map(h => <th key={h} className="p-2">{h}</th>)}</tr></thead>
        <tbody>{pool.workers.map(w => <tr key={w.name} className="border-t">
          <td className="p-2">{w.name}</td><td className="p-2">{w.tags.join(', ') || '—'}</td><td className="p-2">{w.roles.join(', ') || '—'}</td>
          <td className="p-2">{fresh ? (workerConnection(w, now) ? 'connected' : 'not connected') : 'connection unknown (stale snapshot)'} · {fresh ? heartbeat(w, now) : 'heartbeat unknown (stale snapshot)'}</td><td className="p-2" title={w.last_seen || ''}>{observedTime(w.last_seen || undefined)}</td>
          <td className="p-2" title={w.last_heartbeat || ''}>{observedTime(w.last_heartbeat || undefined)}</td>
          <td className="p-2 break-all">{w.running_tasks.join(', ') || '—'}</td>
        </tr>)}</tbody>
      </table></div>
      {!pool.workers.length && <p className="py-2 text-muted-foreground">No registered workers.</p>}
      <div className="overflow-x-auto mt-3"><table className="w-full text-left">
        <caption className="text-left font-semibold">Tasks ({pool.tasks.length})</caption>
        <thead><tr>{['Task / repository', 'Kind', 'State / stage', 'Tags', 'Priority', 'Worker', 'Lease expiry', 'Retries'].map(h => <th key={h} className="p-2">{h}</th>)}</tr></thead>
        <tbody>{pool.tasks.map(t => <tr key={t.id} className="border-t">
          <td className="p-2 break-all" title={t.id}>{t.id.slice(0, 12)} · {t.repo} {t.pr ? `PR #${t.pr}` : `#${t.issue}`}</td>
          <td className="p-2">{t.kind}</td><td className="p-2">{t.state}{t.stage !== t.state ? ` / ${t.stage}` : ''}{t.waiting_reason && <p>{t.waiting_reason}</p>}{t.application_error && <p className="text-red-600">结果应用失败（{t.application_attempts}/3）：{t.application_error}</p>}</td>
          <td className="p-2">{t.tags.join(', ') || '—'}</td><td className="p-2">{t.priority}</td><td className="p-2">{t.worker || '—'}</td>
          <td className="p-2">{t.lease_expires_at ? new Date(t.lease_expires_at).toLocaleString() : '—'}</td><td className="p-2">{t.attempt}</td>
        </tr>)}</tbody>
      </table></div>
      {!pool.tasks.length && <p className="py-2 text-muted-foreground">No tasks.</p>}
    </>}
  </section>
}

