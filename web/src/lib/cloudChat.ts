// cloudChat.ts — typed fetch + EventSource helpers for the cloud chat API.
//
// The cloud server (`internal/cloud/chat`) runs `claude -p` inside a
// per-user clone of a target repo and streams the stdout back as
// Server-Sent Events. All requests carry the session cookie via
// credentials: 'include' — the browser never sees the underlying
// access token (it lives only in the HttpOnly cookie).
//
// Endpoint inventory (read-only mirror of internal/cloud/chat/handler.go):
//
//   POST   /api/cloud/chat/sessions               create
//   GET    /api/cloud/chat/sessions               list
//   GET    /api/cloud/chat/sessions/{id}/stream   SSE
//   POST   /api/cloud/chat/sessions/{id}/message  queue follow-up
//   DELETE /api/cloud/chat/sessions/{id}          kill + cleanup

/** Shape of an SSE frame emitted by the chat handler. The `time` field
 *  is RFC3339; we surface it verbatim to the UI. */
export type ChatEvent = {
  type: 'output' | 'stderr' | 'end' | 'error'
  text: string
  time: string
}

/** Response shape for POST /api/cloud/chat/sessions. */
export interface ChatSession {
  id: string
  repo: string
  work_dir: string
  created_at: string
}

async function readError(res: Response): Promise<string> {
  try {
    const data = (await res.json()) as { error?: string }
    if (data?.error) return data.error
  } catch {
    /* fall through */
  }
  return `${res.status} ${res.statusText}`
}

/** Create a new chat session bound to `repo`. The server starts the
 *  subprocess immediately with `message` as the first turn; the caller
 *  should subscribe to the stream right after this resolves. */
export async function createChatSession(
  repo: string,
  message: string,
): Promise<ChatSession> {
  const res = await fetch('/api/cloud/chat/sessions', {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ repo, message }),
  })
  if (!res.ok) throw new Error(await readError(res))
  return (await res.json()) as ChatSession
}

/** Queue a follow-up turn. The server cancels the previous subprocess
 *  context (emitting `end` on the stream) and starts a new one — the
 *  EventSource on the client reconnects to the same /stream URL
 *  automatically and picks up the new turn. */
export async function sendChatMessage(
  sessionId: string,
  message: string,
): Promise<void> {
  const res = await fetch(
    `/api/cloud/chat/sessions/${encodeURIComponent(sessionId)}/message`,
    {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message }),
    },
  )
  if (!res.ok) throw new Error(await readError(res))
}

/** Tear down the server-side session. Best-effort: callers usually
 *  fire-and-forget on drawer close. 404 is treated as success because
 *  it just means the GC already reaped the session. */
export async function deleteChatSession(sessionId: string): Promise<void> {
  const res = await fetch(
    `/api/cloud/chat/sessions/${encodeURIComponent(sessionId)}`,
    { method: 'DELETE', credentials: 'include' },
  )
  if (!res.ok && res.status !== 204 && res.status !== 404) {
    throw new Error(await readError(res))
  }
}

/** subscribeToChat wraps `new EventSource(url)` and parses the JSON
 *  frames that the server emits as `data: {...}\n\n`. The browser's
 *  EventSource auto-reconnects on network blips; on real protocol
 *  errors we surface to `onError` and the caller can decide whether
 *  to teardown.
 *
 *  EventSource sends cookies on same-origin requests by default;
 *  `withCredentials: true` is required for cross-origin, which we
 *  set defensively even though the cloud app and the API share an
 *  origin in production. */
export function subscribeToChat(
  sessionId: string,
  onEvent: (e: ChatEvent) => void,
  onError: (err: Error) => void,
): () => void {
  const url = `/api/cloud/chat/sessions/${encodeURIComponent(sessionId)}/stream`
  const es = new EventSource(url, { withCredentials: true })

  es.onmessage = ev => {
    if (!ev.data) return
    try {
      const parsed = JSON.parse(ev.data) as ChatEvent
      onEvent(parsed)
    } catch (err) {
      onError(err instanceof Error ? err : new Error(String(err)))
    }
  }

  // EventSource fires `error` for transient disconnects too — the
  // browser keeps retrying behind the scenes. We only forward the
  // error if the connection is permanently closed (readyState ===
  // CLOSED). For CONNECTING we stay silent; the caller doesn't need
  // to know about a retry.
  es.onerror = () => {
    if (es.readyState === EventSource.CLOSED) {
      onError(new Error('chat stream closed'))
    }
  }

  return () => {
    es.close()
  }
}
