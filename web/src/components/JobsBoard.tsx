import { useEffect, useState } from 'react'
import { Layers, RefreshCw, ChevronDown, ChevronUp } from 'lucide-react'
import { fetchJobs, fetchRuns, timeAgo, type JobRecord, type RunRecord } from '../lib/cloudApi'


type JobStatus = JobRecord['status'] | 'all'

const STATUS_COLORS: Record<string, { bg: string; text: string; border: string }> = {
  pending:   { bg: 'hsl(38 92% 50% / 0.12)', text: 'hsl(38 92% 44%)', border: 'hsl(38 92% 50% / 0.3)' },
  leased:    { bg: 'hsl(210 100% 56% / 0.12)', text: 'hsl(210 100% 48%)', border: 'hsl(210 100% 56% / 0.3)' },
  running:   { bg: 'hsl(262 80% 60% / 0.12)', text: 'hsl(262 80% 52%)', border: 'hsl(262 80% 60% / 0.3)' },
  succeeded: { bg: 'hsl(142 76% 36% / 0.12)', text: 'hsl(142 76% 32%)', border: 'hsl(142 76% 36% / 0.3)' },
  failed:    { bg: 'hsl(0 84% 60% / 0.12)', text: 'hsl(0 84% 54%)', border: 'hsl(0 84% 60% / 0.3)' },
  cancelled: { bg: 'hsl(var(--bg-panel))', text: 'hsl(var(--text-low))', border: 'hsl(var(--border))' },
  expired:   { bg: 'hsl(var(--bg-panel))', text: 'hsl(var(--text-low))', border: 'hsl(var(--border))' },
}

function StatusBadge({ status }: { status: string }) {
  const colors = STATUS_COLORS[status] ?? STATUS_COLORS.cancelled
  return (
    <span
      className="inline-flex items-center text-xs px-2 py-0.5 rounded-full font-medium"
      style={{ background: colors.bg, color: colors.text, border: `1px solid ${colors.border}` }}
    >
      {status}
    </span>
  )
}

