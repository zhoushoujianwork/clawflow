import { createFileRoute, Link } from '@tanstack/react-router'
import { ChevronLeft, ExternalLink } from 'lucide-react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { usePoll } from '../lib/usePoll'
import { Markdown } from '../components/Markdown'
import { FreshnessBanner, JobStatusBadge } from '../components/JobStatus'
import { LiveLog } from '../components/LiveLog'
import { STAGE_LABEL, TRIGGER_LABEL, detailLabel, formatDuration, isTerminal, jobElapsed, label, type JobDetail, type TimelineEvent } from '../lib/jobs'
import { jobTitle } from './_app.jobs.index'

export const Route = createFileRoute('/_app/jobs/$id')({
  component: JobPage,
})

const EVENT_LABEL: Record<string, string> = {
  queued: 'Queued', started: 'Started on a worker', attempt_done: 'Attempt finished', attempt_failed: 'Attempt failed',
  attempt_cancelled: 'Attempt cancelled', attempt_expired: 'Lease expired', attempt_interrupted: 'Attempt interrupted',
  attempt_running: 'Attempt running', result_applied: 'Result written back', result_apply_failed: 'Write-back failed',
  result_apply_deferred: 'Write-back deferred', cancelled: 'Cancelled', lease_expired: 'Lease expired',
  recovery_started: 'Recovery started', recovery_exhausted: 'Recovery gave up', interruption_retained: 'Interruption retained',
  acceptance_superseded: 'Superseded by a newer head', interruption_adopted: 'Interruption adopted', released: 'Released back to the queue',
}

function JobPage() {
  const { id } = Route.useParams()
  useDocumentTitle(`Run ${id.slice(0, 8)}`)
  const { data, error, now, live } = usePoll<JobDetail>(`/api/jobs/${encodeURIComponent(id)}`, 3000)
  return <JobView id={id} data={data} error={error} now={now} live={live} />
}

