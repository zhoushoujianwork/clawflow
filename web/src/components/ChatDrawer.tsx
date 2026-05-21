import { useEffect } from 'react'
import { X, AlertCircle } from 'lucide-react'
import { useChatDrawer } from '../lib/chatContext'

// WS_CLOSE_* — kept exported because internal/pty/server.go's WS code
// path is still mounted (other consumers may dial it directly even
// though the dashboard now spawns a native terminal instead). The
// numeric values must stay in sync with that server.
export const WS_CLOSE_DESTROY = 4001
export const WS_CLOSE_COLLAPSE = 4000

export type CloseIntent = 'destroy' | 'collapse'

// ChatDrawer is the FALLBACK ui surfaced when /api/chat/spawn fails
// — e.g. linux without a recognized terminal emulator, or osascript
// missing on a stripped-down macOS image. The happy path opens the
// user's native terminal directly with no in-browser surface, so
// this component renders nothing on success.
export function ChatDrawer() {
  const { isOpen, spawnError, close } = useChatDrawer()

  useEffect(() => {
    if (!isOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [isOpen, close])

  if (!isOpen || !spawnError) return null

  return (
    <>
      <div
        className="fixed inset-0 z-40 bg-black/30"
        onClick={close}
        aria-hidden="true"
      />
      <div
        className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 z-50 max-w-lg w-[calc(100%-2rem)] rounded-xl border shadow-2xl"
        style={{ background: 'hsl(var(--bg-primary))', borderColor: 'hsl(var(--border))' }}
      >
        <div className="flex items-center justify-between px-4 h-11 border-b" style={{ borderColor: 'hsl(var(--border))' }}>
          <div className="flex items-center gap-2 text-sm font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
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
            {spawnError.error}
          </div>
          {spawnError.command && (
            <>
              <div className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                {spawnError.hint || 'Run this in any terminal:'}
              </div>
              <code
                className="block font-mono text-[11px] px-3 py-2 rounded select-all break-all"
                style={{ background: 'hsl(var(--bg-elevated))', color: 'hsl(var(--text-high))' }}
              >
                {spawnError.command}
              </code>
            </>
          )}
        </div>
      </div>
    </>
  )
}
