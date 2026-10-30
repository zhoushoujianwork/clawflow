import { useEffect, useState } from 'react'
import type { Board, PipelineEvent, RepoBoard } from '../routes/_app.pipeline'
import type { SourceHealth } from './WorkSummary'
import { workDetailKey } from './WorkDetail'
import { runDetailUrl } from '../lib/runDetailUrl'

type ActivityEvent = PipelineEvent & { source?: string; href?: string }
type Filters = { events?: 'important' | 'all'; repo?: string; activityIssue?: string }
const dated = (value?: string | null): value is string => !!value && !value.startsWith('0001-') && Number.isFinite(Date.parse(value))
const repetitive = (e: PipelineEvent) => !['failed', 'error', 'merged'].includes(e.fields.status) && /(?:poll|skip|sweep|queue_wait|queue\/wait)/.test(e.event)
const identity = (e: ActivityEvent) => JSON.stringify([e.event, e.level, e.source, e.href, Object.entries(e.fields).sort(([a], [b]) => a.localeCompare(b))])

// Sort copies only. Session identity is deliberately never used to fold attempts.
export function foldEvents(events: ActivityEvent[]) {
  const folded: (ActivityEvent & { count: number; since: string })[] = []
  const sorted = [...events].sort((a, b) => (Date.parse(a.time) || 0) - (Date.parse(b.time) || 0))
  for (const e of sorted) {
    const prev = folded[folded.length - 1]
    if (prev && repetitive(e) && identity(prev) === identity(e)) { prev.count++; prev.time = e.time }
    else folded.push({ ...e, count: 1, since: e.time })
  }
  return folded
}

export function importantEvent(e: PipelineEvent) {
  if (e.level === 'warn' || e.level === 'error') return true
  if (repetitive(e)) return false
  return /execution\/|source\/|vcs\/|acceptance\/|delivery\/|harvest_end|automerge_merged|automerge_success|self_upgrade|lock_timeout|queue_(pick|wait|blocked)|queue\/(pick|wait|blocked|park|unpark)|ci_repair|run\/exit/.test(e.event) || ['failed', 'error', 'merged'].includes(e.fields.status) || Number(e.fields.merged) > 0
}

// Use only existing attributed metadata, never transcripts or arbitrary log fields.
export function projectActivity(repos: RepoBoard[]): ActivityEvent[] {
  const out: ActivityEvent[] = []
  for (const repo of repos) for (const item of repo.items) {
    const fields = { repo: repo.repo, issue: String(item.issue) }
    const work = '/pipeline?detail=' + encodeURIComponent(workDetailKey(repo.repo, item.issue))
    const add = (event: string, time: string | undefined, source: string, extra = {}, href = work) => {
      if (dated(time)) out.push({ event, time, source, level: 'info', fields: { ...fields, ...extra }, href })
    }
    for (const a of item.executions || []) {
      const extra = { attempt: a.attempt_id || 'unknown', status: a.status }
      const href = runDetailUrl(a.run_path) || work
      add('execution/started', a.started_at, a.provenance.source, extra, href)
      if (a.ended_at) add('execution/' + a.status, a.ended_at, a.provenance.source, extra, href)
      else if (a.status === 'stalled') add('execution/stalled (observed)', a.provenance.observed_at || undefined, a.provenance.source, extra, href)
    }
    if (item.pr) {
      add('vcs/PR ' + item.pr.state + ' (observed)', item.pr.updated_at, 'VCS snapshot', { pr: String(item.pr.number) })
      if (item.pr.ci) add('vcs/CI ' + item.pr.ci + ' (PR observation)', item.pr.updated_at, 'VCS snapshot', { pr: String(item.pr.number) })
    }
    if (item.acceptance?.verdict) add('acceptance/' + item.acceptance.verdict, item.acceptance.completed_at, 'ClawFlow acceptance')
    if (item.delivery) add('delivery/merged', item.delivery.merged_at, 'VCS delivery', { pr: String(item.delivery.pr) })
  }
  return out
}

export function projectLogActivity(events: PipelineEvent[], repos: RepoBoard[]): ActivityEvent[] {
  return events.map(e => {
    const item = !e.fields.issue && e.fields.pr ? repos.find(r => r.repo === e.fields.repo)?.items.find(i => String(i.pr?.number) === e.fields.pr) : undefined
    return { ...e, fields: { ...e.fields, ...(item ? { issue: String(item.issue) } : {}) } }
  })
}

export function sourceTransitions(previous: SourceHealth[] | undefined, current: SourceHealth[], observedAt: string): ActivityEvent[] {
  if (!dated(observedAt)) return []
  return current.flatMap(s => {
    const before = previous?.find(p => p.source === s.source && p.repo === s.repo)
    const down = s.stale || !!s.error_category
    const wasDown = before && (before.stale || !!before.error_category)
    if ((!before && !down) || (before && down === !!wasDown)) return []
    return [{time: observedAt, level: down ? 'warn' : 'info', event: down ? 'source/unavailable (observed)' : 'source/recovered (observed)', source: s.source, fields: {repo: s.repo || ''}}]
  })
}

