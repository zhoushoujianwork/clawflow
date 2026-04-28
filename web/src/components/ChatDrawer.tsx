import { useEffect, useMemo, useRef, useState } from 'react'
import { X, MessageSquare } from 'lucide-react'
import { useChatDrawer } from '../lib/chatContext'
import { XTerminal } from './Terminal'

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

  // closeIntentRef is read by Terminal.tsx in its WS cleanup so the
  // browser can tell the server which kind of close this is. We use a
  // ref instead of state because the cleanup runs on unmount with
  // closure over the LATEST ref value, not the render-time one.
  const closeIntentRef = useRef<CloseIntent>('collapse')
  const drawerRef = useRef<HTMLDivElement>(null)

  const wsUrl = useMemo(() => {
    if (!target) return ''
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const params = new URLSearchParams()
    params.set('repo', target.repo)
    if (target.issue) params.set('issue', String(target.issue))
    if (target.model) params.set('model', target.model)
    return `${proto}//${window.location.host}/ws/pty?${params.toString()}`
  }, [target])

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

      {/* Terminal */}
      <div className="flex-1 min-h-0">
        {isOpen && target && wsUrl && (
          <XTerminal wsUrl={wsUrl} closeIntentRef={closeIntentRef} />
        )}
      </div>
      </div>
    </>
  )
}
