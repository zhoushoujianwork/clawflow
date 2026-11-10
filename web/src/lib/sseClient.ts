// sseClient — POST-based Server-Sent-Events consumer.
//
// The browser's built-in EventSource only supports GET, so the chat stream
// (which needs a JSON body) is consumed via fetch + ReadableStream and parsed
// by hand against the SSE wire format:
//
//   data: <payload>\n           — one or more lines accumulate into an event
//   <blank line>                 — terminator: handlers fire with the joined data
//
// Multi-line `data:` per spec joins with '\n'. `id:` is passed to onEvent so a
// caller can resume after a dropped connection; `event:` and `retry:` are
// ignored — no backend here uses them.
//
// GET streams (e.g. the live job log) are consumed the same way as POST ones:
// EventSource would also work, but this client gives the caller the abort
// signal, the HTTP status and control over the reconnect schedule.
//
// Two synthetic conventions on top of plain SSE:
//   - `data: [DONE]` → end of stream marker. onDone fires, loop exits.
//   - aborting via AbortSignal is treated as a graceful close (onDone, no
//     onError) so callers can cancel without an error toast.

export interface SSEHandlers {
  /** id is the event's `id:` field when the server sent one. */
  onEvent: (data: string, id?: string) => void
  onError: (err: Error) => void
  /** explicit is true only when the server sent the `[DONE]` marker. */
  onDone: (explicit?: boolean) => void
}

export interface SSEOptions {
  url: string
  /** Ignored for GET. */
  body?: unknown
  method?: 'GET' | 'POST'
  signal?: AbortSignal
  headers?: Record<string, string>
}

const DONE_PAYLOAD = '[DONE]'

export async function streamSSE(opts: SSEOptions, handlers: SSEHandlers): Promise<void> {
  const { url, body, signal, headers } = opts
  const method = opts.method ?? 'POST'
  let doneFired = false
  const fireDone = (explicit = false) => {
    if (doneFired) return
    doneFired = true
    handlers.onDone(explicit)
  }

  let response: Response
  try {
    response = await fetch(url, {
      method,
      headers: {
        ...(method === 'POST' ? { 'Content-Type': 'application/json' } : {}),
        Accept: 'text/event-stream',
        ...(headers || {}),
      },
      ...(method === 'POST' ? { body: JSON.stringify(body) } : {}),
      cache: 'no-store',
      signal,
    })
  } catch (err) {
    if (signal?.aborted) {
      fireDone()
      return
    }
    handlers.onError(err instanceof Error ? err : new Error(String(err)))
    return
  }

  if (!response.ok) {
    handlers.onError(new Error(`HTTP ${response.status} ${response.statusText}`))
    return
  }
  if (!response.body) {
    handlers.onError(new Error('response has no body'))
    return
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder('utf-8')
  let buffer = ''
  // dataLines accumulates the `data:` payloads of the current event until the
  // blank-line terminator flushes them.
  let dataLines: string[] = []
  let eventId: string | undefined

  const flushEvent = (): boolean => {
    const id = eventId
    eventId = undefined
    if (dataLines.length === 0) return true
    const payload = dataLines.join('\n')
    dataLines = []
    if (payload === DONE_PAYLOAD) {
      fireDone(true)
      return false
    }
    handlers.onEvent(payload, id)
    return true
  }

  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })

      // Normalize CRLF → LF so the line splitter works the same on every
      // platform; servers occasionally emit \r\n.
      let nlIdx: number
      while ((nlIdx = buffer.indexOf('\n')) !== -1) {
        let line = buffer.slice(0, nlIdx)
        buffer = buffer.slice(nlIdx + 1)
        if (line.endsWith('\r')) line = line.slice(0, -1)

        if (line === '') {
          // Blank line: event boundary.
          if (!flushEvent()) {
            await reader.cancel().catch(() => {})
            return
          }
          continue
        }

        if (line.startsWith(':')) {
          // Comment / keep-alive — ignore per spec.
          continue
        }

        if (line.startsWith('data:')) {
          // Per spec, a single optional space after the colon is stripped.
          const v = line.slice(5)
          dataLines.push(v.startsWith(' ') ? v.slice(1) : v)
          continue
        }
        if (line.startsWith('id:')) {
          const v = line.slice(3)
          eventId = v.startsWith(' ') ? v.slice(1) : v
          continue
        }
        // Other fields (event:, retry:) ignored.
      }
    }

    // Stream ended without a trailing blank line — flush any pending data so
    // the final event isn't silently dropped.
    flushEvent()
    fireDone()
  } catch (err) {
    if (signal?.aborted) {
      fireDone()
      return
    }
    handlers.onError(err instanceof Error ? err : new Error(String(err)))
  }
}
