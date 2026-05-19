// chatContext.tsx — dual-mode chat orchestration.
//
// Two host modes share this context:
//
//   • cloud  — bundle served from clawflow.daboluo.cc; there is no
//              local CLI on the user's machine. `open(target)` creates
//              a server-side chat session (POST /api/cloud/chat/sessions)
//              and shows the in-browser drawer.
//
//   • local  — bundle served from `clawflow web`. `open(target)` POSTs
//              to /api/chat/spawn, which opens a native terminal on the
//              user's machine. If that fails, we fall back to the
//              spawn-error drawer with a manual command to copy.
//
// Mode is resolved once on mount by probing /api/v1/auth/me via the
// existing fetchAuthMe helper: 'no-cloud' → local; anything else → cloud.
//
// Feedback flow (ReportIssueButton calls open({feedback:true})):
//
//   • local mode: spawn `clawflow feedback` in a native terminal —
//     unchanged from prior behavior.
//
//   • cloud mode: open a chat against zhoushoujianwork/clawflow with a
//     primed "I'd like to report feedback…" message. Picked option (a)
//     from the design doc — keeps the entry point alive on the cloud
//     surface and avoids inventing a new GitHub-issue-form UI in this
//     pass. The clawflow repo must be cloneable by the server's GitHub
//     App for this to work; if it isn't, the session-create call
//     surfaces the error in the drawer as a normal chat error.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { fetchAuthMe } from './cloudApi'
import {
  createChatSession,
  deleteChatSession,
  sendChatMessage,
  subscribeToChat,
  type ChatEvent,
} from './cloudChat'

/** ChatTarget mirrors the prior call sites (IssueList row "Chat", the
 *  top-bar "Report an issue" button). Cloud mode only consumes `repo`,
 *  `feedback`; the other fields are forwarded to the local spawn API
 *  unchanged. */
export interface ChatTarget {
  repo?: string
  issue?: number
  model?: string
  mode?: 'issue' | 'edit'
  project?: string
  action?: 'generate' | 'chat'
  feedback?: boolean
}

/** SpawnError describes a failure from the local /api/chat/spawn call.
 *  Only meaningful in local mode. */
export interface SpawnError {
  target: ChatTarget
  error: string
  command?: string
  hint?: string
}

/** Message is a single turn in the cloud chat drawer. Assistant
 *  content is appended to as `output` events stream in; stderr is
 *  rendered separately as a dim secondary stream. */
export interface ChatMessage {
  id: string
  role: 'user' | 'assistant' | 'error'
  text: string
  // True while the assistant message is still receiving chunks. We
  // use it to render a "streaming…" caret and to know when to append
  // vs. start a fresh assistant bubble on the next event.
  streaming?: boolean
}

/** CloudState is the live chat drawer state in cloud mode. `status`
 *  drives the UI:
 *    idle       — drawer closed, or open with no active session yet.
 *    connecting — POSTed /sessions, waiting for the response.
 *    streaming  — SSE attached, assistant currently emitting tokens.
 *    closed     — server emitted `end` (turn finished); ready for
 *                 next user message.
 *    error      — session failed; see the last message for details. */
export interface CloudState {
  sessionId?: string
  repo?: string
  messages: ChatMessage[]
  status: 'idle' | 'connecting' | 'streaming' | 'closed' | 'error'
}

/** HostMode: undefined while the auth probe is in flight. */
export type HostMode = 'cloud' | 'local' | undefined

interface ChatContextValue {
  /** True when the drawer should be visible. In cloud mode this is
   *  set as soon as open() begins (so the user sees a "connecting…"
   *  state); in local mode it only goes true if /api/chat/spawn
   *  fails (otherwise the OS terminal carries the conversation). */
  isOpen: boolean
  mode: 'cloud' | 'local' | 'spawn-error'
  hostMode: HostMode
  cloud: CloudState
  spawnError: SpawnError | null
  /** open(target) — entry point used by ReportIssueButton, IssueList.
   *  Behavior depends on hostMode. Resolves once the request has been
   *  dispatched (cloud) or the spawn attempt completed (local). */
  open: (target: ChatTarget) => Promise<void>
  /** sendMessage — only meaningful in cloud mode while a session is
   *  open. Sends a follow-up turn. In local mode this is a no-op. */
  sendMessage: (text: string) => Promise<void>
  close: () => void
}

const FEEDBACK_REPO = 'zhoushoujianwork/clawflow'

