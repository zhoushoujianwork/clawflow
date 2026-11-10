import { createFileRoute, Link } from '@tanstack/react-router'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { usePoll } from '../lib/usePoll'
import { FreshnessBanner, JobStatusBadge } from '../components/JobStatus'
import { STAGE_LABEL, ago, formatDuration, heartbeatState, jobElapsed, label, type Runner } from '../lib/jobs'
import { jobTitle } from './_app.jobs.index'

export const Route = createFileRoute('/_app/runners')({
  component: RunnersPage,
})

function RunnersPage() {
  useDocumentTitle('Runners')
  const { data, error, now, live } = usePoll<{ generated_at: string; runners: Runner[] }>('/api/runners', 3000)
  return <RunnersView data={data} error={error} now={now} live={live} />
}

export function RunnersView({ data, error, now, live }: { data: { generated_at: string; runners: Runner[] } | null; error: string; now: number; live: boolean }) {
  const runners = data?.runners ?? []
  return (
    <div className="max-w-6xl mx-auto px-4 py-6 min-w-0">
      <div className="flex flex-wrap items-end justify-between gap-3 mb-4">
        <div>
          <h1 className="text-2xl font-bold">Runners</h1>
          <p className="text-sm text-muted-foreground">Workers that take tasks, what each one is running, and when it was last heard from. Read-only.</p>
        </div>
        <Link to="/jobs" className="underline text-sm">Runs</Link>
      </div>
      <FreshnessBanner live={live} error={error} hasData={!!data} />
      {!data && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
      {data && runners.length === 0 && <p role="status" className="text-sm text-muted-foreground border rounded-lg p-6 text-center">No workers are registered.</p>}
      <ul className="space-y-3">
        {runners.map(r => {
          const online = live && r.online
          const beat = heartbeatState(r.last_heartbeat, now)
          return (
            <li key={r.name} className="border rounded-lg p-3" data-runner={r.name}>
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
                <h2 className="font-mono font-semibold break-all">{r.name}</h2>
                <span className={`text-xs font-semibold ${online ? 'text-green-700' : 'text-muted-foreground'}`}>
                  {r.revoked ? 'Revoked' : !live ? 'Unknown (stale data)' : r.online ? 'Online' : 'Offline'}
                </span>
                {r.paused && <span className="text-xs text-amber-700">Draining</span>}
                <span className="text-xs text-muted-foreground">Last seen {ago(r.last_seen, now)}</span>
                <span className="text-xs text-muted-foreground">
                  Heartbeat {beat === 'unknown' ? 'unknown' : `${ago(r.last_heartbeat, now)}${beat === 'lost' ? ' (lost)' : ''}`}
                </span>
                <span className="text-xs text-muted-foreground">{r.running.length} / {r.max_parallel} running</span>
              </div>
              <p className="text-xs mt-1">
                <span className="text-muted-foreground">Tags: </span>{r.tags.length ? r.tags.map(t => <code key={t} className="mr-1 px-1 rounded bg-secondary">{t}</code>) : '—'}
                <span className="text-muted-foreground ml-3">Roles: </span>{r.roles.length ? r.roles.join(', ') : '—'}
              </p>
              {r.running.length > 0 && (
                <ul className="mt-2 border-t pt-2 space-y-1">
                  {r.running.map(j => (
                    <li key={j.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
                      <JobStatusBadge status={j.status} live={live} detail={j.detail} />
                      <Link to="/jobs/$id" params={{ id: j.id }} className="underline break-words [overflow-wrap:anywhere]">{j.repo} {jobTitle(j)}</Link>
                      <span className="text-xs text-muted-foreground">{label(STAGE_LABEL, j.stage)}</span>
                      <span className="text-xs tabular-nums">{formatDuration(jobElapsed(j, now, live, data?.generated_at))}</span>
                    </li>
                  ))}
                </ul>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
