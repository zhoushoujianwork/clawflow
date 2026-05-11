import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { ChevronLeft, Activity, Copy, Check } from 'lucide-react'
import { cn } from '../lib/utils'
import {
  type PilotRun,
  PilotRunDetailModal,
} from '../components/PilotRun'

// Pilot wake history — every recorded wake for one project, with the
// same row format users were used to in the old Pilot Activity card.
// Splitting it onto its own page keeps the main project view focused
// on "what's happening RIGHT NOW" (latest wake + duties + digest),
// and lets the history grow without pushing the rest of the page out
// of the viewport.
export const Route = createFileRoute('/_app/projects/$name_/pilot-runs')({
  component: PilotRunsPage,
})

function PilotRunsPage() {
  const { name } = Route.useParams()
  const [runs, setRuns] = useState<PilotRun[]>([])
  const [loading, setLoading] = useState(true)
  const [visible, setVisible] = useState(20)
  const [detail, setDetail] = useState<PilotRun | null>(null)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    fetch(`/api/project/pilot-runs?project=${encodeURIComponent(name)}`, { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then(data => {
        setRuns(Array.isArray(data) ? data : [])
        setLoading(false)
      })
  }, [name])

  function handleCopyAll() {
    const text = runs.map(r => {
      const ts = new Date(r.started_at).toLocaleString()
      const result = r.result ? r.result.replace(/^PILOT-RESULT:\s*/, '') : r.error ?? 'no result'
      const cost = r.usage ? ` $${r.usage.total_cost_usd.toFixed(2)}` : ''
      return `[${ts}]${cost} ${result}`
    }).join('\n')
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <Link
        to="/projects/$name"
        params={{ name }}
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground mb-4"
      >
        <ChevronLeft className="w-3.5 h-3.5" /> {name}
      </Link>

      <div className="flex items-center justify-between mb-6 gap-3">
        <div>
          <h1 className="text-2xl font-bold text-foreground font-mono flex items-center gap-2">
            <Activity className="w-5 h-5 text-muted-foreground" />
            Pilot wakes
          </h1>
          <p className="text-xs text-muted-foreground mt-1">
            Every recorded wake for <code className="font-mono text-foreground">{name}</code> — newest first. Click any row for the full duty breakdown.
          </p>
        </div>
        {runs.length > 0 && (
          <button
            type="button"
            onClick={handleCopyAll}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium border border-border text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors shrink-0"
            title="Copy all wakes as text"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-emerald-500" /> : <Copy className="w-3.5 h-3.5" />}
            {copied ? 'Copied' : 'Copy all'}
          </button>
        )}
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground py-8">Loading…</p>
      ) : runs.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <p className="text-sm text-muted-foreground mb-1">No Pilot wakes recorded yet.</p>
          <p className="text-xs text-muted-foreground">
            Wakes appear here after each <code className="font-mono">clawflow run</code> pass, or when you trigger one manually from the project page.
          </p>
        </div>
      ) : (
        <div className="bg-card border border-border rounded-xl divide-y divide-border overflow-hidden">
          {runs.slice(0, visible).map((run, i) => (
            <button
              key={i}
              type="button"
              onClick={() => setDetail(run)}
              className="w-full text-left px-4 py-3 hover:bg-secondary/20 transition-colors"
            >
              <div className="flex items-center gap-2 mb-1 flex-wrap">
                <span
                  className={cn(
                    'inline-block w-2 h-2 rounded-full shrink-0',
                    run.status === 'success' ? 'bg-emerald-500' : run.status === 'failed' ? 'bg-red-500' : 'bg-amber-500 animate-pulse',
                  )}
                />
                <span className="text-xs text-muted-foreground tabular-nums">
                  {new Date(run.started_at).toLocaleString()}
                </span>
                {run.ended_at && (
                  <span className="text-xs text-muted-foreground tabular-nums">
                    · {Math.round((new Date(run.ended_at).getTime() - new Date(run.started_at).getTime()) / 1000)}s
                  </span>
                )}
                {run.usage && (
                  <>
                    <span className="text-xs text-muted-foreground tabular-nums">
                      · ${run.usage.total_cost_usd.toFixed(2)}
                    </span>
                    <span className="text-xs text-muted-foreground tabular-nums">
                      · {run.usage.num_turns} turns
                    </span>
                  </>
                )}
              </div>
              {run.result ? (
                <p className="text-sm text-foreground truncate" title={run.result.replace(/^PILOT-RESULT:\s*/, '')}>
                  {run.result.replace(/^PILOT-RESULT:\s*/, '')}
                </p>
              ) : run.error ? (
                <p className="text-sm text-red-600 font-mono text-xs truncate" title={run.error}>{run.error}</p>
              ) : (
                <p className="text-xs text-muted-foreground italic">no result line</p>
              )}
            </button>
          ))}
          {runs.length > visible && (
            <button
              type="button"
              onClick={() => setVisible(v => v + 20)}
              className="w-full px-4 py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-secondary/20 transition-colors text-center"
            >
              Show 20 more ({runs.length - visible} remaining)
            </button>
          )}
        </div>
      )}

      {detail && <PilotRunDetailModal run={detail} onClose={() => setDetail(null)} />}
    </div>
  )
}