const FEEDBACK_PROMPT =
  "I'd like to report feedback or a bug about ClawFlow. Please help me draft a clear GitHub issue (title + body) targeting zhoushoujianwork/clawflow. Ask me follow-up questions until you have enough detail, then file the issue."

const ChatContext = createContext<ChatContextValue>({
  isOpen: false,
  mode: 'cloud',
  hostMode: undefined,
  cloud: { messages: [], status: 'idle' },
  spawnError: null,
  open: async () => {},
  sendMessage: async () => {},
  close: () => {},
})

export function useChatDrawer() {
  return useContext(ChatContext)
}

// Lightweight id generator — only used to key React lists. crypto.randomUUID
// would be cleaner but it's optional on older Safari and we don't want a
// polyfill just for list keys.
function makeId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

// LOCAL_AGENT_CANDIDATES is the short list of localhost URLs the cloud
// bundle probes on mount to detect a co-located `clawflow web`. The
// first 200-OK response (with CORS allow — see internal/api/
// cors_chat_agent.go) wins; chat.open() then targets that URL's
// /api/chat/spawn instead of the in-browser cloud drawer.
//
// 8090 is the `clawflow web --port` default; 7070 is a common
// override. If a user binds elsewhere they can still use the drawer.
const LOCAL_AGENT_CANDIDATES = [
  'http://127.0.0.1:8090',
  'http://127.0.0.1:7070',
]

// probeLocalAgent races the GET /api/version probes against the
// candidate list and resolves to the first reachable URL, or null
// when nothing answers within 800 ms (typical localhost RTT is sub-
// 10 ms; the cap is just a guard against blocking the cloud drawer
// behind a slow probe).
async function probeLocalAgent(): Promise<string | null> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 800)
  try {
    const results = await Promise.allSettled(
      LOCAL_AGENT_CANDIDATES.map(async url => {
        const r = await fetch(`${url}/api/version`, {
          mode: 'cors',
          signal: controller.signal,
        })
        if (!r.ok) throw new Error(`probe ${url}: ${r.status}`)
        return url
      }),
    )
    for (const res of results) {
      if (res.status === 'fulfilled') return res.value
    }
    return null
  } finally {
    clearTimeout(timeout)
  }
}

