import { runDetailUrl } from '../lib/runDetailUrl'
import { useState } from 'react'
import type { Board, Item, PipelineEvent } from '../routes/_app.pipeline'
import { issueUrl, repoUrl, type RepoInfoMap } from '../lib/vcsUrls'

type Disposition = 'needs-human' | 'automatic-retry' | 'informational'
type Severity = 'error' | 'warning' | 'info'
interface Reason { text: string; disposition: Disposition; severity: Severity; time?: string }
export interface Incident {
  key: string
  repo: string
  issue?: number
  title: string
  reasons: Reason[]
  primary: Reason
  firstSeen?: string
  lastSeen?: string
  occurrences: number
  runUrl?: string
}
export const ATTENTION_LIMIT = 5
const active = (item: Item) => item.state !== 'closed' && item.stage !== 'merged'
const rank = (r: Reason) => (r.disposition === 'needs-human' ? 6 : r.disposition === 'automatic-retry' ? 3 : 0) + (r.severity === 'error' ? 2 : r.severity === 'warning' ? 1 : 0)
const validTime = (s?: string) => s && Number.isFinite(Date.parse(s)) ? s : undefined

// These are observations in the bounded snapshot/event window, not lifetime totals.
// Re-reading a snapshot never increments a counter or creates another incident.
export function attentionIncidents(board: Board): Incident[] {
  const incidents = new Map<string, Incident>()
  function add(key: string, repo: string, issue: number | undefined, title: string, reason: Reason, runUrl?: string) {
    let row = incidents.get(key)
    if (!row) {
      row = { key, repo, issue, title, reasons: [], primary: reason, occurrences: 0, runUrl }
      incidents.set(key, row)
    }
    row.runUrl ||= runUrl
    row.occurrences++
    if (!row.reasons.some(r => r.text === reason.text)) row.reasons.push(reason)
    if (rank(reason) > rank(row.primary)) row.primary = reason
    const time = validTime(reason.time)
    if (time && (!row.firstSeen || Date.parse(time) < Date.parse(row.firstSeen))) row.firstSeen = time
    if (time && (!row.lastSeen || Date.parse(time) > Date.parse(row.lastSeen))) row.lastSeen = time
  }
  const keyFor = (repo: string, issue: number) => JSON.stringify(['issue', repo, issue])
  for (const repo of board.repos) {
    for (const item of repo.items) {
      if (!active(item)) continue
      const reasons = new Set(item.attention || [])
      if (['ci_failed', 'infra_retry'].includes(item.stage)) reasons.add(item.why)
      if (item.pr?.ci === 'unknown' && item.pr.ci_error) reasons.add('CI status could not be read: ' + item.pr.ci_error)
      if (item.executions?.some(e => e.status === 'stalled' && e.provenance.source === 'offload_live' && !e.provenance.stale)) reasons.add('Execution has stalled')
      // A shared VCS read failure belongs to the repository source incident.
      if (repo.error && item.pr?.ci_error && repo.error.includes(item.pr.ci_error) && [...reasons].every(r => r.startsWith('CI status could not be read'))) continue
      const latestRun = item.executions?.filter(e => e.run_path).sort((a, b) => (Date.parse(b.started_at || '') || 0) - (Date.parse(a.started_at || '') || 0))[0]
      // Only the existing local run route is supported. Never link arbitrary paths.
      const runUrl = runDetailUrl(latestRun?.run_path)
      const exhausted = [...reasons].some(r => /repair exhausted|repair blocked|retries stopped|budget exhausted/.test(r))
      const retry = !exhausted && repo.enabled && item.stage === 'infra_retry' && !!validTime(item.acceptance?.next_retry_at) && !item.acceptance?.running
      const repairing = !exhausted && item.run?.live && item.run.operator === 'repair-ci'
      for (const text of reasons) {
        if (!text) continue
        const manual = text === 'waiting for review (manual merge)'
        // A CI read error is unknown evidence, not a failed check or permission to retry.
        const unknownCI = text.startsWith('CI status could not be read')
        const human = manual || exhausted || item.stage === 'failed' || item.stage === 'rejected' || item.stage === 'infra_retry' || text.includes('agent-failed') || text.startsWith('blocked:') || (item.stage === 'blocked' && !unknownCI && !['rate-limited', 'cost-limit'].includes(item.run?.status || ''))
        const disposition: Disposition = manual ? 'needs-human' : retry || repairing ? 'automatic-retry' : unknownCI || !human ? 'informational' : 'needs-human'
        add(keyFor(repo.repo, item.issue), repo.repo, item.issue, item.title, {
          text, disposition, severity: disposition === 'automatic-retry' || unknownCI || manual ? 'warning' : 'error',
          time: unknownCI || manual ? undefined : item.stage === 'infra_retry' || item.stage === 'rejected' ? item.acceptance?.completed_at : item.stage === 'failed' ? item.run?.ended_at : undefined,
        }, runUrl)
      }
    }
    if (repo.error) {
      const observations = (board.events || []).filter(e => e.fields.repo === repo.repo && !e.fields.issue && e.fields.error === repo.error)
      for (const observation of observations.length ? observations : [undefined]) add(JSON.stringify(['source', repo.repo]), repo.repo, undefined, 'Repository source unavailable', {
        text: (repo.stale ? 'Stale snapshot: ' : '') + repo.error, disposition: 'informational', severity: 'warning', time: observation?.time,
      })
    }
  }
  // Only unresolved lock timeouts survive. A later repo end or issue run clears them.
  const locks = new Map<string, PipelineEvent[]>()
  for (const e of [...(board.events || [])].sort((a, b) => Date.parse(a.time) - Date.parse(b.time))) {
    const repo = e.fields.repo
    if (!repo) continue
    const issue = Number(e.fields.issue) || 0
    const key = keyFor(repo, issue)
    if (e.event === 'run/end') {
      for (const [k, group] of locks) if (group[0].fields.repo === repo) locks.delete(k)
    } else if (e.event === 'run/lock_timeout') locks.set(key, [...(locks.get(key) || []), e])
  }
  for (const [key, events] of locks) {
    const last = events[events.length - 1]
    const repo = board.repos.find(r => r.repo === last.fields.repo)
    const issue = Number(last.fields.issue) || 0
    const item = repo?.items.find(i => i.issue === issue)
    if (!repo || (issue && (!item || !active(item))) || (item?.run?.started_at && Date.parse(item.run.started_at) > Date.parse(last.time))) continue
    for (const e of events) add(key, repo.repo, issue || undefined, item?.title || 'Repository lock timeout', {
      text: 'Lock wait timed out: ' + (e.fields.reason || ''), disposition: 'informational', severity: 'warning', time: e.time,
    })
  }
  // Pilot activity is evidence, not an automatic human-required verdict.
  for (const a of board.pilot_actions || []) {
    const repo = board.repos.find(r => r.repo === a.action.repo)
    if (!repo) continue
    const item = repo.items.find(i => a.action.issue ? i.issue === a.action.issue : a.action.pr ? i.pr?.number === a.action.pr : false)
    if ((a.action.issue || a.action.pr) && (!item || !active(item))) continue
    const key = item ? keyFor(repo.repo, item.issue) : JSON.stringify(['pilot', repo.repo, a.action.type])
    // Historical notes/merge attempts stay in the item timeline; they cannot
    // resurrect a resolved incident. An explicit human question is actionable.
    if (!incidents.has(key) && !(a.action.type === 'ask_human' && a.outcome === 'applied')) continue
    add(key, repo.repo, item?.issue, item?.title || 'Pilot activity', {
      text: `Pilot ${a.action.type} · ${a.outcome}${a.action.body ? ': ' + a.action.body : ''}${a.reason ? ' · ' + a.reason : ''}`,
      disposition: a.action.type === 'ask_human' && a.outcome === 'applied' ? 'needs-human' : 'informational', severity: 'info', time: a.at,
    })
  }
  return [...incidents.values()].sort((a, b) => rank(b.primary) - rank(a.primary) || a.key.localeCompare(b.key))
}

