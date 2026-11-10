import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { usePoll } from '../lib/usePoll'
import { FreshnessBanner, JobStatusBadge } from '../components/JobStatus'
import { STAGE_LABEL, TRIGGER_LABEL, detailLabel, formatDuration, jobElapsed, label, type Job } from '../lib/jobs'

export const Route = createFileRoute('/_app/jobs/')({
  component: JobsPage,
})

const STATUSES = ['', 'running', 'queued', 'failed', 'success', 'cancelled'] as const

export function jobTitle(j: Pick<Job, 'repo' | 'issue' | 'pr' | 'title'>) {
  const ref = j.pr ? `PR #${j.pr}` : `#${j.issue}`
  return `${ref}${j.title ? ` · ${j.title}` : ''}`
}

function JobsPage() {
  useDocumentTitle('Runs')
  const [status, setStatus] = useState<string>('')
  const [repo, setRepo] = useState('')
  const url = `/api/jobs?limit=200${status ? `&status=${status}` : ''}${repo ? `&repo=${encodeURIComponent(repo)}` : ''}`
  const { data, error, now, live } = usePoll<{ generated_at: string; jobs: Job[] }>(url, 3000)
  return <JobsView data={data} error={error} now={now} live={live} status={status} repo={repo} onStatus={setStatus} onRepo={setRepo} />
}

/** Group by repository, then issue or PR; rows keep the server's newest-first order. */
export function groupJobs(jobs: Job[]): [string, Job[]][] {
  const map = new Map<string, Job[]>()
  for (const j of jobs) {
    const key = `${j.repo}\u0000${j.pr ? `pr-${j.pr}` : `issue-${j.issue}`}`
    map.set(key, [...(map.get(key) ?? []), j])
  }
  return [...map.entries()]
}

export function JobsView({ data, error, now, live, status, repo, onStatus, onRepo }: {
  data: { generated_at: string; jobs: Job[] } | null; error: string; now: number; live: boolean
  status: string; repo: string; onStatus: (s: string) => void; onRepo: (r: string) => void
}) {
  const jobs = data?.jobs ?? []
  const repos = [...new Set(jobs.map(j => j.repo))].sort()
  const groups = groupJobs(jobs)
  return (
    <div className="max-w-6xl mx-auto px-4 py-6 min-w-0">
      <div className="flex flex-wrap items-end justify-between gap-3 mb-4">
        <div>
          <h1 className="text-2xl font-bold">Runs</h1>
          <p className="text-sm text-muted-foreground">Every task by repository and issue. Read-only. A finished run does not mean CI, review or merge finished.</p>
        </div>
        <nav aria-label="Run views" className="flex gap-2 text-sm">
          <Link to="/runners" className="underline">Runners</Link>
        </nav>
      </div>
      <FreshnessBanner live={live} error={error} hasData={!!data} />
      <div className="flex flex-wrap gap-2 mb-3 text-sm" role="group" aria-label="Filters">
        {STATUSES.map(s => (
          <button key={s || 'all'} onClick={() => onStatus(s)} aria-pressed={status === s} className={`px-3 py-1 rounded border ${status === s ? 'bg-secondary' : ''}`}>
            {s ? label({ running: 'Running', queued: 'Queued', failed: 'Failed', success: 'Succeeded', cancelled: 'Cancelled' }, s) : 'All'}
          </button>
        ))}
        <label className="inline-flex items-center gap-1">
          Repository
          <select value={repo} onChange={e => onRepo(e.target.value)} className="border rounded px-1 py-1 bg-background">
            <option value="">All</option>
            {repos.map(r => <option key={r} value={r}>{r}</option>)}
          </select>
        </label>
      </div>
      {!data && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
      {data && jobs.length === 0 && <p role="status" className="text-sm text-muted-foreground border rounded-lg p-6 text-center">No runs match.</p>}
      <div className="space-y-4">
        {groups.map(([key, rows]) => (
          <section key={key} aria-label={`${rows[0].repo} ${rows[0].pr ? `PR ${rows[0].pr}` : `issue ${rows[0].issue}`}`} className="border rounded-lg overflow-hidden">
            <h2 className="px-3 py-2 text-sm font-semibold bg-secondary/40 break-words [overflow-wrap:anywhere]">
              <span className="font-mono">{rows[0].repo}</span> · {jobTitle(rows[0])}
            </h2>
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="text-xs text-muted-foreground">
                  <tr>{['Status', 'Stage', 'Worker', 'Duration', 'Trigger', 'Started'].map(h => <th key={h} scope="col" className="px-3 py-1.5 font-medium">{h}</th>)}</tr>
                </thead>
                <tbody>
                  {rows.map(j => <JobRow key={j.id} job={j} now={now} live={live} asOf={data?.generated_at} />)}
                </tbody>
              </table>
            </div>
          </section>
        ))}
      </div>
    </div>
  )
}

function JobRow({ job, now, live, asOf }: { job: Job; now: number; live: boolean; asOf?: string }) {
  const elapsed = jobElapsed(job, now, live, asOf)
  const note = detailLabel(job.detail)
  return (
    <tr className="border-t align-top">
      <td className="px-3 py-2">
        <Link to="/jobs/$id" params={{ id: job.id }} className="inline-flex items-center gap-2 hover:underline" aria-label={`Open run ${job.id.slice(0, 8)}`}>
          <JobStatusBadge status={job.status} live={live} detail={job.detail} />
        </Link>
        {note && <p className="text-xs text-amber-700">{note}</p>}
        {job.waiting_reason && <p className="text-xs text-muted-foreground">{job.waiting_reason}</p>}
      </td>
      <td className="px-3 py-2">{label(STAGE_LABEL, job.stage)}</td>
      <td className="px-3 py-2 font-mono text-xs break-all">{job.worker || '—'}</td>
      <td className="px-3 py-2 tabular-nums" title={job.status === 'running' ? 'Counted from the recorded start' : undefined}>{formatDuration(elapsed)}</td>
      <td className="px-3 py-2">{label(TRIGGER_LABEL, job.trigger)}{job.trigger === 'operator' ? ` · ${job.operator}` : ''}</td>
      <td className="px-3 py-2 text-xs">{job.started_at ? <time dateTime={job.started_at}>{new Date(job.started_at).toLocaleString()}</time> : 'Not started'}</td>
    </tr>
  )
}