export function JobsBoard() {
  const [jobs, setJobs] = useState<JobRecord[]>([])
  const [runs, setRuns] = useState<RunRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState<JobStatus>('all')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const load = () => {
    setLoading(true)
    setError(null)
    Promise.all([fetchJobs(), fetchRuns()])
      .then(([j, r]) => {
        setJobs(j.jobs ?? [])
        setRuns(r.runs ?? [])
      })
      .catch(e => setError(String(e)))
      .finally(() => setLoading(false))
  }

  useEffect(load, [])

  const allStatuses: JobStatus[] = ['all', 'pending', 'leased', 'running', 'succeeded', 'failed', 'cancelled', 'expired']
  const filtered = filter === 'all' ? jobs : jobs.filter(j => j.status === filter)
  const runsForJob = (jobId: string) => runs.filter(r => r.job_id === jobId)
  const toggleExpand = (id: string) =>
    setExpanded(prev => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })

  return (
    <div className="px-6 py-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-base font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
          Jobs &amp; Runs
        </h1>
        <button
          onClick={load}
          disabled={loading}
          className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
          style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
        >
          <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
          Refresh
        </button>
      </div>

      {/* Status filter chips */}
      <div className="flex flex-wrap gap-1.5 mb-4">
        {allStatuses.map(s => {
          const active = filter === s
          const colors = s !== 'all' ? (STATUS_COLORS[s] ?? STATUS_COLORS.cancelled) : null
          return (
            <button
              key={s}
              onClick={() => setFilter(s)}
              className="text-xs px-2.5 py-1 rounded-full transition-colors border font-medium"
              style={{
                background: active ? (colors?.bg ?? 'hsl(var(--brand) / 0.1)') : 'transparent',
                color: active ? (colors?.text ?? 'hsl(var(--brand))') : 'hsl(var(--text-low))',
                borderColor: active ? (colors?.border ?? 'hsl(var(--brand) / 0.4)') : 'hsl(var(--border))',
              }}
            >
              {s}
              {s !== 'all' && (
                <span className="ml-1 opacity-70">
                  {jobs.filter(j => j.status === s).length}
                </span>
              )}
            </button>
          )
        })}
      </div>

      {error && (
        <div
          className="mb-4 px-4 py-3 rounded-md text-sm border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
        >
          {error}
        </div>
      )}

      {!loading && filtered.length === 0 && !error && (
        filter === 'all' ? <EmptyJobs /> : (
          <div className="text-center py-12 text-sm" style={{ color: 'hsl(var(--text-low))' }}>
            No {filter} jobs
          </div>
        )
      )}

      {filtered.length > 0 && (
        <div className="space-y-2">
          {filtered.map(job => {
            const isOpen = expanded.has(job.spec.job_id)
            const jobRuns = runsForJob(job.spec.job_id)
            return (
              <div
                key={job.spec.job_id}
                className="rounded-lg border overflow-hidden"
                style={{ borderColor: 'hsl(var(--border))' }}
              >
                {/* Job row */}
                <button
                  className="w-full text-left px-4 py-3 flex items-center gap-3 transition-colors hover:bg-[hsl(var(--bg-panel)/0.5)]"
                  onClick={() => toggleExpand(job.spec.job_id)}
                >
                  <StatusBadge status={job.status} />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                        {job.spec.repo}
                      </span>
                      <span className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                        #{job.spec.number}
                      </span>
                      <span
                        className="text-xs px-1.5 py-0.5 rounded"
                        style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))', border: '1px solid hsl(var(--border))' }}
                      >
                        {job.spec.operator}
                      </span>
                    </div>
                    {job.spec.title && (
                      <p className="text-sm truncate mt-0.5" style={{ color: 'hsl(var(--text-high))' }}>
                        {job.spec.title}
                      </p>
                    )}
                  </div>
                  <div className="flex items-center gap-3 shrink-0">
                    <span className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {timeAgo(job.updated_at)}
                    </span>
                    {jobRuns.length > 0 && (
                      <span className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                        {jobRuns.length} run{jobRuns.length !== 1 ? 's' : ''}
                      </span>
                    )}
                    {isOpen ? <ChevronUp size={14} style={{ color: 'hsl(var(--text-low))' }} /> : <ChevronDown size={14} style={{ color: 'hsl(var(--text-low))' }} />}
                  </div>
                </button>

                {/* Run details drawer */}
                {isOpen && (
                  <div
                    className="border-t px-4 py-3"
                    style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel) / 0.5)' }}
                  >
                    <div className="grid grid-cols-3 gap-3 text-xs mb-3">
                      <div>
                        <p className="font-medium mb-0.5" style={{ color: 'hsl(var(--text-low))' }}>Job ID</p>
                        <p className="font-mono" style={{ color: 'hsl(var(--text-high))' }}>{job.spec.job_id}</p>
                      </div>
                      <div>
                        <p className="font-medium mb-0.5" style={{ color: 'hsl(var(--text-low))' }}>Attempts</p>
                        <p style={{ color: 'hsl(var(--text-high))' }}>{job.attempt_count}</p>
                      </div>
                      <div>
                        <p className="font-medium mb-0.5" style={{ color: 'hsl(var(--text-low))' }}>Created</p>
                        <p style={{ color: 'hsl(var(--text-high))' }}>{timeAgo(job.created_at)}</p>
                      </div>
                      {job.bound_machine_id && (
                        <div>
                          <p className="font-medium mb-0.5" style={{ color: 'hsl(var(--text-low))' }}>Machine</p>
                          <p className="font-mono" style={{ color: 'hsl(var(--text-high))' }}>{job.bound_machine_id}</p>
                        </div>
                      )}
                    </div>

                    {jobRuns.length > 0 ? (
                      <div>
                        <p className="text-xs font-medium mb-2" style={{ color: 'hsl(var(--text-low))' }}>Runs</p>
                        <div className="space-y-1.5">
                          {jobRuns.map(run => (
                            <div
                              key={run.id}
                              className="rounded-md px-3 py-2 flex items-center gap-3 text-xs border"
                              style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-primary))' }}
                            >
                              <StatusBadge status={run.status} />
                              <span className="font-mono" style={{ color: 'hsl(var(--text-low))' }}>{run.id}</span>
                              {run.outcome && (
                                <span style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>{run.outcome}</span>
                              )}
                              <span className="ml-auto" style={{ color: 'hsl(var(--text-low))' }}>
                                {run.ended_at ? timeAgo(run.ended_at) : 'running…'}
                              </span>
                            </div>
                          ))}
                        </div>
                      </div>
                    ) : (
                      <p className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>No runs recorded yet.</p>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function EmptyJobs() {
  return (
    <div
      className="rounded-lg border px-6 py-12 text-center"
      style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
    >
      <Layers size={32} className="mx-auto mb-3 opacity-30" style={{ color: 'hsl(var(--text-low))' }} />
      <p className="text-sm font-medium mb-1" style={{ color: 'hsl(var(--text-high))' }}>
        No jobs queued
      </p>
      <p className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
        Jobs appear here when issues or PRs match an operator&apos;s trigger conditions.
      </p>
    </div>
  )
}
