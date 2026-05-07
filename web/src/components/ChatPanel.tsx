import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Send, X, Loader2, FileText, AlertCircle, Sparkles, Save as SaveIcon, MessageSquareText } from 'lucide-react'
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

const KIND_META: Record<ChatPanelProps['kind'], { title: string; subtitle: string; icon: typeof Sparkles }> = {
  goals: {
    title: 'Draft your goals',
    subtitle: 'Tell the Pilot what to focus on. It will interview you, then propose a draft you can save.',
    icon: Sparkles,
  },
  context: {
    title: 'Update project context',
    subtitle: 'Talk through architecture, conventions, and current state. Save the result to context.md.',
    icon: MessageSquareText,
  },
}

const SUGGESTIONS_BY_KIND: Record<ChatPanelProps['kind'], string[]> = {
  goals: [
    '帮我从零开始起草 goals.md，先问我优先级',
    '本季重点是减少 agent-failed 率',
    '我现在不想让 Pilot 动 PR 只让他理 issue',
  ],
  context: [
    'Walk me through what is in the project right now',
    'Pull architecture from each member repo',
    'What changed since the last context.md update?',
  ],
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
  const [saved, setSaved] = useState(false)

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
    setTimeout(() => inputRef.current?.focus(), 0)
  }, [])

  const sendText = useCallback(async (text: string) => {
    const trimmed = text.trim()
    if (!trimmed || streaming) return

    setInput('')
    setSaved(false)
    setMessages(prev => [...prev, { role: 'user', text: trimmed }])
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
          message: trimmed,
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
        onDone: () => { void errored },
      },
    )

    finalizeAssistant()
  }, [streaming, kind, project, finalizeAssistant])

  const send = useCallback(() => { void sendText(input) }, [sendText, input])

  const cancel = useCallback(() => {
    abortRef.current?.abort()
    finalizeAssistant()
  }, [finalizeAssistant])

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  const handleSave = useCallback(async () => {
    if (!currentDraft || streaming || saving) return
    setSaving(true)
    try {
      await onSave(currentDraft)
      setSaved(true)
      setTimeout(() => setSaved(false), 2500)
    } finally {
      setSaving(false)
    }
  }, [currentDraft, streaming, saving, onSave])

  const saveDisabled = saving || streaming || !currentDraft

  const meta = KIND_META[kind]
  const HeaderIcon = meta.icon
  const isEmpty = messages.length === 0 && !assistantBuf && !streaming

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      onClick={e => { if (e.target === e.currentTarget) handleClose() }}
    >
      <div className="relative w-full md:w-[92vw] lg:w-[85vw] xl:w-[1200px] max-w-[1200px] h-full md:h-[88vh] md:mx-4 bg-card md:border md:border-border md:rounded-2xl shadow-2xl flex flex-col overflow-hidden">
        {/* Header — gradient strip with icon + title + project crumb */}
        <div className="relative shrink-0 border-b border-border">
          <div className="absolute inset-0 bg-gradient-to-r from-primary/8 via-primary/4 to-transparent dark:from-primary/12 dark:via-primary/6 pointer-events-none" />
          <div className="relative flex items-start justify-between px-6 py-4">
            <div className="flex items-start gap-3 min-w-0">
              <div className="shrink-0 w-9 h-9 rounded-lg bg-primary/10 dark:bg-primary/20 flex items-center justify-center text-primary">
                <HeaderIcon className="w-5 h-5" />
              </div>
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <h2 className="text-base font-semibold text-foreground">{meta.title}</h2>
                  <span className="inline-flex items-center gap-1 px-1.5 py-0.5 bg-secondary/70 border border-border rounded text-[11px] font-mono text-muted-foreground">
                    <FileText className="w-3 h-3" />
                    {draftTag}
                  </span>
                  <span className="text-xs text-muted-foreground">·</span>
                  <span className="text-xs font-mono text-muted-foreground truncate">{project}</span>
                </div>
                <p className="text-xs text-muted-foreground mt-0.5 truncate">{meta.subtitle}</p>
              </div>
            </div>
            <button
              type="button"
              onClick={handleClose}
              className="shrink-0 ml-3 w-8 h-8 -mr-1 -mt-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-secondary/60 transition-colors flex items-center justify-center"
              aria-label="Close"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        <div className="flex-1 flex flex-col md:flex-row overflow-hidden">
          {/* LEFT: chat */}
          <div className="flex-1 flex flex-col border-b md:border-b-0 md:border-r border-border min-h-0 bg-background/40">
            <div ref={historyRef} className="flex-1 overflow-y-auto px-6 py-5 space-y-4">
              {isEmpty && (
                <EmptyState
                  icon={HeaderIcon}
                  title={kind === 'goals' ? '先聊呀，AI 会问你优先级' : 'Tell me about the project'}
                  subtitle={kind === 'goals'
                    ? '列出本季你最在乎什么 / 什么必须避免 / 如果只能完成一项选哪项'
                    : 'Walk me through architecture, conventions, current state.'}
                  suggestions={SUGGESTIONS_BY_KIND[kind]}
                  onPick={s => { setInput(s); inputRef.current?.focus() }}
                />
              )}
              {messages.map((m, i) => (
                <Bubble key={i} role={m.role} text={m.text} />
              ))}
              {assistantBuf && <Bubble role="assistant" text={assistantBuf} streaming />}
              {streaming && !assistantBuf && (
                <div className="flex items-center gap-2 px-3 py-2 text-xs text-muted-foreground">
                  <ThinkingDots />
                  <span>Thinking</span>
                </div>
              )}
            </div>

            {/* Input dock */}
            <div className="border-t border-border bg-card/80 backdrop-blur-sm shrink-0">
              <div className="px-5 py-3">
                <div className="relative bg-secondary/40 dark:bg-secondary/30 border border-border rounded-xl focus-within:border-primary/60 focus-within:bg-card transition-colors">
                  <textarea
                    ref={inputRef}
                    value={input}
                    onChange={e => setInput(e.target.value)}
                    onKeyDown={onKeyDown}
                    placeholder={streaming ? 'Streaming response… (cancel to stop)' : 'Message  (⏎ send  ·  Shift+⏎ newline)'}
                    rows={3}
                    disabled={streaming}
                    className="w-full resize-none bg-transparent border-0 px-3.5 pt-3 pb-12 text-sm text-foreground placeholder:text-muted-foreground/80 focus:outline-none disabled:opacity-60"
                  />
                  <div className="absolute left-3 right-3 bottom-2 flex items-center justify-between gap-2 pointer-events-none">
                    <span className="text-[11px] text-muted-foreground/70 tabular-nums pointer-events-auto">
                      {input.length > 0 && `${input.length} chars`}
                    </span>
                    <div className="pointer-events-auto">
                      {streaming ? (
                        <button
                          type="button"
                          onClick={cancel}
                          className="inline-flex items-center gap-1.5 h-8 px-3 rounded-lg bg-card border border-border text-xs font-medium text-foreground hover:bg-secondary transition-colors shadow-sm"
                        >
                          <X className="w-3.5 h-3.5" />
                          Cancel
                        </button>
                      ) : (
                        <button
                          type="button"
                          onClick={send}
                          disabled={!input.trim()}
                          className={cn(
                            'inline-flex items-center gap-1.5 h-8 px-3 rounded-lg text-xs font-semibold transition-all shadow-sm',
                            input.trim()
                              ? 'bg-primary text-primary-foreground hover:opacity-90'
                              : 'bg-secondary text-muted-foreground/60 cursor-not-allowed',
                          )}
                        >
                          <Send className="w-3.5 h-3.5" />
                          Send
                        </button>
                      )}
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>

          {/* RIGHT: draft preview */}
          <div className="md:w-[44%] lg:w-[40%] flex flex-col min-h-0 bg-secondary/20 dark:bg-secondary/10">
            <div className="flex items-center justify-between px-5 py-3 border-b border-border shrink-0">
              <div className="flex items-center gap-2 min-w-0">
                <FileText className="w-4 h-4 text-muted-foreground shrink-0" />
                <span className="text-sm font-semibold text-foreground truncate">{draftTag}</span>
                {extractedDraft && (
                  <span className="text-[11px] text-muted-foreground tabular-nums shrink-0">
                    · {extractedDraft.split('\n').length} lines · {(extractedDraft.length / 1024).toFixed(1)}kb
                  </span>
                )}
              </div>
              {draftDirty && (
                <span className="inline-flex items-center gap-1 text-[11px] font-medium text-amber-700 dark:text-amber-400 shrink-0">
                  <span className="inline-block w-1.5 h-1.5 rounded-full bg-amber-500 animate-pulse" />
                  unsaved
                </span>
              )}
            </div>
            <div className="flex-1 overflow-auto">
              {currentDraft ? (
                <pre className="text-[12.5px] leading-relaxed font-mono whitespace-pre-wrap text-foreground px-5 py-4">
                  {currentDraft}
                </pre>
              ) : (
                <div className="h-full flex flex-col items-center justify-center px-8 text-center">
                  <div className="w-12 h-12 rounded-xl bg-secondary/60 dark:bg-secondary/40 flex items-center justify-center mb-3">
                    <FileText className="w-6 h-6 text-muted-foreground/70" />
                  </div>
                  <p className="text-sm text-muted-foreground">No draft yet</p>
                  <p className="text-xs text-muted-foreground/70 mt-1 max-w-[260px]">
                    {kind === 'goals'
                      ? '聊到可以落定的时候说一句“起草一份”，AI 会把最终内容放进一个 fenced 块'
                      : 'When ready, ask the assistant to emit the final document in a fenced block.'}
                  </p>
                </div>
              )}
            </div>
            <div className="border-t border-border bg-card/80 backdrop-blur-sm shrink-0 px-4 py-3 flex items-center justify-between gap-2">
              <span className="text-[11px] text-muted-foreground">
                {saved ? (
                  <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400 font-medium">
                    <SaveIcon className="w-3 h-3" />
                    Saved
                  </span>
                ) : draftDirty ? (
                  'Click Save to write goals.md and commit'
                ) : currentDraft ? (
                  'No new changes'
                ) : (
                  ''
                )}
              </span>
              <button
                type="button"
                onClick={() => void handleSave()}
                disabled={saveDisabled || !draftDirty}
                className={cn(
                  'inline-flex items-center gap-1.5 h-8 px-4 rounded-lg text-xs font-semibold transition-all shadow-sm',
                  saveDisabled || !draftDirty
                    ? 'bg-secondary text-muted-foreground/60 cursor-not-allowed'
                    : 'bg-emerald-600 hover:bg-emerald-700 text-white',
                )}
              >
                {saving ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <SaveIcon className="w-3.5 h-3.5" />}
                {saving ? 'Saving…' : 'Save'}
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
        <div className="max-w-[80%] bg-primary text-primary-foreground rounded-2xl rounded-tr-sm px-4 py-2.5 text-sm whitespace-pre-wrap shadow-sm">
          {text}
        </div>
      </div>
    )
  }
  if (role === 'error') {
    return (
      <div className="flex items-start gap-2 bg-red-50 border border-red-200 dark:bg-red-950/30 dark:border-red-900/60 rounded-xl px-3.5 py-2.5">
        <AlertCircle className="w-4 h-4 text-red-600 dark:text-red-400 shrink-0 mt-0.5" />
        <div className="text-xs text-red-800 dark:text-red-300 whitespace-pre-wrap font-mono leading-relaxed">{text}</div>
      </div>
    )
  }
  return (
    <div className="flex justify-start">
      <div className="max-w-[88%] bg-card border border-border rounded-2xl rounded-tl-sm px-4 py-3 shadow-sm">
        <div className="text-sm text-foreground prose-sm">
          <Markdown>{text}</Markdown>
        </div>
        {streaming && (
          <span className="inline-block w-1.5 h-3.5 bg-primary/70 ml-0.5 align-middle animate-pulse rounded-sm" />
        )}
      </div>
    </div>
  )
}