export function JobView({ id, data, error, now, live }: { id: string; data: JobDetail | null; error: string; now: number; live: boolean }) {
  if (!data) {
    return (
      <div className="max-w-4xl mx-auto px-4 py-6">
        <BackLink />
        <FreshnessBanner live={live} error={error} hasData={false} />
        {error ? <p role="alert">Could not load this run: {error}</p> : <p>Loading…</p>}
      </div>
    )
  }
  const job = data
  const done = isTerminal(job.status)
  const elapsed = jobElapsed(job, now, live, undefined)
  const note = detailLabel(job.detail)
  return (
    <div className="max-w-5xl mx-auto px-4 py-6 min-w-0 [overflow-wrap:anywhere]">
      <BackLink />
      <FreshnessBanner live={live} error={error} hasData />
      <header className="mb-4">
        <div className="flex flex-wrap items-center gap-2 mb-1">
          <JobStatusBadge status={job.status} live={live} detail={job.detail} />
          <span className="text-sm">{label(STAGE_LABEL, job.stage)}</span>
          {note && <span className="text-xs text-amber-700">{note}</span>}
        </div>
        <h1 className="text-xl font-bold">{jobTitle(job)}</h1>
        <dl className="mt-2 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Repository</dt><dd className="font-mono">{job.repo}</dd>
          <dt className="text-muted-foreground">Worker</dt><dd className="font-mono" data-field="worker">{job.worker || '—'}</dd>
          <dt className="text-muted-foreground">Duration</dt>
          <dd className="tabular-nums" data-field="duration">{formatDuration(elapsed)}{job.status === 'running' && !live ? ' (paused: connection lost)' : ''}</dd>
          <dt className="text-muted-foreground">Trigger</dt><dd>{label(TRIGGER_LABEL, job.trigger)}{job.trigger === 'operator' ? ` · ${job.operator}` : ''}</dd>
          <dt className="text-muted-foreground">Attempt</dt><dd>#{job.attempt || '—'}{job.tags.length ? ` · tags: ${job.tags.join(', ')}` : ''}</dd>
          {job.waiting_reason && <><dt className="text-muted-foreground">Waiting</dt><dd>{job.waiting_reason}</dd></>}
        </dl>
        <p className="text-xs text-muted-foreground mt-2">Task status only. {done && job.status === 'success' ? 'A finished task does not mean CI, review or merge finished.' : ''}</p>
      </header>

      <Timeline events={data.timeline} />

      {done && <Result job={data} />}

      {job.status === 'queued' && !job.has_logs
        ? <p className="text-sm text-muted-foreground my-4">This run has not started, so there is no output yet. The log appears here when a worker takes it.</p>
        : <LiveLog jobId={id} />}

      {data.related.length > 1 && (
        <section aria-label="Other runs for this work" className="my-6">
          <h2 className="text-sm font-semibold mb-2">All runs for this work</h2>
          <ul className="space-y-1 text-sm">
            {data.related.map(r => (
              <li key={r.id} className="flex flex-wrap items-center gap-2">
                <JobStatusBadge status={r.status} live={live} detail={r.detail} />
                {r.id === id ? <strong>{label(STAGE_LABEL, r.stage)} (this run)</strong> : <Link to="/jobs/$id" params={{ id: r.id }} className="underline">{label(STAGE_LABEL, r.stage)}</Link>}
                <span className="text-xs text-muted-foreground">{r.worker || 'no worker'} · {new Date(r.queued_at).toLocaleString()}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  )
}

function BackLink() {
  return (
    <Link to="/jobs" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground mb-4">
      <ChevronLeft className="w-3.5 h-3.5" /> Runs
    </Link>
  )
}

function Timeline({ events }: { events: TimelineEvent[] }) {
  return (
    <section aria-label="Stage timeline" className="my-4">
      <h2 className="text-sm font-semibold mb-2">Timeline</h2>
      <ol className="border-l pl-4 space-y-2">
        {events.map((e, i) => (
          <li key={i} className="text-sm relative">
            <span className="absolute -left-[21px] top-1.5 w-2 h-2 rounded-full bg-muted-foreground" aria-hidden="true" />
            <span className="font-medium">{label(EVENT_LABEL, e.event)}</span>
            {e.worker && <span className="text-muted-foreground"> · {e.worker}</span>}
            {e.attempt ? <span className="text-muted-foreground"> · attempt #{e.attempt}</span> : null}
            {e.detail && <span className="text-muted-foreground"> · {e.detail}</span>}
            <time className="block text-xs text-muted-foreground" dateTime={e.at}>{new Date(e.at).toLocaleString()}</time>
          </li>
        ))}
      </ol>
    </section>
  )
}

function Result({ job }: { job: JobDetail }) {
  const a = job.artifacts
  const links: { href: string; text: string }[] = []
  if (a.pr_url) links.push({ href: a.pr_url, text: `Pull request${a.pr_number ? ` #${a.pr_number}` : ''}` })
  const hasArtifacts = links.length > 0 || a.branch || a.head_sha || a.verdict || a.next_owner
  return (
    <section aria-label="Result" className="my-4">
      <h2 className="text-sm font-semibold mb-2">Result</h2>
      {job.error && <pre className="rounded border border-red-200 bg-red-50 text-red-900 p-3 text-sm whitespace-pre-wrap" data-field="error">{job.error}</pre>}
      {job.summary && <div className="rounded-lg border p-4 my-2"><Markdown>{job.summary}</Markdown></div>}
      {!job.error && !job.summary && <p className="text-sm text-muted-foreground">No result text was recorded.</p>}
      {hasArtifacts && (
        <dl className="mt-2 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm" aria-label="Artifacts">
          {links.map(l => <div key={l.href} className="contents"><dt className="text-muted-foreground">Link</dt><dd><a href={l.href} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 underline">{l.text} <ExternalLink className="w-3 h-3" /></a></dd></div>)}
          {a.branch && <><dt className="text-muted-foreground">Branch</dt><dd className="font-mono">{a.branch}</dd></>}
          {a.head_sha && <><dt className="text-muted-foreground">Commit</dt><dd className="font-mono">{a.head_sha.slice(0, 12)}</dd></>}
          {a.verdict && <><dt className="text-muted-foreground">Review verdict</dt><dd>{a.verdict}</dd></>}
          {a.next_owner && <><dt className="text-muted-foreground">Next owner</dt><dd>{a.next_owner}</dd></>}
        </dl>
      )}
    </section>
  )
}
