import { useEffect, useMemo } from 'react'
import { X, MessageSquare } from 'lucide-react'
import { useChatDrawer } from '../lib/chatContext'
import { XTerminal } from './Terminal'

export function ChatDrawer() {
  const { isOpen, target, close } = useChatDrawer()

  const wsUrl = useMemo(() => {
    if (!target) return ''
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const params = new URLSearchParams()
    params.set('repo', target.repo)
    if (target.issue) params.set('issue', String(target.issue))
    if (target.model) params.set('model', target.model)
    return `${proto}//${window.location.host}/ws/pty?${params.toString()}`
  }, [target])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && isOpen) close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [isOpen, close])

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 z-40 transition-opacity duration-300"
        style={{
          background: 'rgba(0,0,0,0.3)',
          opacity: isOpen ? 1 : 0,
          pointerEvents: isOpen ? 'auto' : 'none',
        }}
        onClick={close}
      />

      {/* Drawer */}
      <div
        className="fixed top-0 right-0 z-50 h-full flex flex-col transition-transform duration-300 ease-out"
        style={{
          width: 'min(520px, 90vw)',
          transform: isOpen ? 'translateX(0)' : 'translateX(100%)',
          background: '#1a1a2e',
          borderLeft: '1px solid hsl(var(--border))',
          boxShadow: isOpen ? '-8px 0 30px rgba(0,0,0,0.25)' : 'none',
        }}
      >
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
            onClick={close}
            className="w-6 h-6 flex items-center justify-center rounded-sm transition-colors hover:opacity-80"
            style={{ color: 'hsl(var(--text-low))' }}
            aria-label="Close chat"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>

        {/* Terminal */}
        <div className="flex-1 min-h-0">
          {isOpen && target && wsUrl && (
            <XTerminal wsUrl={wsUrl} />
          )}
        </div>
      </div>
    </>
  )
}
