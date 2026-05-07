import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Send, X, Loader2, Square, AlertCircle } from 'lucide-react'
import { cn } from '../lib/utils'
import { streamSSE } from '../lib/sseClient'
import { extractLastFencedBlock } from '../lib/extractFencedBlock'
import { Markdown } from './Markdown'

// ChatPanel — embedded streaming chat used to iterate on a project's
// goals.md / context.md docs. The user types, the backend streams claude
// stream-json over SSE, the assistant's text accumulates into a bubble on
// the left, and any fenced code block tagged with `draftTag` (e.g.
// "goals.md") is extracted and shown live as the right-pane "draft preview"
// the user can save.

export interface ChatPanelProps {
  kind: 'goals' | 'context'
  project: string
  draftTag: string
  initialDraft?: string
  onSave: (content: string) => Promise<void>
  onClose: () => void
}

type Role = 'user' | 'assistant' | 'error'

interface Message {
  role: Role
  text: string
}

// Claude stream-json shapes we care about. Everything else (tool_use,
// system, ping) is ignored — v1 only renders assistant text.
interface ContentBlock {
  type: string
  text?: string
}
interface Delta {
  type: string
  text?: string
}
interface StreamJsonEvent {
  type?: string
  role?: string
  content?: string | ContentBlock[]
  content_block?: ContentBlock
  delta?: Delta
  message?: { role?: string; content?: ContentBlock[] }
}

interface ClawflowSessionEvent {
  type: 'clawflow_session'
  session_id: string
}
interface ClawflowErrorEvent {
  type: 'clawflow_error'
  error: string
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}

// extractAssistantText pulls assistant-visible text out of one parsed
// stream-json event. Handles the four shapes the runs page also handles:
// content_block_start/text, content_block_delta/text_delta, top-level
// assistant role with content (string|array), and message.role=assistant.
function extractAssistantText(ev: StreamJsonEvent): string {
  let out = ''

  if (ev.type === 'content_block_start' && ev.content_block?.type === 'text') {
    out += ev.content_block.text ?? ''
  }
  if (ev.type === 'content_block_delta' && ev.delta?.type === 'text_delta') {
    out += ev.delta.text ?? ''
  }
  if (ev.role === 'assistant' && ev.content !== undefined) {
    if (typeof ev.content === 'string') {
      out += ev.content
    } else if (Array.isArray(ev.content)) {
      for (const c of ev.content) {
        if (c.type === 'text' && c.text) out += c.text
      }
    }
  }
  if (ev.message?.role === 'assistant' && Array.isArray(ev.message.content)) {
    for (const c of ev.message.content) {
      if (c.type === 'text' && c.text) out += c.text
    }
  }
  return out
}

