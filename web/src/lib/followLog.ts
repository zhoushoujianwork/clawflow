import { streamSSE } from './sseClient'
import { reconnectDelay, type LogFrame } from './jobs'

export type FollowPhase = 'connecting' | 'live' | 'reconnecting' | 'ended' | 'failed'

export interface FollowHandlers {
  onFrame: (frame: LogFrame) => void
  onPhase: (phase: FollowPhase, info?: { retryInMs?: number; message?: string }) => void
}

export interface FollowOptions {
  jobId: string
  /** Attempt to follow; 0 follows the newest and pins it from the first frame. */
  epoch?: number
  /** Cursor to start after, e.g. entries already held. */
  after?: number
  /** Origin prefix, empty in the browser. */
  base?: string
  /** Reconnect schedule; defaults to 1s doubling to 15s. */
  delay?: (failures: number) => number
}

/**
 * Follow one job's live log over SSE and keep following across dropped
 * connections. Each reconnect asks for entries after the highest id received,
 * so nothing repeats and nothing is skipped. A `[DONE]` marker means the
 * execution ended and the stream is not reopened; a 404 means the job does not
 * exist and retrying cannot help. Returns a function that stops following.
 */
export function followLog(opts: FollowOptions, h: FollowHandlers): () => void {
  const delayFor = opts.delay ?? reconnectDelay
  let cursor = opts.after ?? 0
  let epoch = opts.epoch ?? 0
  let stopped = false
  let finished = false
  let failures = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let controller: AbortController | undefined

  // Both a clean close without [DONE] and a transport or HTTP error end up here:
  // wait, then ask again from the cursor.
  const retry = (message?: string) => {
    if (stopped || finished) return
    const wait = delayFor(failures++)
    h.onPhase('reconnecting', { retryInMs: wait, message })
    timer = setTimeout(connect, wait)
  }

  const connect = () => {
    if (stopped || finished) return
    controller = new AbortController()
    const url = `${opts.base ?? ''}/api/jobs/${encodeURIComponent(opts.jobId)}/logs?epoch=${epoch}&after=${cursor}`
    void streamSSE({ url, method: 'GET', signal: controller.signal }, {
      onEvent: payload => {
        let frame: LogFrame
        try { frame = JSON.parse(payload) } catch { return }
        failures = 0
        if (!epoch && frame.epoch) epoch = frame.epoch
        for (const e of frame.entries ?? []) if (e.id > cursor) cursor = e.id
        h.onPhase('live')
        h.onFrame(frame)
      },
      onError: err => {
        if (stopped || finished) return
        if (/HTTP 404/.test(err.message)) {
          finished = true
          h.onPhase('failed', { message: 'This job does not exist.' })
          return
        }
        retry(/HTTP 503/.test(err.message) ? 'The server is busy or the coordinator is not running; retrying.' : err.message)
      },
      onDone: explicit => {
        if (stopped || finished) return
        if (explicit) {
          finished = true
          h.onPhase('ended')
          return
        }
        retry()
      },
    })
  }
  h.onPhase('connecting')
  connect()
  return () => {
    stopped = true
    controller?.abort()
    clearTimeout(timer)
  }
}