function ThinkingDots() {
  return (
    <span className="inline-flex items-center gap-0.5">
      <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground/60 animate-bounce" style={{ animationDelay: '0ms' }} />
      <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground/60 animate-bounce" style={{ animationDelay: '120ms' }} />
      <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground/60 animate-bounce" style={{ animationDelay: '240ms' }} />
    </span>
  )
}

function EmptyState({
  icon: Icon,
  title,
  subtitle,
  suggestions,
  onPick,
}: {
  icon: typeof Sparkles
  title: string
  subtitle: string
  suggestions: string[]
  onPick: (s: string) => void
}) {
  return (
    <div className="h-full flex flex-col items-center justify-center text-center px-6 py-10 -mt-2">
      <div className="w-14 h-14 rounded-2xl bg-primary/10 dark:bg-primary/15 text-primary flex items-center justify-center mb-4">
        <Icon className="w-7 h-7" />
      </div>
      <h3 className="text-base font-semibold text-foreground mb-1.5">{title}</h3>
      <p className="text-sm text-muted-foreground max-w-md mb-5">{subtitle}</p>
      {suggestions.length > 0 && (
        <div className="flex flex-col gap-2 w-full max-w-md">
          <span className="text-[11px] uppercase tracking-wider text-muted-foreground/70 font-medium">Try saying</span>
          <div className="flex flex-wrap justify-center gap-2">
            {suggestions.map(s => (
              <button
                key={s}
                type="button"
                onClick={() => onPick(s)}
                className="text-xs px-3 py-1.5 rounded-full bg-card border border-border hover:border-primary/40 hover:bg-secondary/60 transition-colors text-muted-foreground hover:text-foreground text-left"
              >
                {s}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