export function ChatPanel(props: ChatPanelProps): JSX.Element {
  const { kind, project, draftTag, initialDraft, onSave, onClose } = props

  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [saving, setSaving] = useState(false)
  // assistantBuf is the text accumulating from the in-flight assistant turn.
  // Kept in state (not just ref) so the UI re-renders deltas in real time.
  const [assistantBuf, setAssistantBuf] = useState('')

  // sessionId + the streaming abort controller live in refs so the SSE
  // handlers (which capture closure values once) always see the freshest
  // value without re-subscribing.
  const sessionIdRef = useRef<string>('')
  const abortRef = useRef<AbortController | null>(null)
  const assistantBufRef = useRef('')
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const historyRef = useRef<HTMLDivElement>(null)

  // Aggregate every assistant bubble (finalized + in-flight) so the draft
  // preview can pick up a fenced block no matter which turn produced it.
  const aggregatedAssistantText = useMemo(() => {
    const parts: string[] = []
    for (const m of messages) {
      if (m.role === 'assistant') parts.push(m.text)
    }
    if (assistantBuf) parts.push(assistantBuf)
    return parts.join('\n\n')
  }, [messages, assistantBuf])

  const extractedDraft = useMemo(
    () => extractLastFencedBlock(aggregatedAssistantText, draftTag),
    [aggregatedAssistantText, draftTag],
  )

  const currentDraft = extractedDraft || initialDraft || ''
  const draftDirty = !!extractedDraft && extractedDraft !== (initialDraft || '')

  // Auto-scroll the chat history to the latest message as content streams
  // in. We always pin to bottom — the textarea is short, and the user can
  // scroll back up freely; we don't fight them on any subsequent renders
  // because the next delta will simply re-pin.
  useEffect(() => {
    const el = historyRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [messages, assistantBuf])

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const handleClose = useCallback(() => {
    if (draftDirty) {
      const ok = window.confirm('You have an unsaved draft. Close anyway?')
      if (!ok) return
    }
    abortRef.current?.abort()
    onClose()
  }, [draftDirty, onClose])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') handleClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [handleClose])

  const finalizeAssistant = useCallback(() => {
    const buf = assistantBufRef.current
    assistantBufRef.current = ''
    setAssistantBuf('')
    if (buf.trim().length > 0) {
      setMessages(prev => [...prev, { role: 'assistant', text: buf }])
    }
    setStreaming(false)
    abortRef.current = null
    // Re-focus the input so the user can keep typing without grabbing the
    // mouse; setTimeout defers past the disabled→enabled flip.
    setTimeout(() => inputRef.current?.focus(), 0)
  }, [])

  const send = useCallback(async () => {
    const text = input.trim()
    if (!text || streaming) return

    setInput('')
    setMessages(prev => [...prev, { role: 'user', text }])
    setStreaming(true)
    assistantBufRef.current = ''
    setAssistantBuf('')

    const ctrl = new AbortController()
    abortRef.current = ctrl

    let errored = false

    await streamSSE(
      {
        url: '/api/chat/stream',
        body: {
          kind,
          project,
          session_id: sessionIdRef.current,
          message: text,
        },
        signal: ctrl.signal,
      },
      {
        onEvent: raw => {
          let parsed: unknown
          try {
            parsed = JSON.parse(raw)
          } catch {
            return
          }
          if (!isObject(parsed)) return

          // Clawflow's own envelope events (session id + error) come through
          // the same stream as the claude payloads.
          if (parsed.type === 'clawflow_session' && typeof parsed.session_id === 'string') {
            const ev = parsed as unknown as ClawflowSessionEvent
            sessionIdRef.current = ev.session_id
            return
          }
          if (parsed.type === 'clawflow_error' && typeof parsed.error === 'string') {
            const ev = parsed as unknown as ClawflowErrorEvent
            errored = true
            setMessages(prev => [...prev, { role: 'error', text: ev.error }])
            return
          }

          const delta = extractAssistantText(parsed as StreamJsonEvent)
          if (delta) {
            assistantBufRef.current += delta
            setAssistantBuf(assistantBufRef.current)
          }
        },
        onError: err => {
          errored = true
          setMessages(prev => [...prev, { role: 'error', text: err.message }])
        },
        onDone: () => {
          // finalizeAssistant fires inside the synchronous flow below
          // regardless of whether [DONE] arrived or the stream just closed.
          // Marking errored already inserts the error bubble; finalize still
          // commits any partial text we did receive before the failure.
          void errored
        },
      },
    )

    finalizeAssistant()
  }, [input, streaming, kind, project, finalizeAssistant])

  const cancel = useCallback(() => {
    abortRef.current?.abort()
    // streamSSE's onDone fires synchronously after abort; finalize is what
    // actually commits the partial bubble and re-enables input.
    finalizeAssistant()
  }, [finalizeAssistant])

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      void send()
    }
  }

  const handleSave = useCallback(async () => {
    if (!currentDraft || streaming || saving) return
    setSaving(true)
    try {
      await onSave(currentDraft)
    } finally {
      setSaving(false)
    }
  }, [currentDraft, streaming, saving, onSave])

  const saveDisabled = saving || streaming || !currentDraft

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm"
      onClick={e => {
        if (e.target === e.currentTarget) handleClose()
      }}
    >
      <div className="relative w-full md:w-[90vw] lg:w-[80vw] max-w-6xl h-full md:h-[85vh] md:mx-4 bg-card border border-border md:rounded-xl shadow-2xl flex flex-col overflow-hidden">
        <div className="flex items-center justify-between px-5 py-3 border-b border-border shrink-0">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-foreground">
              Chat · {kind === 'goals' ? 'Goals' : 'Context'}
            </h2>
            <span className="text-xs text-muted-foreground font-mono">{project}</span>
          </div>
          <button
            type="button"
            onClick={handleClose}
            className="text-muted-foreground hover:text-foreground transition-colors"
            aria-label="Close"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="flex-1 flex flex-col md:flex-row overflow-hidden">
          {/* LEFT: chat */}
          <div className="flex-1 flex flex-col border-b md:border-b-0 md:border-r border-border min-h-0">
            <div ref={historyRef} className="flex-1 overflow-y-auto px-5 py-4 space-y-3">
              {messages.length === 0 && !assistantBuf && (
                <div className="text-xs text-muted-foreground">
                  Describe what you want in {draftTag}. The assistant will draft it and you can save when it looks right.
                </div>
              )}
              {messages.map((m, i) => (
                <Bubble key={i} role={m.role} text={m.text} />
              ))}
              {assistantBuf && <Bubble role="assistant" text={assistantBuf} streaming />}
              {streaming && !assistantBuf && (
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                  Thinking…
                </div>
              )}
            </div>

            <div className="border-t border-border p-3 shrink-0">
              <div className="flex items-end gap-2">
                <textarea
                  ref={inputRef}
                  value={input}
                  onChange={e => setInput(e.target.value)}
                  onKeyDown={onKeyDown}
                  placeholder={streaming ? 'Streaming…' : 'Message (Enter to send, Shift+Enter for newline)'}
                  rows={2}
                  disabled={streaming}
                  className="flex-1 resize-none bg-secondary/40 border border-border rounded-md px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-60"
                />
                {streaming ? (
                  <button
                    type="button"
                    onClick={cancel}
                    className="inline-flex items-center gap-1 px-3 py-2 rounded-md border border-border text-xs font-medium text-foreground hover:bg-secondary transition-colors"
                  >
                    <Square className="w-3.5 h-3.5" />
                    Cancel
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => void send()}
                    disabled={!input.trim()}
                    className="inline-flex items-center gap-1 px-3 py-2 rounded-md bg-foreground text-background text-xs font-medium hover:opacity-90 transition-opacity disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    <Send className="w-3.5 h-3.5" />
                    Send
                  </button>
                )}
              </div>
            </div>
          </div>

          {/* RIGHT: draft preview */}
          <div className="md:w-[44%] lg:w-[40%] flex flex-col min-h-0">
            <div className="flex items-center justify-between px-5 py-2 border-b border-border shrink-0">
              <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wide">
                Draft · {draftTag}
              </span>
              {extractedDraft && (
                <span className="text-[10px] text-muted-foreground">
                  {extractedDraft.split('\n').length} lines
                </span>
              )}
            </div>
            <div className="flex-1 overflow-auto px-5 py-3">
              {currentDraft ? (
                <pre className="text-[12px] leading-relaxed font-mono whitespace-pre-wrap text-foreground">
                  {currentDraft}
                </pre>
              ) : (
                <div className="text-xs text-muted-foreground italic">(no draft yet)</div>
              )}
            </div>
            <div className="border-t border-border p-3 shrink-0 flex items-center justify-end gap-2">
              {draftDirty && (
                <span className="text-[11px] text-amber-600 dark:text-amber-400">unsaved changes</span>
              )}
              <button
                type="button"
                onClick={() => void handleSave()}
                disabled={saveDisabled}
                className={cn(
                  'inline-flex items-center gap-1 px-3 py-1.5 rounded-md text-xs font-medium transition-opacity',
                  saveDisabled
                    ? 'bg-secondary text-muted-foreground cursor-not-allowed'
                    : 'bg-foreground text-background hover:opacity-90',
                )}
              >
                {saving && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
                Save
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function Bubble({ role, text, streaming }: { role: Role; text: string; streaming?: boolean }) {
  if (role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] bg-foreground text-background rounded-lg px-3 py-2 text-sm whitespace-pre-wrap">
          {text}
        </div>
      </div>
    )
  }
  if (role === 'error') {
    return (
      <div className="flex items-start gap-2 bg-red-50 border border-red-200 dark:bg-red-950/30 dark:border-red-900 rounded-lg px-3 py-2">
        <AlertCircle className="w-4 h-4 text-red-600 dark:text-red-400 shrink-0 mt-0.5" />
        <div className="text-xs text-red-700 dark:text-red-300 whitespace-pre-wrap">{text}</div>
      </div>
    )
  }
  return (
    <div className="flex justify-start">
      <div className="max-w-[92%] bg-secondary/50 border border-border rounded-lg px-3 py-2">
        <Markdown>{text}</Markdown>
        {streaming && (
          <span className="inline-block w-1.5 h-3 bg-foreground/60 ml-0.5 align-middle animate-pulse" />
        )}
      </div>
    </div>
  )
}