export function ActivityTimeline({ board, ...props }: { board: Board; filter: string; repo: string; issue: string; update: (p: Filters) => void }) {
  const [health, setHealth] = useState<{ previous?: SourceHealth[]; events: ActivityEvent[] }>({events: []})
  useEffect(() => {
    setHealth(h => ({ previous: board.sources || [], events: [...h.events, ...sourceTransitions(h.previous, board.sources || [], new Date().toISOString())].slice(-100) }))
  }, [board.sources, board.generated_at])
  const events = [...projectLogActivity(board.events || [], board.repos), ...projectActivity(board.repos), ...health.events]
  return <EventTimeline {...props} events={events} repos={board.repos.map(r => r.repo)} />
}

export function EventTimeline({ events, filter, repo, issue = '', repos, update }: { events: ActivityEvent[]; filter: string; repo: string; issue?: string; repos: string[]; update: (p: Filters) => void }) {
  const [pageSize, setPageSize] = useState(50)
  useEffect(() => setPageSize(50), [filter, repo, issue])
  const rows = foldEvents(events).filter(e => (!repo || e.fields.repo === repo) && (!issue || e.fields.issue === issue) && (filter === 'all' || importantEvent(e))).reverse()
  return <details aria-label="Activity timeline" className="border rounded-xl p-3 mb-4 min-w-0 [overflow-wrap:anywhere]">
    <summary className="text-sm font-semibold cursor-pointer">Activity ({rows.length})</summary>
    <div className="flex flex-wrap gap-2 my-3 text-xs">
      <label>Show <select aria-label="Event importance" value={filter} onChange={e => update({ events: e.target.value as Filters['events'] })} className="bg-card border rounded p-1"><option value="important">Important</option><option value="all">All</option></select></label>
      <label>Repo <select aria-label="Event repository" className="max-w-full bg-card border rounded p-1" value={repo} onChange={e => update({ repo: e.target.value })}><option value="">All repositories</option>{repos.map(r => <option key={r}>{r}</option>)}</select></label>
      <label>Issue <input aria-label="Activity issue" inputMode="numeric" pattern="[0-9]*" value={issue} onChange={e => { if (/^\d{0,10}$/.test(e.target.value)) update({activityIssue: e.target.value || undefined}) }} className="bg-card border rounded p-1 w-24" placeholder="All issues" /></label>
    </div>
    <p className="text-xs text-muted-foreground mb-2">Newest first · showing {Math.min(pageSize, rows.length)} of {rows.length} matching groups. Log window: latest 50 events plus up to 50 retained important transitions from the last 4 MiB. Snapshot observations are not a complete transition history. Source changes: last 100 observed in this page session. Older attempts remain available in work details and Runs.</p>
    <ol className="divide-y max-h-96 overflow-auto text-xs">
      {rows.slice(0, pageSize).map((e, i) => {
        const href = e.href || runDetailUrl(e.fields.run_path) || (e.fields.repo && /^\d+$/.test(e.fields.issue) ? '/pipeline?detail=' + encodeURIComponent(workDetailKey(e.fields.repo, Number(e.fields.issue))) : undefined)
        return <li key={i} className="py-2 flex flex-wrap gap-2 break-words">
          <time dateTime={e.time}>{dated(e.time) ? e.time : 'Time unknown'}</time>
          <span>{e.event.replace(/^run\//, '')}{e.count > 1 ? ` ×${e.count} since ${e.since} — ${e.time}` : ''}</span>
          <span className="text-muted-foreground">Source: {e.source || 'ClawFlow run log'}{e.fields.repo ? ' · ' + e.fields.repo : ''}{e.fields.issue ? ' #' + e.fields.issue : ''}{e.fields.attempt ? ' · attempt ' + e.fields.attempt : ''}{e.fields.pr ? ' · PR #' + e.fields.pr : ''}{e.fields.status ? ' · ' + e.fields.status : ''}{e.fields.verdict ? ' · verdict ' + e.fields.verdict : ''}{['warn', 'error'].includes(e.level) ? ' · ' + e.level : ''}</span>
          {href && <a href={href} className="text-brand underline">{runDetailUrl(e.fields.run_path) || e.href?.startsWith('/runs/') ? 'Open attempt' : 'Open work item'}</a>}
        </li>
      })}
    </ol>
    {rows.length > pageSize && <button className="border rounded p-2 text-xs" onClick={() => setPageSize(n => n + 50)}>Load 50 more activity groups</button>}
    {!rows.length && <p className="text-xs text-muted-foreground">No matching activity.</p>}
    <a href="/runs" className="text-xs text-brand underline">Browse retained run history</a>
  </details>
}
