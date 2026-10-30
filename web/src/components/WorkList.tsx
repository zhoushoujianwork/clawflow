import { useState, type ReactNode } from 'react'
import { selectWorkItems, type WorkItem, type WorkFilter } from './WorkSummary'
import type { WorkSearch } from '../lib/workSearch'
import { findProjectsForRepo, type ProjectEntry } from '../lib/projectMembership'
import { issueUrl, type RepoInfoMap } from '../lib/vcsUrls'

export interface ListItem extends WorkItem {
  issue: number
  title: string
  why: string
  updated_at?: string
  queue_position?: number
  executions?: (NonNullable<WorkItem['executions']>[number] & {
    attempt_id?: string
    role?: string
    operator?: string
    started_at?: string
    elapsed_ms?: number | null
    last_progress_at?: string | null
  })[]
}
export interface ListRepo<T extends ListItem> {
  repo: string
  enabled: boolean
  items: T[]
}
export interface QueueEntry {
  repo: string
  number: number
  position: number
  tier?: string
  state: string
}
export const WORK_PAGE_SIZE = 50
export type ListFilter = WorkFilter | 'active'
const stageLabels: Record<string, string> = {
  queued: 'Queued', implementing: 'Implementing', pr_open: 'PR open', ci_pending: 'CI pending',
  ci_failed: 'CI failed', acceptance_pending: 'Acceptance pending', acceptance_running: 'Acceptance running',
  accepted: 'Accepted', rejected: 'Acceptance failed', infra_retry: 'Infrastructure retry',
  merged: 'Merged', failed: 'Failed', blocked: 'Blocked',
}

