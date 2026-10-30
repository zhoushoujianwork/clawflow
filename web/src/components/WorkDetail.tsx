import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { Item } from '../routes/_app.pipeline'
import { runDetailUrl } from '../lib/runDetailUrl'

export interface ExecutionAttempt {
  attempt_id?: string
  run_path?: string
  session_id?: string
  offload_run_id?: string
  caller_run_id?: string
  role?: string
  operator?: string
  profile?: string
  model?: string
  machine?: string
  status: string
  started_at?: string
  ended_at?: string
  elapsed_ms?: number | null
  exit_code?: number | null
  error_kind?: string
  provenance: { source: string; stale: boolean; observed_at?: string | null; generated_at?: string | null; source_machine?: string }
}
interface HistoryRun extends Partial<ExecutionAttempt> {
  repo: string
  issue_number: number
  path?: string
  execution_status?: string
  attribution?: { role?: string }
  availability?: { cost?: boolean; tokens?: boolean }
  usage?: { total_cost_usd?: number | null; input_tokens?: number | null; output_tokens?: number | null; cache_read_input_tokens?: number | null; cache_creation_input_tokens?: number | null; cost_is_estimate?: boolean; cost_basis?: string }
}
const known = (value: unknown) => value === undefined || value === null || value === '' ? 'unknown' : String(value)
const time = (value?: string | null) => value && Number.isFinite(Date.parse(value)) && !value.startsWith('0001-') ? value : 'unknown'
export const workDetailKey = (repo: string, issue: number) => JSON.stringify([repo, issue])
export function chronologicalAttempts(attempts: ExecutionAttempt[]) {
  return [...attempts].sort((a, b) => (time(a.started_at) === 'unknown' ? 0 : Date.parse(a.started_at!)) - (time(b.started_at) === 'unknown' ? 0 : Date.parse(b.started_at!)) || known(a.attempt_id).localeCompare(known(b.attempt_id)))
}

export function AttemptList({ attempts, history }: { attempts: ExecutionAttempt[]; history: HistoryRun[] }) {
  const ordered = chronologicalAttempts(attempts)
  const latest = [...ordered].reverse().find(a => time(a.started_at) !== 'unknown')
  return <section aria-label="Execution attempts" className="my-4">
    <h3 className="font-semibold">Execution attempts · oldest first; undated first</h3>
    {!ordered.length && <p>No execution evidence available.</p>}
    {!!ordered.length && !latest && <p>Latest attempt unknown: start timestamps unavailable.</p>}
    <ol className="space-y-3 mt-2">{ordered.map((a, index) => {
      // Never join by session: a resumed session can contain multiple attempts.
      const record = history.find(h => (h.attempt_id && h.attempt_id === a.attempt_id) || (runDetailUrl(h.path) && runDetailUrl(h.path) === runDetailUrl(a.run_path)))
      const url = runDetailUrl(a.run_path || record?.path)
      const u = record?.usage
      return <li key={a.attempt_id || index} data-attempt-id={a.attempt_id} className={`border rounded-lg p-3 ${a === latest ? 'border-brand bg-secondary' : ''}`}>
        <p className="font-semibold">{a === latest ? 'Latest attempt · ' : ''}{known(a.role || record?.attribution?.role)} · {known(a.operator)} · {a.status}</p>
        <p>Profile: {known(a.profile)} · model: {known(a.model)} · machine: {known(a.machine)}</p>
        <p>Attempt: {known(a.attempt_id)} · session: {known(a.session_id)} · Offload run: {known(a.offload_run_id)} · caller: {known(a.caller_run_id)}</p>
        <p>Started: {time(a.started_at)} · ended: {time(a.ended_at)}</p>
        <p>Exit: {known(a.exit_code)} · error: {known(a.error_kind)} · elapsed: {a.elapsed_ms == null ? 'unknown' : `${a.elapsed_ms} ms`}</p>
        <p>Tokens (input / output / cache read / cache creation): {record?.availability?.tokens ? [u?.input_tokens, u?.output_tokens, u?.cache_read_input_tokens, u?.cache_creation_input_tokens].map(known).join(' / ') : 'unknown'}</p>
        <p>Cost: {record?.availability?.cost && u?.total_cost_usd != null ? `$${u.total_cost_usd} · ${u.cost_is_estimate ? 'estimate' : 'reported'} · basis: ${known(u.cost_basis)}` : 'unknown'}. Usage source: persisted Offload metadata; per attempt, not summed across sessions.</p>
        <p>Source: {known(a.provenance.source)} · observed: {time(a.provenance.observed_at)} · generated: {time(a.provenance.generated_at)}{a.provenance.stale ? ' · stale evidence' : ''}</p>
        {url ? <a className="text-brand underline" href={url}>Open existing run details (loads transcript)</a> : <p>Run details unavailable.</p>}
      </li>
    })}</ol>
  </section>
}