export function Attention({ board, repoInfo }: { board: Board; repoInfo: RepoInfoMap }) {
  const [limit, setLimit] = useState(ATTENTION_LIMIT)
  const rows = attentionIncidents(board)
  return <section className="mb-4 min-w-0" aria-label="Needs attention">
    {board.pilot_error && <p role="alert">{board.pilot_error}</p>}
    {rows.length ? <>
      <h2 className="font-semibold text-sm mb-2">Needs attention ({rows.length})</h2>
      <p className="text-xs text-muted-foreground mb-2">First/last seen and occurrences cover available symptom evidence only; unknown times stay unknown. Snapshot refreshes are not occurrences.</p>
      <ul className="border rounded-lg divide-y min-w-0">{rows.slice(0, limit).map(row => <li key={row.key} data-incident-key={row.key} className="p-3 text-xs min-w-0 [overflow-wrap:anywhere]">
        <div className="flex flex-wrap gap-2"><strong>{row.title || `Issue #${row.issue}`}</strong><span>{row.primary.severity}</span><span>{row.primary.disposition === 'needs-human' ? 'Needs human' : row.primary.disposition === 'automatic-retry' ? 'Automatic retry' : 'Informational'}</span></div>
        <p className="mt-1">{row.primary.text}</p>
        <p className="text-muted-foreground mt-2">{row.repo}{row.issue ? ` #${row.issue}` : ''} · First seen: {row.firstSeen || 'unknown'} · Last seen: {row.lastSeen || 'unknown'} · Occurrences: {row.occurrences}</p>
        <div className="flex flex-wrap gap-3 mt-2"><a className="text-brand underline" href={row.issue ? issueUrl(row.repo, row.issue, repoInfo) : repoUrl(row.repo, repoInfo)} target="_blank" rel="noreferrer">{row.issue ? 'View issue' : 'View repository'}</a>{row.runUrl && <a className="text-brand underline" href={row.runUrl}>View run</a>}</div>
        {row.reasons.length > 1 && <details className="mt-2"><summary className="cursor-pointer">Supporting reasons ({row.reasons.length - 1})</summary><ul className="list-disc pl-4">{row.reasons.filter(r => r.text !== row.primary.text).map(r => <li key={r.text}>{r.text}</li>)}</ul></details>}
      </li>)}</ul>
      {rows.length > limit && <button type="button" className="text-sm underline mt-2" onClick={() => setLimit(limit + ATTENTION_LIMIT)}>Show more ({rows.length - limit})</button>}
    </> : <p className="text-sm text-muted-foreground">Nothing needs you</p>}
  </section>
}
