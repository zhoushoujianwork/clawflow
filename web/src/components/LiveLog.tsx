import { useEffect, useMemo, useState } from 'react'
import { followLog, type FollowPhase } from '../lib/followLog'
import { TraceView } from './TraceView'
import { availabilityNote, logToTraceEvents, mergeEntries, type LogEntry, type LogFrame, type LogStreamInfo } from '../lib/jobs'

/**
 * Live execution output of one job, shown through the existing TraceView.
 *
 * The connection, resume cursor and retry schedule live in followLog; this
 * component only holds what the frames say and renders it. After a dropped
 * connection the viewer resumes from the last entry id it holds, so no line
 * repeats or is lost.
 */
export function LiveLog({ jobId }: { jobId: string }) {
  const [entries, setEntries] = useState<LogEntry[]>([])
  const [streams, setStreams] = useState<LogStreamInfo[]>([])
  const [availability, setAvailability] = useState('')
  const [epochs, setEpochs] = useState<LogFrame['epochs']>([])
  const [epoch, setEpoch] = useState(0)
  const [phase, setPhase] = useState<FollowPhase>('connecting')
  const [retryIn, setRetryIn] = useState(0)
  const [message, setMessage] = useState('')
  const [active, setActive] = useState(false)

  useEffect(() => {
    setEntries([]); setStreams([]); setAvailability(''); setEpochs([]); setActive(false); setMessage('')
    let countdown: ReturnType<typeof setInterval> | undefined
    const stop = followLog({ jobId, epoch }, {
      onPhase: (next, info) => {
        setPhase(next)
        clearInterval(countdown)
        if (next === 'reconnecting' && info?.retryInMs != null) {
          setRetryIn(Math.ceil(info.retryInMs / 1000))
          countdown = setInterval(() => setRetryIn(n => Math.max(0, n - 1)), 1000)
        }
        setMessage(next === 'live' ? '' : info?.message ?? '')
      },
      onFrame: frame => {
        setActive(frame.active)
        setAvailability(frame.availability)
        setStreams(frame.streams ?? [])
        setEpochs(frame.epochs ?? [])
        if (frame.entries?.length) setEntries(have => mergeEntries(have, frame.entries))
      },
    })
    return () => { stop(); clearInterval(countdown) }
  }, [jobId, epoch])

  const events = useMemo(() => logToTraceEvents(entries), [entries])
  const note = entries.length === 0 ? availabilityNote(availability, streams.find(s => s.reason)?.reason) : ''
  const dropped = streams.reduce((n, s) => n + (s.dropped_events ?? 0), 0)
  const limited = streams.some(s => s.limited)
  const running = phase === 'live' && active

  return (
    <section aria-label="Job log" className="my-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs mb-2" role="status" aria-live="polite">
        <span className="font-semibold" data-phase={phase}>
          {phase === 'connecting' && 'Connecting…'}
          {phase === 'live' && (active ? 'Live' : 'Up to date')}
          {phase === 'reconnecting' && (retryIn > 0 ? `Connection lost — reconnecting in ${retryIn}s` : 'Reconnecting…')}
          {phase === 'ended' && 'Execution ended'}
          {phase === 'failed' && 'Log unavailable'}
        </span>
        <span>{entries.length} events</span>
        {epochs.length > 1 && (
          <label className="inline-flex items-center gap-1">
            Attempt
            <select value={epoch || epochs[epochs.length - 1].epoch} onChange={e => setEpoch(Number(e.target.value))} className="border rounded px-1 py-0.5 bg-background">
              {epochs.map(e => <option key={e.epoch} value={e.epoch}>#{e.attempt} · {e.worker || 'unknown worker'} · {e.status}</option>)}
            </select>
          </label>
        )}
        {message && <span className="text-amber-700">{message}</span>}
      </div>
      {(limited || dropped > 0) && <p className="text-xs text-amber-700 mb-2">Output was limited: {dropped} events were not stored.</p>}
      {note && <p className="text-sm text-muted-foreground mb-2">{note}</p>}
      <TraceView events={events} loading={phase === 'connecting' && entries.length === 0} running={running} openByDefault={true} />
    </section>
  )
}