export function WorkDetail({ repo, issue, item, children, onClose }: { repo: string; issue: number; item?: Item; children?: ReactNode; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const close = useRef<HTMLButtonElement>(null)
  const [history, setHistory] = useState<HistoryRun[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [page, setPage] = useState<string | undefined>()
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    dialog.current?.showModal()
    close.current?.focus()
    return () => {
      dialog.current?.close()
      if (previous?.isConnected && previous !== document.body) previous.focus()
      else document.querySelector<HTMLElement>('#work-page-title')?.focus()
    }
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    const query = new URLSearchParams({ repo, limit: '100' })
    if (page) query.set('cursor', page)
    void fetch(`/api/runs?${query}`, { cache: 'no-store', signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error(`History unavailable (HTTP ${response.status})`)
      const rows: HistoryRun[] = await response.json()
      if (controller.signal.aborted) return
      setHistory(current => [...current, ...rows.filter(r => r.repo === repo && r.issue_number === issue)])
      setCursor(response.headers.get('X-Next-Cursor'))
    }).catch(e => { if (!controller.signal.aborted) setError(String(e.message)) }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [repo, issue, page])
  const attempts: ExecutionAttempt[] = [...(item?.executions || [])]
  for (const h of history) {
    if (attempts.some(a => (h.attempt_id && h.attempt_id === a.attempt_id) || (runDetailUrl(h.path) && runDetailUrl(h.path) === runDetailUrl(a.run_path)))) continue
    attempts.push({ ...h, run_path: h.path, role: h.attribution?.role, status: h.execution_status || 'unknown', provenance: { source: h.execution_status ? 'offload_final' : 'unknown', stale: false } })
  }
  return <dialog ref={dialog} onCancel={e => { e.preventDefault(); onClose() }} aria-labelledby="work-detail-title" className="fixed inset-0 m-auto w-[calc(100%_-_2rem)] max-w-4xl max-h-[90dvh] overflow-y-auto rounded-xl border bg-card text-foreground p-4 backdrop:bg-black/40 [overflow-wrap:anywhere]">
    <div className="flex justify-between gap-3"><h2 id="work-detail-title" className="text-lg font-semibold">{item?.title || `Issue #${issue}`} · {repo} #{issue}</h2><button ref={close} onClick={onClose} className="border rounded px-3 py-1 h-fit">Close details</button></div>
    {item ? <section className="my-3"><h3 className="font-semibold">Overview · {item.stage}</h3><p>{item.why}</p><p>Source: ClawFlow pipeline · updated: {time(item.updated_at)}</p></section> : <p role="status">This work item is not in the current snapshot. Available recorded attempts remain below.</p>}
    {children}
    <AttemptList attempts={attempts} history={history} />
    <p className="text-xs">Read-only metadata inspection. Execution success does not establish CI, independent acceptance or delivery.</p>
    {loading && <p role="status">Loading bounded history…</p>}{error && <p role="alert">{error}</p>}
    {cursor && <button disabled={loading} onClick={() => setPage(cursor)} className="border rounded p-2">Load more repository history (100 records per page)</button>}
    <p className="text-xs">History scope: {repo}; only issue #{issue} is displayed. {loading ? 'Loading history.' : error ? 'History incomplete.' : cursor ? 'More records may be available.' : 'End of available history.'}</p>
  </dialog>
}