export function ChatProvider({ children }: { children: ReactNode }) {
  const [hostMode, setHostMode] = useState<HostMode>(undefined)
  // agentURL is the prefix used by openLocal when hostMode === 'local'.
  // Empty string = same-origin local web bundle (chatContext is on the
  // local server itself). A full http://127.0.0.1:NNNN URL = a remote
  // cloud bundle that discovered a co-located local-web via the
  // localhost probe above.
  const [agentURL, setAgentURL] = useState<string>('')
  const [spawnError, setSpawnError] = useState<SpawnError | null>(null)
  const [cloud, setCloud] = useState<CloudState>({ messages: [], status: 'idle' })
  const [cloudOpen, setCloudOpen] = useState(false)

  // Active SSE teardown. Kept in a ref because the cleanup may run
  // from inside event callbacks (no re-render cycle to depend on).
  const teardownRef = useRef<(() => void) | null>(null)
  // The id of the assistant message currently being streamed into.
  // Reset to null between turns so the next `output` event starts a
  // fresh assistant bubble.
  const activeAssistantIdRef = useRef<string | null>(null)

  // Probe host mode on mount. Two probes race:
  //
  //   1. fetchAuthMe — 'no-cloud' (404) means we're on a local-web
  //      bundle, so hostMode is unambiguously 'local'.
  //   2. probeLocalAgent — looks for a `clawflow web` on common
  //      localhost ports. Only succeeds when origin === user's
  //      configured cloud URL (CORS gate). When it succeeds the
  //      remote cloud bundle gets to spawn a native terminal here
  //      via /api/chat/spawn instead of falling back to the in-
  //      browser drawer (the user's stated preference).
  //
  // Local-web bundle always gets hostMode='local' with agentURL=''
  // (relative paths). Cloud bundle gets 'local' + the probed agent
  // URL when reachable, else 'cloud' for the drawer fallback.
  useEffect(() => {
    let cancelled = false
    ;(async () => {
      const me = await fetchAuthMe().catch(() => ({ kind: 'no-cloud' as const }))
      if (cancelled) return
      if (me.kind === 'no-cloud') {
        setHostMode('local')
        setAgentURL('')
        return
      }
      // Remote bundle — try the localhost agent. If found, route
      // chat through it; otherwise stay in cloud (drawer) mode.
      const agent = await probeLocalAgent()
      if (cancelled) return
      if (agent) {
        setAgentURL(agent)
        setHostMode('local')
      } else {
        setHostMode('cloud')
      }
    })().catch(() => {
      if (!cancelled) setHostMode('local')
    })
    return () => {
      cancelled = true
    }
  }, [])

  // ---------- cloud-mode helpers ----------

  const handleEvent = useCallback((ev: ChatEvent) => {
    setCloud(prev => {
      if (ev.type === 'output') {
        const messages = [...prev.messages]
        const id = activeAssistantIdRef.current
        const lastIdx = id ? messages.findIndex(m => m.id === id) : -1
        if (lastIdx >= 0) {
          messages[lastIdx] = {
            ...messages[lastIdx],
            text: messages[lastIdx].text + ev.text,
          }
        } else {
          const newId = makeId('asst')
          activeAssistantIdRef.current = newId
          messages.push({
            id: newId,
            role: 'assistant',
            text: ev.text,
            streaming: true,
          })
        }
        return { ...prev, messages, status: 'streaming' }
      }
      if (ev.type === 'stderr') {
        // stderr is appended into the active assistant bubble in
        // brackets so the user sees the diagnostic without us having
        // to render a separate stream. This matches how the terminal
        // would interleave it.
        const messages = [...prev.messages]
        const id = activeAssistantIdRef.current
        const lastIdx = id ? messages.findIndex(m => m.id === id) : -1
        const chunk = ev.text
        if (lastIdx >= 0) {
          messages[lastIdx] = {
            ...messages[lastIdx],
            text: messages[lastIdx].text + chunk,
          }
        } else {
          const newId = makeId('asst')
          activeAssistantIdRef.current = newId
          messages.push({ id: newId, role: 'assistant', text: chunk, streaming: true })
        }
        return { ...prev, messages }
      }
      if (ev.type === 'end') {
        const messages = prev.messages.map(m =>
          m.id === activeAssistantIdRef.current ? { ...m, streaming: false } : m,
        )
        activeAssistantIdRef.current = null
        return { ...prev, messages, status: 'closed' }
      }
      if (ev.type === 'error') {
        const messages = [...prev.messages]
        messages.push({
          id: makeId('err'),
          role: 'error',
          text: ev.text || 'chat error',
        })
        activeAssistantIdRef.current = null
        return { ...prev, messages, status: 'error' }
      }
      return prev
    })
  }, [])

  const handleSubscribeError = useCallback((err: Error) => {
    setCloud(prev => ({
      ...prev,
      status: 'error',
      messages: [...prev.messages, { id: makeId('err'), role: 'error', text: err.message }],
    }))
  }, [])

  // attachStream — wraps subscribeToChat and stores the teardown on
  // the ref. Calling it replaces any prior subscription.
  const attachStream = useCallback(
    (sessionId: string) => {
      if (teardownRef.current) {
        teardownRef.current()
        teardownRef.current = null
      }
      teardownRef.current = subscribeToChat(sessionId, handleEvent, handleSubscribeError)
    },
    [handleEvent, handleSubscribeError],
  )

  // ---------- open ----------

  const openLocal = useCallback(async (t: ChatTarget) => {
    setSpawnError(null)
    try {
      // agentURL is empty string for same-origin (local-web bundle)
      // and a full http://127.0.0.1:NNNN URL when this is a cloud
      // bundle that found a co-located local-web during the probe.
      const r = await fetch(`${agentURL}/api/chat/spawn`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(t),
      })
      const d = await r.json().catch(() => ({}))
      if (r.ok && d.status === 'ok') {
        // Native terminal launched — nothing to surface in-browser.
        return
      }
      setSpawnError({
        target: t,
        error: d.error || `HTTP ${r.status}`,
        command: d.command,
        hint: d.hint,
      })
    } catch (e) {
      setSpawnError({
        target: t,
        error: String((e as Error).message || e),
      })
    }
  }, [agentURL])

  const openCloud = useCallback(
    async (t: ChatTarget) => {
      // Feedback in cloud mode: target the clawflow repo and prime
      // the conversation with a feedback intent.
      const repo = t.feedback ? FEEDBACK_REPO : t.repo
      if (!repo) {
        setCloud({
          messages: [
            {
              id: makeId('err'),
              role: 'error',
              text: 'No repo specified for cloud chat.',
            },
          ],
          status: 'error',
        })
        setCloudOpen(true)
        return
      }

      const primer = t.feedback
        ? FEEDBACK_PROMPT
        : t.issue
        ? `Let's discuss issue #${t.issue} in ${repo}.`
        : `Let's work on ${repo}.`

      const userMsg: ChatMessage = {
        id: makeId('user'),
        role: 'user',
        text: primer,
      }

      // Reset state and show drawer immediately so the user sees the
      // connecting spinner. Drop any prior session — only one chat
      // at a time.
      if (teardownRef.current) {
        teardownRef.current()
        teardownRef.current = null
      }
      activeAssistantIdRef.current = null
      setCloud({
        sessionId: undefined,
        repo,
        messages: [userMsg],
        status: 'connecting',
      })
      setCloudOpen(true)

      try {
        const session = await createChatSession(repo, primer)
        setCloud(prev => ({
          ...prev,
          sessionId: session.id,
          repo: session.repo,
          status: 'streaming',
        }))
        attachStream(session.id)
      } catch (e) {
        const msg = (e as Error).message || String(e)
        setCloud(prev => ({
          ...prev,
          status: 'error',
          messages: [
            ...prev.messages,
            { id: makeId('err'), role: 'error', text: `failed to start chat: ${msg}` },
          ],
        }))
      }
    },
    [attachStream],
  )

  const open = useCallback(
    async (t: ChatTarget) => {
      // Wait one tick for hostMode if we're called before the probe
      // returns. In practice the probe resolves in <50ms, but the
      // ReportIssueButton click could theoretically race it.
      let mode = hostMode
      if (mode === undefined) {
        try {
          const r = await fetchAuthMe()
          mode = r.kind === 'no-cloud' ? 'local' : 'cloud'
          setHostMode(mode)
        } catch {
          mode = 'local'
          setHostMode('local')
        }
      }
      if (mode === 'cloud') {
        await openCloud(t)
      } else {
        await openLocal(t)
      }
    },
    [hostMode, openCloud, openLocal],
  )

  // ---------- sendMessage ----------

  const sendMessage = useCallback(
    async (text: string) => {
      const trimmed = text.trim()
      if (!trimmed) return
      const sessionId = cloud.sessionId
      if (!sessionId) return

      const userMsg: ChatMessage = {
        id: makeId('user'),
        role: 'user',
        text: trimmed,
      }
      activeAssistantIdRef.current = null
      setCloud(prev => ({
        ...prev,
        messages: [...prev.messages, userMsg],
        status: 'connecting',
      }))

      try {
        await sendChatMessage(sessionId, trimmed)
        // The server will cancel the previous procCtx (emitting `end`
        // on the current stream); our EventSource fires its onerror
        // and we need to reattach to pick up the new turn.
        setCloud(prev => ({ ...prev, status: 'streaming' }))
        attachStream(sessionId)
      } catch (e) {
        const msg = (e as Error).message || String(e)
        setCloud(prev => ({
          ...prev,
          status: 'error',
          messages: [
            ...prev.messages,
            { id: makeId('err'), role: 'error', text: `send failed: ${msg}` },
          ],
        }))
      }
    },
    [cloud.sessionId, attachStream],
  )

  // ---------- close ----------

  const close = useCallback(() => {
    // Cloud: tear down the SSE, best-effort delete the session, and
    // reset the drawer. We do NOT await the delete so the UI feels
    // instant; if it fails the GC sweep will catch the orphan.
    if (teardownRef.current) {
      teardownRef.current()
      teardownRef.current = null
    }
    activeAssistantIdRef.current = null
    const sid = cloud.sessionId
    if (sid) {
      void deleteChatSession(sid).catch(() => {})
    }
    setCloud({ messages: [], status: 'idle' })
    setCloudOpen(false)
    // Local: also dismiss the spawn-error fallback.
    setSpawnError(null)
  }, [cloud.sessionId])

  // ---------- derived state ----------

  // Drawer is open if:
  //   • cloud mode and the user opened a session, OR
  //   • local mode and a spawn failed.
  const isOpen = cloudOpen || spawnError !== null
  // `mode` tells the drawer which UI to render. spawn-error wins over
  // cloud because if we somehow have both, the spawn-error is the
  // immediate failure to acknowledge.
  const drawerMode: 'cloud' | 'local' | 'spawn-error' = spawnError
    ? 'spawn-error'
    : 'cloud'

  // Clean up SSE on unmount.
  useEffect(() => {
    return () => {
      if (teardownRef.current) {
        teardownRef.current()
        teardownRef.current = null
      }
    }
  }, [])

  return (
    <ChatContext.Provider
      value={{
        isOpen,
        mode: drawerMode,
        hostMode,
        cloud,
        spawnError,
        open,
        sendMessage,
        close,
      }}
    >
      {children}
    </ChatContext.Provider>
  )
}
