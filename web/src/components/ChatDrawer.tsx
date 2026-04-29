import { useEffect, useRef, useState } from 'react'
import { X, MessageSquare, Terminal as TerminalIcon, ExternalLink, Loader2, AlertCircle, CheckCircle2 } from 'lucide-react'
import { useChatDrawer } from '../lib/chatContext'

const WIDTH_KEY = 'clawflow.chat.width'
const MIN_WIDTH = 320
const DEFAULT_WIDTH = 480

// WebSocket close codes — must match wsCloseDestroy/Collapse in
// internal/pty/server.go. Browsers reserve the 4000–4999 range for app
// use.
export const WS_CLOSE_DESTROY = 4001
export const WS_CLOSE_COLLAPSE = 4000

export type CloseIntent = 'destroy' | 'collapse'

function clampWidth(w: number): number {
  const max = Math.floor(window.innerWidth * 0.9)
  return Math.max(MIN_WIDTH, Math.min(max, w))
}

export function ChatDrawer() {
  const { isOpen, target, close } = useChatDrawer()

  const [width, setWidth] = useState<number>(() => {
    if (typeof window === 'undefined') return DEFAULT_WIDTH
    const stored = window.localStorage.getItem(WIDTH_KEY)
    const n = stored ? parseInt(stored, 10) : NaN
    return clampWidth(isNaN(n) ? DEFAULT_WIDTH : n)
  })
  const [dragging, setDragging] = useState(false)

  // closeIntentRef is preserved for the WS cleanup path that Terminal.tsx
  // still uses if anything else routes through it. The native-terminal
  // launcher path doesn't need it.
  const closeIntentRef = useRef<CloseIntent>('collapse')
  const drawerRef = useRef<HTMLDivElement>(null)

  // Launcher state for the native-terminal flow.
  const [spawning, setSpawning] = useState(false)
  const [spawnResult, setSpawnResult] = useState<
    | { ok: true; command: string }
    | { ok: false; error: string; command?: string; hint?: string }
    | null
  >(null)

  // Reset the launcher result every time the drawer opens for a different
  // (repo, issue) pair — stale "Launched" / error chips from a previous
  // target shouldn't bleed into the next one.
  useEffect(() => {
    setSpawnResult(null)
    setSpawning(false)
  }, [target?.repo, target?.issue])

  const collapseDrawer = () => {
    closeIntentRef.current = 'collapse'
    close()
  }
  const destroyDrawer = () => {
    closeIntentRef.current = 'destroy'
    close()
  }

  // Escape = collapse (preserve session). User explicitly chose X for
  // destroy, so a casual dismiss should not silently nuke the chat.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && isOpen) collapseDrawer()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOpen])

  // Re-clamp on viewport resize so a stored width doesn't exceed 90vw
  // after the user shrinks the window.
  useEffect(() => {
    const onResize = () => setWidth(w => clampWidth(w))
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const startDrag = (e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = width
    let last = startW
    setDragging(true)
    document.body.style.userSelect = 'none'
    document.body.style.cursor = 'ew-resize'

    const onMove = (ev: MouseEvent) => {
      // The handle sits on the LEFT edge of a right-anchored drawer:
      // dragging left should grow the drawer, hence (startX - clientX).
      last = clampWidth(startW + (startX - ev.clientX))
      setWidth(last)
    }
    const onUp = () => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      document.body.style.userSelect = ''
      document.body.style.cursor = ''
      window.localStorage.setItem(WIDTH_KEY, String(last))
      setDragging(false)
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }

  return (
    <>
      {/* Transparent backdrop — clicks anywhere outside the drawer
          collapse it (preserve session for next open). The X button
          uses destroyDrawer instead. We intentionally cover the whole
          page so any stray click is treated as "dismiss". */}
      <div
        className="fixed inset-0 z-40 transition-opacity duration-200"
        style={{
          background: 'transparent',
          opacity: isOpen ? 1 : 0,
          pointerEvents: isOpen ? 'auto' : 'none',
        }}
        onClick={collapseDrawer}
        aria-hidden="true"
      />

      <div
        ref={drawerRef}
        className="fixed top-0 right-0 z-50 h-full flex flex-col transition-transform duration-300 ease-out"
        style={{
          width: `${width}px`,
          transform: isOpen ? 'translateX(0)' : 'translateX(100%)',
          background: '#1a1a2e',
          borderLeft: '1px solid hsl(var(--border))',
          boxShadow: isOpen ? '-8px 0 30px rgba(0,0,0,0.25)' : 'none',
          // Skip the slide animation while the user is actively dragging
          // the resize handle, otherwise width updates feel laggy.
          transitionProperty: dragging ? 'none' : undefined,
        }}
      >
      {/* Drag handle — wide invisible hit area straddling the left edge,
          with a thin always-visible line in the center so users can find
          it. The hit zone extends 4px outside the drawer for easy grab. */}
      <div
        onMouseDown={startDrag}
        className="absolute top-0 -left-1 h-full w-2 cursor-ew-resize z-10 group"
        aria-label="Resize chat drawer"
      >
        <div
          className="absolute top-0 left-1/2 -translate-x-1/2 h-full w-px transition-colors group-hover:bg-[hsl(var(--brand))]"
          style={{
            background: dragging ? 'hsl(var(--brand))' : 'rgba(255,255,255,0.12)',
          }}
        />
      </div>

      {/* Header */}
      <div
        className="flex items-center justify-between px-4 h-11 shrink-0"
        style={{
          background: 'hsl(var(--bg-primary))',
          borderBottom: '1px solid hsl(var(--border))',
        }}
      >
        <div className="flex items-center gap-2 min-w-0">
          <MessageSquare className="w-3.5 h-3.5 shrink-0" style={{ color: 'hsl(var(--brand))' }} />
          <span className="text-xs font-mono truncate" style={{ color: 'hsl(var(--text-high))' }}>
            {target?.repo}
          </span>
          {target?.issue && (
            <span className="text-[11px] font-mono shrink-0" style={{ color: 'hsl(var(--text-low))' }}>
              #{target.issue}
            </span>
          )}
        </div>
        <button
          onClick={destroyDrawer}
          className="w-6 h-6 flex items-center justify-center rounded-sm transition-colors hover:opacity-80"
          style={{ color: 'hsl(var(--text-low))' }}
          aria-label="End chat session"
          title="End session (next open will start a new conversation)"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      </div>

      {/* Native terminal launcher.

          Why no in-browser terminal: xterm.js works but inherits the
          tax of running a TTY emulator inside an HTML element — focus
          quirks, font fallbacks, IME edge cases, copy/paste edges,
          cursor blink relying on focus state. The user already has a
          native terminal they trust; clicking the button below opens
          it pre-loaded with `clawflow chat --repo X --issue Y` so the
          system prompt + issue context arrive at session start exactly
          as the in-drawer flow did. */}
      <div className="flex-1 min-h-0 flex flex-col items-stretch px-6 py-8 gap-4 overflow-y-auto">
        <div className="flex items-start gap-3">
          <TerminalIcon className="w-5 h-5 mt-0.5 shrink-0" style={{ color: 'hsl(var(--brand))' }} />
          <div className="text-sm" style={{ color: 'hsl(var(--text-high))' }}>
            <p className="font-semibold mb-1">Open chat in your terminal</p>
            <p className="text-xs leading-relaxed" style={{ color: 'hsl(var(--text-low))' }}>
              Spawns a new window of your native terminal app
              {' '}<span className="font-mono">(macOS Terminal.app / $TERMINAL on linux)</span>{' '}
              already running <code className="px-1 py-0.5 rounded bg-secondary text-[11px]">clawflow chat</code> with this issue's context loaded.
            </p>
          </div>
        </div>

        <button
          type="button"
          disabled={!target || spawning}
          onClick={async () => {
            if (!target) return
            setSpawning(true)
            setSpawnResult(null)
            try {
              const r = await fetch('/api/chat/spawn', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                  repo: target.repo,
                  issue: target.issue,
                  model: target.model,
                }),
              })
              const d = await r.json().catch(() => ({}))
              if (r.ok && d.status === 'ok') {
                setSpawnResult({ ok: true, command: d.command })
              } else {
                setSpawnResult({ ok: false, error: d.error || `HTTP ${r.status}`, command: d.command, hint: d.hint })
              }
            } catch (e) {
              setSpawnResult({ ok: false, error: String((e as Error).message || e) })
            } finally {
              setSpawning(false)
            }
          }}
          className="inline-flex items-center justify-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          style={{
            background: 'hsl(var(--brand))',
            color: 'hsl(var(--on-brand))',
          }}
        >
          {spawning ? (
            <><Loader2 className="w-4 h-4 animate-spin" /> Opening…</>
          ) : (
            <><ExternalLink className="w-4 h-4" /> Open in Terminal</>
          )}
        </button>

        {spawnResult && spawnResult.ok && (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg border text-xs"
               style={{ background: 'hsl(var(--bg-elevated))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}>
            <CheckCircle2 className="w-3.5 h-3.5 mt-0.5 shrink-0 text-green-500" />
            <div className="min-w-0">
              <div className="font-semibold">Terminal launched.</div>
              <div className="font-mono text-[11px] mt-1 break-all" style={{ color: 'hsl(var(--text-low))' }}>
                {spawnResult.command}
              </div>
            </div>
          </div>
        )}
        {spawnResult && !spawnResult.ok && (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg border text-xs"
               style={{ background: 'rgba(220,38,38,0.08)', borderColor: 'rgba(220,38,38,0.3)', color: 'hsl(var(--text-high))' }}>
            <AlertCircle className="w-3.5 h-3.5 mt-0.5 shrink-0 text-red-500" />
            <div className="min-w-0">
              <div className="font-semibold">Couldn't open a terminal automatically.</div>
              <div className="text-[11px] mt-1" style={{ color: 'hsl(var(--text-low))' }}>
                {spawnResult.error}
              </div>
              {spawnResult.command && (
                <div className="mt-2">
                  <div className="text-[11px]" style={{ color: 'hsl(var(--text-low))' }}>
                    {spawnResult.hint || 'Run this in any terminal:'}
                  </div>
                  <code className="block font-mono text-[11px] mt-1 px-2 py-1 rounded select-all break-all"
                        style={{ background: 'hsl(var(--bg-elevated))' }}>
                    {spawnResult.command}
                  </code>
                </div>
              )}
            </div>
          </div>
        )}

        {/* Avoid 'unused' on closeIntentRef without ripping out the
            type for a future revival. The ref is still wired into the
            destroyDrawer path on the X button. */}
        <span className="hidden">{String(!!closeIntentRef.current)}</span>
      </div>
      </div>
    </>
  )
}
