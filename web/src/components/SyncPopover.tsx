import { useCallback, useEffect, useRef, useState } from 'react'
import { Cloud, Loader2, RefreshCw } from 'lucide-react'

interface SyncStatus {
  gist_id: string
  gh_token_set: boolean
  last_synced_at?: string
}

type ActionState = 'idle' | 'loading' | 'success' | 'error'

function formatRelativeTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const diff = Date.now() - t
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  if (diff < 604_800_000) return `${Math.floor(diff / 86_400_000)}d ago`
  return new Date(iso).toLocaleDateString()
}

export function SyncPopover() {
  const [open, setOpen] = useState(false)
  const [status, setStatus] = useState<SyncStatus | null>(null)
  const [statusLoading, setStatusLoading] = useState(false)

  const [pushState, setPushState] = useState<ActionState>('idle')
  const [pullState, setPullState] = useState<ActionState>('idle')
  const [pushError, setPushError] = useState<string | null>(null)
  const [pullError, setPullError] = useState<string | null>(null)

  const [token, setToken] = useState('')
  const [loginState, setLoginState] = useState<ActionState>('idle')
  const [loginError, setLoginError] = useState<string | null>(null)

  const containerRef = useRef<HTMLDivElement>(null)

  const fetchStatus = useCallback(async () => {
    setStatusLoading(true)
    try {
      const r = await fetch('/api/sync/status', { cache: 'no-store' })
      if (r.ok) {
        const d: SyncStatus = await r.json()
        setStatus(d)
      }
    } catch {
      // silently ignore — popover will show "Not configured"
    } finally {
      setStatusLoading(false)
    }
  }, [])

  // Fetch status when popover opens
  useEffect(() => {
    if (open) {
      void fetchStatus()
    }
  }, [open, fetchStatus])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    function handleClick(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    function handleEsc(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', handleClick)
    document.addEventListener('keydown', handleEsc)
    return () => {
      document.removeEventListener('mousedown', handleClick)
      document.removeEventListener('keydown', handleEsc)
    }
  }, [open])

  async function handlePush() {
    setPushState('loading')
    setPushError(null)
    try {
      const r = await fetch('/api/sync/push', { method: 'POST' })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
      setPushState('success')
      await fetchStatus()
    } catch (e) {
      setPushState('error')
      setPushError(e instanceof Error ? e.message : 'push failed')
    } finally {
      setTimeout(() => setPushState('idle'), 3000)
    }
  }

  async function handlePull() {
    setPullState('loading')
    setPullError(null)
    try {
      const r = await fetch('/api/sync/pull', { method: 'POST' })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
      setPullState('success')
      await fetchStatus()
    } catch (e) {
      setPullState('error')
      setPullError(e instanceof Error ? e.message : 'pull failed')
    } finally {
      setTimeout(() => setPullState('idle'), 3000)
    }
  }

  async function handleLogin() {
    if (!token.trim()) return
    setLoginState('loading')
    setLoginError(null)
    try {
      const r = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: token.trim() }),
      })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
      setLoginState('success')
      setToken('')
      await fetchStatus()
    } catch (e) {
      setLoginState('error')
      setLoginError(e instanceof Error ? e.message : 'login failed')
    } finally {
      setTimeout(() => setLoginState(s => s === 'success' ? 'idle' : s), 3000)
    }
  }

  const isActing = pushState === 'loading' || pullState === 'loading'
  const configured = status?.gh_token_set === true
  const gistHint = status?.gist_id ? status.gist_id.slice(-8) : null

  return (
    <div ref={containerRef} className="relative">
      <button
        onClick={() => setOpen(v => !v)}
        className="w-7 h-7 flex items-center justify-center rounded-sm transition-colors hover:opacity-80"
        style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
        aria-label="Sync settings"
        title="Sync"
        aria-expanded={open}
        aria-haspopup="true"
      >
        {configured
          ? <RefreshCw className="w-3.5 h-3.5" />
          : <Cloud className="w-3.5 h-3.5" />
        }
      </button>

      {open && (
        <div
          className="absolute right-0 top-full mt-2 w-72 rounded-lg border shadow-lg z-50 p-3 flex flex-col gap-3"
          style={{
            background: 'hsl(var(--bg-panel))',
            borderColor: 'hsl(var(--border))',
            color: 'hsl(var(--text-high))',
          }}
          role="dialog"
          aria-label="Sync popover"
        >
          {/* Status line */}
          <div className="flex flex-col gap-0.5">
            <span className="text-[11px] font-semibold uppercase tracking-wide" style={{ color: 'hsl(var(--text-low))' }}>
              Sync
            </span>
            {statusLoading ? (
              <span className="text-xs flex items-center gap-1" style={{ color: 'hsl(var(--text-low))' }}>
                <Loader2 className="w-3 h-3 animate-spin" /> Loading…
              </span>
            ) : configured ? (
              <div className="flex flex-col gap-0.5">
                <a
                  href={`https://gist.github.com/${status.gist_id}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-xs font-mono underline hover:opacity-80 transition-opacity"
                  style={{ color: 'hsl(var(--brand))' }}
                  title={`Open Gist ${status.gist_id}`}
                >
                  Gist: …{gistHint} ↗
                </a>
                {status?.last_synced_at && (
                  <span className="text-[11px]" style={{ color: 'hsl(var(--text-low))' }}>
                    Last synced {formatRelativeTime(status.last_synced_at)}
                  </span>
                )}
              </div>
            ) : (
              <span className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>Not configured</span>
            )}
          </div>

          {/* Push / Pull buttons — only shown when configured */}
          {configured && (
            <div className="flex gap-2">
              <button
                onClick={handlePush}
                disabled={isActing}
                className="flex-1 inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                style={{
                  background: pushState === 'success'
                    ? 'hsl(var(--success) / 0.15)'
                    : pushState === 'error'
                      ? 'hsl(var(--error) / 0.15)'
                      : 'hsl(var(--brand) / 0.12)',
                  color: pushState === 'success'
                    ? 'hsl(var(--success))'
                    : pushState === 'error'
                      ? 'hsl(var(--error))'
                      : 'hsl(var(--brand))',
                }}
              >
                {pushState === 'loading'
                  ? <><Loader2 className="w-3 h-3 animate-spin" /> Pushing…</>
                  : pushState === 'success'
                    ? '✓ Pushed'
                    : pushState === 'error'
                      ? '✗ Failed'
                      : <><RefreshCw className="w-3 h-3" /> Push</>
                }
              </button>
              <button
                onClick={handlePull}
                disabled={isActing}
                className="flex-1 inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                style={{
                  background: pullState === 'success'
                    ? 'hsl(var(--success) / 0.15)'
                    : pullState === 'error'
                      ? 'hsl(var(--error) / 0.15)'
                      : 'hsl(var(--bg-primary))',
                  color: pullState === 'success'
                    ? 'hsl(var(--success))'
                    : pullState === 'error'
                      ? 'hsl(var(--error))'
                      : 'hsl(var(--text-high))',
                  border: '1px solid hsl(var(--border))',
                }}
              >
                {pullState === 'loading'
                  ? <><Loader2 className="w-3 h-3 animate-spin" /> Pulling…</>
                  : pullState === 'success'
                    ? '✓ Pulled'
                    : pullState === 'error'
                      ? '✗ Failed'
                      : <><Cloud className="w-3 h-3" /> Pull</>
                }
              </button>
            </div>
          )}

          {/* Inline error messages */}
          {pushError && pushState === 'error' && (
            <p className="text-[11px] break-words" style={{ color: 'hsl(var(--error))' }}>{pushError}</p>
          )}
          {pullError && pullState === 'error' && (
            <p className="text-[11px] break-words" style={{ color: 'hsl(var(--error))' }}>{pullError}</p>
          )}

          {/* Login section — shown when not configured */}
          {!configured && (
            <div className="flex flex-col gap-2 pt-1 border-t" style={{ borderColor: 'hsl(var(--border))' }}>
              <span className="text-[11px]" style={{ color: 'hsl(var(--text-low))' }}>
                Enter a GitHub token to enable sync via Gist.
              </span>
              <input
                type="password"
                value={token}
                onChange={e => setToken(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') void handleLogin() }}
                placeholder="ghp_…"
                className="w-full px-2.5 py-1.5 rounded-md text-xs font-mono outline-none"
                style={{
                  background: 'hsl(var(--bg-primary))',
                  border: '1px solid hsl(var(--border))',
                  color: 'hsl(var(--text-high))',
                }}
                aria-label="GitHub token"
                autoComplete="off"
              />
              <button
                onClick={handleLogin}
                disabled={loginState === 'loading' || !token.trim()}
                className="inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                style={{
                  background: loginState === 'success'
                    ? 'hsl(var(--success) / 0.15)'
                    : 'hsl(var(--brand))',
                  color: loginState === 'success'
                    ? 'hsl(var(--success))'
                    : 'hsl(var(--text-on-brand))',
                }}
              >
                {loginState === 'loading'
                  ? <><Loader2 className="w-3 h-3 animate-spin" /> Connecting…</>
                  : loginState === 'success'
                    ? '✓ Connected'
                    : 'Connect'
                }
              </button>
              {loginError && (
                <p className="text-[11px] break-words" style={{ color: 'hsl(var(--error))' }}>{loginError}</p>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
