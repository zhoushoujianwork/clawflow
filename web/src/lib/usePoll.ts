import { useEffect, useState } from 'react'

export interface Polled<T> {
  data: T | null
  error: string
  /** Time of the last successful response, or 0 before the first. */
  lastOk: number
  /** Wall clock, advanced once a second so durations can tick. */
  now: number
  /** True while the latest request succeeded and its response is recent. */
  live: boolean
}

/**
 * Poll a JSON endpoint. Polling pauses while the tab is hidden and resumes with
 * an immediate fetch. The last good data is kept across failures, and `live`
 * turns false once responses stop arriving, so callers can stop animations and
 * elapsed-time counters instead of presenting stale data as current.
 */
export function usePoll<T>(url: string, intervalMs: number): Polled<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [lastOk, setLastOk] = useState(0)
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    // Local to this effect run: when the URL changes, a request still in flight
    // for the old URL must neither write its data nor schedule another poll.
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const controller = new AbortController()
    const tick = async () => {
      if (document.hidden) { timer = setTimeout(tick, intervalMs); return }
      try {
        const r = await fetch(url, { cache: 'no-store', signal: controller.signal })
        if (!r.ok) throw new Error(r.status === 503 ? 'Coordinator is not running on this host' : `HTTP ${r.status}`)
        const body = (await r.json()) as T
        if (cancelled) return
        setData(body)
        setError('')
        setLastOk(Date.now())
      } catch (err) {
        if (cancelled || controller.signal.aborted) return
        setError(err instanceof Error ? err.message : String(err))
      }
      if (!cancelled) timer = setTimeout(tick, intervalMs)
    }
    const onVisible = () => { if (!document.hidden && !cancelled) { clearTimeout(timer); void tick() } }
    document.addEventListener('visibilitychange', onVisible)
    void tick()
    return () => {
      cancelled = true
      controller.abort()
      clearTimeout(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [url, intervalMs])

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  // A failed latest request ends "live" at once: the page keeps the last data but
  // must not keep animating or counting time as if it were still receiving updates.
  const live = lastOk > 0 && !error && now - lastOk <= Math.max(intervalMs * 3, 10_000)
  return { data, error, lastOk, now, live }
}