// Missing timestamps sort last; the issue key breaks ties independently of poll order.
const observedTime = (value?: string) => value && !value.startsWith('0001-') && Number.isFinite(Date.parse(value)) ? Date.parse(value) : undefined
export function latestWorkAttempt(item: ListItem) {
  return [...(item.executions || [])].sort((a, b) =>
    (observedTime(b.started_at) ?? -Infinity) - (observedTime(a.started_at) ?? -Infinity) || JSON.stringify([a.attempt_id, a.role, a.run_path]).localeCompare(JSON.stringify([b.attempt_id, b.role, b.run_path])))[0]
}
export const workRole = (item: ListItem): string => latestWorkAttempt(item)?.role || 'unknown'
// Queue position is supplied by ClawFlow; inspection sorts never mutate it.
// Deduplicate issues before filtering; attempts never become top-level work rows.
export function workRows<T extends ListItem>(repos: ListRepo<T>[], queue: QueueEntry[], filter: ListFilter, query: WorkSearch = {}, projects: ProjectEntry[] = []) {
  const positions = new Map(queue.map(q => [JSON.stringify([q.repo, q.number]), q]))
  const seen = new Set<string>()
  const rows = repos.flatMap(repo => repo.items.flatMap(item => {
    const key = JSON.stringify([repo.repo, item.issue])
    if (seen.has(key)) return []
    seen.add(key)
    return [{ key, repo: repo.repo, enabled: repo.enabled, item, queue: positions.get(key) }]
  }))
  const selected = new Set(selectWorkItems(rows.map(r => r.item), filter))
  const q = query.q?.trim().toLowerCase() || ''
  return rows.filter(row => selected.has(row.item)
    && (!query.workRepo || row.repo === query.workRepo)
    && (!query.project || findProjectsForRepo(row.repo, projects).includes(query.project))
    && (!query.stage || row.item.stage === query.stage)
    && (!query.role || workRole(row.item) === query.role)
    && (!q || (row.item.title || '').toLowerCase().includes(q) || String(row.item.issue) === q.replace(/^#/, '') || `${row.repo}#${row.item.issue}`.toLowerCase() === q)
  ).sort((a, b) => {
    if (query.sort) {
      const at = observedTime(a.item.updated_at), bt = observedTime(b.item.updated_at)
      if (at === undefined && bt !== undefined) return 1
      if (bt === undefined && at !== undefined) return -1
      if (at !== undefined && bt !== undefined && at !== bt) return query.sort === 'age' ? at - bt : bt - at
    } else {
      const rank = (a.queue?.position ?? a.item.queue_position ?? Infinity) - (b.queue?.position ?? b.item.queue_position ?? Infinity)
      if (rank) return rank
    }
    return a.key.localeCompare(b.key)
  })
}

export function workReason(item: ListItem, enabled: boolean, queue?: QueueEntry) {
  // Explain explicit scheduler verdicts; do not estimate capacity from visible rows.
  if (item.stage === 'merged') return item.why || 'Pull request merged'
  const reasons: string[] = []
  if (!enabled) reasons.push('Automation disabled for this repository')
  if (item.stage === 'ci_pending') reasons.push('Waiting for CI on the current revision')
  if (item.stage === 'acceptance_pending') reasons.push('Waiting for independent acceptance')
  if (queue?.state === 'waiting:dependency') reasons.push('Waiting for dependencies to finish')
  if (queue?.state === 'blocked:dependency_cycle') reasons.push('Dependency cycle blocks this issue')
  if (queue?.state === 'eligible:max_in_flight') reasons.push('Waiting for repository capacity')
  if (item.why) reasons.push(item.why)
  return reasons.join(' · ') || 'Reason unavailable'
}

function elapsed(ms?: number | null) {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return 'unknown'
  const seconds = Math.floor(ms / 1000)
  return seconds < 60 ? `${seconds}s` : seconds < 3600 ? `${Math.floor(seconds / 60)}min` : `${Math.floor(seconds / 3600)}h`
}
function timestamp(value?: string | null) {
  return value && Number.isFinite(Date.parse(value)) ? value : 'unknown'
}

export function WorkList<T extends ListItem>({ repos, queue, filter = 'active', repoInfo, renderDetails, onOpen, query = {}, projects = [] }: {
  query?: WorkSearch
  projects?: ProjectEntry[]
  repos: ListRepo<T>[]
  queue: QueueEntry[]
  filter?: ListFilter
  repoInfo: RepoInfoMap
  onOpen?: (item: T, repo: string) => void
  renderDetails?: (item: T, repo: string) => ReactNode
}) {
  const [grouped, setGrouped] = useState(false)
  const view = JSON.stringify([filter, query.q, query.workRepo, query.project, query.stage, query.role, query.sort])
  const [paging, setPaging] = useState({ view, page: 0 })
  const rows = workRows(repos, queue, filter, query, projects)
  const lastPage = Math.max(0, Math.ceil(rows.length / WORK_PAGE_SIZE) - 1)
  const page = paging.view === view ? Math.min(paging.page, lastPage) : 0
  const visible = rows.slice(page * WORK_PAGE_SIZE, (page + 1) * WORK_PAGE_SIZE)
  const groups = grouped ? [...new Set(visible.map(row => row.repo))].map(repo => ({
    name: repo, rows: visible.filter(row => row.repo === repo),
  })) : [{ name: '', rows: visible }]
  return <section aria-label="Work list" className="min-w-0 mb-4">
    <div className="flex flex-wrap justify-between gap-3 mb-3 text-sm">
      <p role="status">{rows.length ? `${page * WORK_PAGE_SIZE + 1}–${Math.min((page + 1) * WORK_PAGE_SIZE, rows.length)} of ${rows.length} items` : 'No items in this view. 0 results.'}</p>
      <label className="flex items-center gap-2"><input type="checkbox" checked={grouped} onChange={e => setGrouped(e.target.checked)} />Group by repository</label>
    </div>
    <p className="text-xs text-muted-foreground mb-3">Queue positions and priority come from ClawFlow. Inspection order never changes dispatch rank. Missing update times sort last; age measures time since last update. Role uses the latest recorded attempt. Up to {WORK_PAGE_SIZE} items per page{grouped ? '; grouping applies to this page' : ''}.</p>
    {groups.map(group => <div key={group.name} className="min-w-0">
      {group.name && <h2 className="font-semibold text-sm break-all my-3">{group.name}</h2>}
      <ul className="divide-y border rounded-xl min-w-0">
        {group.rows.map(row => {
          const { item } = row
          const attempts = item.executions || []
          const latest = latestWorkAttempt(item)
          return <li key={row.key} data-work-key={row.key} className="p-4 min-w-0 [overflow-wrap:anywhere]">
            <div className="flex flex-wrap items-start gap-2">
              <a className="text-base font-semibold text-brand flex-1 min-w-0 basis-56" href={issueUrl(row.repo, item.issue, repoInfo)} target="_blank" rel="noreferrer">{item.title || `Issue #${item.issue}`}</a>
              <span data-stage={item.stage} className="text-xs border rounded-full px-2 py-1">{stageLabels[item.stage] || item.stage}</span>
            </div>
            <p data-work-reason className="text-sm mt-2 whitespace-normal">{workReason(item, row.enabled, row.queue)}</p>
            <div className="text-xs text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 mt-3">
              <span>{row.repo} #{item.issue}</span>
              {row.queue && <span>Queue #{row.queue.position}{row.queue.tier ? ` · ${row.queue.tier}` : ''}</span>}
              <span>Role: {workRole(item)}</span>
              <span>Elapsed: {elapsed(latest?.elapsed_ms)}</span>
              <span>Last progress: {timestamp(latest?.last_progress_at)}</span>
              <span>Updated: {timestamp(item.updated_at)}</span>
            </div>
            {onOpen ? <button className="mt-3 text-xs border rounded px-3 py-2" onClick={() => onOpen(item, row.repo)}>Details · {attempts.length} execution attempts</button> : <details className="mt-3 text-xs">
              <summary className="cursor-pointer">Details · {attempts.length} execution attempts</summary>
              <ul className="space-y-2 my-3" aria-label="Execution attempts">
                {attempts.map((attempt, i) => <li key={attempt.attempt_id || i} className="break-words">
                  {attempt.role || 'unknown role'} · {attempt.operator || 'unknown operator'} · {attempt.status} · elapsed {elapsed(attempt.elapsed_ms)} · last progress {timestamp(attempt.last_progress_at)}
                  {attempt.provenance.stale ? ' · stale evidence' : ''}
                </li>)}
              </ul>
              {renderDetails?.(item, row.repo)}
            </details>}
          </li>
        })}
      </ul>
    </div>)}
    {rows.length > WORK_PAGE_SIZE && <nav aria-label="Work list pages" className="flex flex-wrap items-center gap-3 mt-3 text-sm">
      <button className="border rounded px-3 py-1 disabled:opacity-50" disabled={page === 0} onClick={() => setPaging({ view, page: page - 1 })}>Previous</button>
      <span>Page {page + 1} of {lastPage + 1}</span>
      <button className="border rounded px-3 py-1 disabled:opacity-50" disabled={page === lastPage} onClick={() => setPaging({ view, page: page + 1 })}>Next</button>
    </nav>}
  </section>
}
