import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'

interface ChatTarget {
  repo: string
  issue?: number
  model?: string
}

interface ChatContextValue {
  isOpen: boolean
  target: ChatTarget | null
  open: (target: ChatTarget) => void
  close: () => void
}

const ChatContext = createContext<ChatContextValue>({
  isOpen: false,
  target: null,
  open: () => {},
  close: () => {},
})

export function useChatDrawer() {
  return useContext(ChatContext)
}

export function ChatProvider({ children }: { children: ReactNode }) {
  const [isOpen, setIsOpen] = useState(false)
  const [target, setTarget] = useState<ChatTarget | null>(null)

  const open = useCallback((t: ChatTarget) => {
    setTarget(t)
    setIsOpen(true)
  }, [])

  const close = useCallback(() => {
    setIsOpen(false)
  }, [])

  return (
    <ChatContext.Provider value={{ isOpen, target, open, close }}>
      {children}
    </ChatContext.Provider>
  )
}
