import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'

interface ChatTarget {
  repo?: string
  issue?: number
  model?: string
  mode?: 'issue' | 'edit'
  project?: string
  action?: 'generate' | 'chat'
  feedback?: boolean
}

// SpawnError describes a chat-spawn failure. The drawer opens
// only when this is set; success cases leave the drawer hidden
// because the user's native terminal app is already in front of
// them with the chat.
interface SpawnError {
  target: ChatTarget
  error: string
  command?: string
  hint?: string
}

interface ChatContextValue {
  // True only when a spawn failed and we're showing the fallback
  // drawer with the manual command. Successful spawns set this to
  // false and let the OS terminal carry the conversation.
  isOpen: boolean
  spawnError: SpawnError | null
  // open() fires /api/chat/spawn directly. The drawer is only
  // surfaced if the spawn fails. On success: the user sees their
  // OS terminal pop up running clawflow chat — no drawer at all.
  open: (target: ChatTarget) => Promise<void>
  close: () => void
}

const ChatContext = createContext<ChatContextValue>({
  isOpen: false,
  spawnError: null,
  open: async () => {},
  close: () => {},
})

export function useChatDrawer() {
  return useContext(ChatContext)
}

export function ChatProvider({ children }: { children: ReactNode }) {
  const [spawnError, setSpawnError] = useState<SpawnError | null>(null)

  const open = useCallback(async (t: ChatTarget) => {
    setSpawnError(null)
    try {
      const r = await fetch('/api/chat/spawn', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(t),
      })
      const d = await r.json().catch(() => ({}))
      if (r.ok && d.status === 'ok') {
        // Success — terminal is opening in the user's native app.
        // Nothing else to do; no drawer to show.
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
  }, [])

  const close = useCallback(() => {
    setSpawnError(null)
  }, [])

  return (
    <ChatContext.Provider value={{ isOpen: spawnError !== null, spawnError, open, close }}>
      {children}
    </ChatContext.Provider>
  )
}
