// ChatDrawer — slide-in right-side panel that hosts the cloud chat UI
// and, when on a local clawflow web bundle, doubles as the spawn-error
// fallback for /api/chat/spawn.
//
// Cloud mode (the primary path now):
//   • Repo name + close button in the header.
//   • Scrollable transcript with markdown-rendered assistant bubbles
//     and right-aligned user bubbles.
//   • Bottom textarea + Send button. Cmd/Ctrl+Enter sends, plain Enter
//     inserts a newline (matches Claude.ai conventions).
//   • A "streaming…" hint at the foot of the transcript while the
//     server is still emitting tokens.
//
// Local spawn-error mode (legacy fallback for `clawflow web` users
// whose terminal emulator couldn't be launched):
//   • Centered card with the error message and a "Run this in any
//     terminal" command snippet.
//
// The `WS_CLOSE_*` constants below stay exported because Terminal.tsx
// still imports them — internal/pty/server.go's WS path is mounted on
// local clawflow web and uses those codes to distinguish destroy vs.
// collapse intents on tab close. Removing them would break unrelated
// XTerminal consumers.

import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import { AlertCircle, Send, X } from 'lucide-react'
import { useChatDrawer, type ChatMessage } from '../lib/chatContext'
import { Markdown } from './Markdown'

export const WS_CLOSE_DESTROY = 4001
export const WS_CLOSE_COLLAPSE = 4000
export type CloseIntent = 'destroy' | 'collapse'

export function ChatDrawer() {
  const { isOpen, mode, cloud, spawnError, sendMessage, close } = useChatDrawer()

  // Escape closes whichever drawer flavor is showing.
  useEffect(() => {
    if (!isOpen) return
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [isOpen, close])

  if (!isOpen) return null

  if (mode === 'spawn-error' && spawnError) {
    return <SpawnErrorPanel close={close} error={spawnError} />
  }

  // Cloud mode — the rich drawer.
  return (
    <CloudDrawer
      repo={cloud.repo}
      messages={cloud.messages}
      status={cloud.status}
      onSend={sendMessage}
      onClose={close}
    />
  )
}

// ---------------- Cloud drawer ----------------

function CloudDrawer({
  repo,
  messages,
  status,
  onSend,
  onClose,
}: {
  repo?: string
  messages: ChatMessage[]
  status: 'idle' | 'connecting' | 'streaming' | 'closed' | 'error'
  onSend: (text: string) => Promise<void>
  onClose: () => void
}) {
  const [input, setInput] = useState('')
  const listRef = useRef<HTMLDivElement>(null)

  // Autoscroll: after every messages change (including streamed
  // chunks), pin to the bottom. Using useLayoutEffect avoids the
  // visible jump between paint and scroll.
  useLayoutEffect(() => {
    const el = listRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
  }, [messages, status])

  const canSend = status !== 'connecting' && input.trim().length > 0

  const submit = async () => {
    if (!canSend) return
    const text = input
    setInput('')
    await onSend(text)
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Cmd/Ctrl+Enter → send. Plain Enter inserts a newline (lets the
    // user paste multi-line context without a stray submit).
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      void submit()
    }
  }

  return (
    <>
      <div
        className="fixed inset-0 z-40 bg-black/30"
        onClick={onClose}
        aria-hidden="true"
      />
      <aside
        className="fixed top-0 right-0 z-50 h-screen w-full sm:w-[480px] flex flex-col border-l shadow-2xl"
        style={{
          background: 'hsl(var(--bg-primary))',
          borderColor: 'hsl(var(--border))',
        }}
        role="dialog"
        aria-label="Chat with Claude"
      >
        {/* Header */}
        <div
          className="flex items-center justify-between px-4 h-12 border-b shrink-0"
          style={{ borderColor: 'hsl(var(--border))' }}
        >
          <div className="flex flex-col min-w-0">
            <div
              className="text-sm font-semibold truncate"
              style={{ color: 'hsl(var(--text-high))' }}
            >
              Chat with Claude
            </div>
            <div
              className="text-[11px] truncate"
              style={{ color: 'hsl(var(--text-low))' }}
            >
              {repo || '—'}
              {status === 'connecting' && ' · connecting…'}
              {status === 'streaming' && ' · streaming'}
              {status === 'closed' && ' · idle'}
              {status === 'error' && ' · error'}
            </div>
          </div>
          <button
            onClick={onClose}
            className="w-7 h-7 flex items-center justify-center rounded-sm hover:opacity-80"
            style={{ color: 'hsl(var(--text-low))' }}
            aria-label="Close chat"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Transcript */}
        <div
          ref={listRef}
          className="flex-1 overflow-y-auto px-4 py-4 space-y-3"
          style={{ background: 'hsl(var(--bg-primary))' }}
        >
          {messages.length === 0 && status === 'idle' && (
            <div
              className="text-xs text-center mt-8"
              style={{ color: 'hsl(var(--text-low))' }}
            >
              Type below to start a conversation.
            </div>
          )}
          {messages.map(m => (
            <Bubble key={m.id} msg={m} />
          ))}
          {status === 'streaming' && (
            <div
              className="text-xs italic pl-1"
              style={{ color: 'hsl(var(--text-low))' }}
            >
              Claude is typing…
            </div>
          )}
        </div>

        {/* Composer */}
        <div
          className="border-t px-3 py-3 shrink-0"
          style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-primary))' }}
        >
          <div
            className="flex items-end gap-2 rounded-md border px-2 py-1.5"
            style={{
              background: 'hsl(var(--bg-elevated))',
              borderColor: 'hsl(var(--border))',
            }}
          >
            <textarea
              value={input}
              onChange={e => setInput(e.target.value)}
              onKeyDown={onKeyDown}
              rows={2}
              placeholder="Message Claude…  (⌘/Ctrl+Enter to send)"
              className="flex-1 resize-none bg-transparent outline-none text-sm placeholder:opacity-60 max-h-40"
              style={{ color: 'hsl(var(--text-high))' }}
              disabled={status === 'connecting'}
            />
            <button
              onClick={() => { void submit() }}
              disabled={!canSend}
              className="w-7 h-7 flex items-center justify-center rounded-sm transition-opacity disabled:opacity-40 hover:opacity-80"
              style={{ background: 'hsl(var(--brand))', color: 'white' }}
              aria-label="Send message"
              title="Send (⌘/Ctrl+Enter)"
            >
              <Send className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
      </aside>
    </>
  )
}

