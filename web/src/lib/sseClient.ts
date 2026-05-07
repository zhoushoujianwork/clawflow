// sseClient — POST-based Server-Sent-Events consumer.
//
// The browser's built-in EventSource only supports GET, so the chat stream
// (which needs a JSON body) is consumed via fetch + ReadableStream and parsed
// by hand against the SSE wire format:
//
//   data: <payload>\n           — one or more lines accumulate into an event
//   <blank line>                 — terminator: handlers fire with the joined data
//
// Multi-line `data:` per spec joins with '\n'. Other field names (`event:`,
// `id:`, `retry:`) are ignored — the chat backend doesn't use them.
//
// Two synthetic conventions on top of plain SSE:
//   - `data: [DONE]` → end of stream marker. onDone fires, loop exits.
//   - aborting via AbortSignal is treated as a graceful close (onDone, no
//     onError) so callers can cancel without an error toast.

export interface SSEHandlers {
  onEvent: (data: string) => void
  onError: (err: Error) => void
  onDone: () => void
}

export interface SSEOptions {
  url: string
  body: unknown
  signal?: AbortSignal
  headers?: Record<string, string>
}

const DONE_PAYLOAD = '[DONE]'

export async function streamSSE(opts: SSEOptions, handlers: SSEHandlers): Promise<void> {
  const { url, body, signal, headers } = opts
  let doneFired = false
  const fireDone = () => {
    if (doneFired) return
    doneFired = true
    handlers.onDone()
  }

  let response: Response
  try {
    response = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'text/event-stream',
        ...(headers || {}),
      },
      body: JSON.stringify(body),
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

  const flushEvent = (): boolean => {
    if (dataLines.length === 0) return true
    const payload = dataLines.join('\n')
    dataLines = []
    if (payload === DONE_PAYLOAD) {
      fireDone()
      return false
    }
    handlers.onEvent(payload)
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
        // Other fields (event:, id:, retry:) ignored.
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