function Bubble({ msg }: { msg: ChatMessage }) {
  if (msg.role === 'user') {
    return (
      <div className="flex justify-end">
        <div
          className="max-w-[85%] rounded-lg px-3 py-2 text-sm whitespace-pre-wrap break-words"
          style={{ background: 'hsl(var(--brand))', color: 'white' }}
        >
          {msg.text}
        </div>
      </div>
    )
  }
  if (msg.role === 'error') {
    return (
      <div className="flex justify-start">
        <div
          className="max-w-[85%] rounded-lg px-3 py-2 text-xs flex items-start gap-2"
          style={{
            background: 'hsl(var(--bg-elevated))',
            border: '1px solid hsl(var(--border))',
            color: 'hsl(var(--text-high))',
          }}
        >
          <AlertCircle className="w-3.5 h-3.5 text-red-500 shrink-0 mt-0.5" />
          <span className="whitespace-pre-wrap break-words">{msg.text}</span>
        </div>
      </div>
    )
  }
  // assistant
  return (
    <div className="flex justify-start">
      <div
        className="max-w-[92%] rounded-lg px-3 py-2"
        style={{
          background: 'hsl(var(--bg-elevated))',
          border: '1px solid hsl(var(--border))',
        }}
      >
        {/* While streaming the partial text often isn't valid markdown
            yet (open code fence, half a list bullet). react-markdown
            renders it as plain text in that case, which is fine.
            We feed everything through Markdown for the final form. */}
        <Markdown>{msg.text || ' '}</Markdown>
      </div>
    </div>
  )
}

// ---------------- Spawn-error fallback (local mode) ----------------

function SpawnErrorPanel({
  close,
  error,
}: {
  close: () => void
  error: {
    error: string
    command?: string
    hint?: string
  }
}) {
  return (
    <>
      <div
        className="fixed inset-0 z-40 bg-black/30"
        onClick={close}
        aria-hidden="true"
      />
      <div
        className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 z-50 max-w-lg w-[calc(100%-2rem)] rounded-xl border shadow-2xl"
        style={{
          background: 'hsl(var(--bg-primary))',
          borderColor: 'hsl(var(--border))',
        }}
      >
        <div
          className="flex items-center justify-between px-4 h-11 border-b"
          style={{ borderColor: 'hsl(var(--border))' }}
        >
          <div
            className="flex items-center gap-2 text-sm font-semibold"
            style={{ color: 'hsl(var(--text-high))' }}
          >
            <AlertCircle className="w-4 h-4 text-red-500" />
            Couldn't open a terminal
          </div>
          <button
            onClick={close}
            className="w-6 h-6 flex items-center justify-center rounded-sm hover:opacity-80"
            style={{ color: 'hsl(var(--text-low))' }}
            aria-label="Dismiss"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>

        <div className="px-4 py-4 space-y-3">
          <div className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
            {error.error}
          </div>
          {error.command && (
            <>
              <div className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                {error.hint || 'Run this in any terminal:'}
              </div>
              <code
                className="block font-mono text-[11px] px-3 py-2 rounded select-all break-all"
                style={{
                  background: 'hsl(var(--bg-elevated))',
                  color: 'hsl(var(--text-high))',
                }}
              >
                {error.command}
              </code>
            </>
          )}
        </div>
      </div>
    </>
  )
}
