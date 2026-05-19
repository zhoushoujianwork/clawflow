import { createFileRoute, Outlet, Link } from '@tanstack/react-router'
import { Loader2, LogOut } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTheme } from '../lib/useTheme'
import { ChatProvider, useChatDrawer } from '../lib/chatContext'
import { ChatDrawer } from '../components/ChatDrawer'
import { fetchAuthMe, signOut, type AuthMe, type AuthMeResult } from '../lib/cloudApi'

export const Route = createFileRoute('/_app')({
  component: AppLayout,
})

function AppLayout() {
  const { theme, toggle } = useTheme()
  const [currentVersion, setCurrentVersion] = useState<string | null>(null)
  const [latestVersion, setLatestVersion] = useState<string | null>(null)
  const [updateAvailable, setUpdateAvailable] = useState(false)
  const [updating, setUpdating] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const [showTooltip, setShowTooltip] = useState(false)
  const [auth, setAuth] = useState<AuthMeResult | undefined>(undefined)
  // hostMode trinary:
  //  'cloud'   — served from cloud, signed in
  //  'cloud-anon' — served from cloud, anonymous (visible Sign-in button)
  //  'local'   — served from `clawflow web`, no cloud auth
  //  undefined — still loading auth probe
  const hostMode =
    auth === undefined ? undefined
    : auth.kind === 'authed' ? 'cloud'
    : auth.kind === 'anon' ? 'cloud-anon'
    : 'local'
  const authUser = auth?.kind === 'authed' ? auth.user : null

  // Check version once on mount. /api/version is local-web only; on the
  // cloud-served bundle the fetch will 404 silently and the version chip
  // stays hidden.
  useEffect(() => {
    fetch('/api/version', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d) {
          setCurrentVersion(d.current || null)
          setLatestVersion(d.latest || null)
          setUpdateAvailable(!!d.update_available)
        }
      })
      .catch(() => {})
  }, [])

  // Resolve identity on mount. fetchAuthMe distinguishes 'cloud-authed',
  // 'cloud-anon', and 'local' (no /api/v1/auth/me on this origin).
  useEffect(() => {
    fetchAuthMe()
      .then(setAuth)
      .catch(() => setAuth({ kind: 'no-cloud' }))
  }, [])

  const cloudConfigured = hostMode === 'cloud' || hostMode === 'cloud-anon'

  const triggerUpdate = useCallback(() => {
    setUpdating(true)
    fetch('/api/update', { method: 'POST' })
      .then(r => r.json())
      .then(d => {
        if (d.status !== 'ok') {
          setUpdating(false)
          return
        }
        if (d.respawning) {
          setRestarting(true)
          const started = Date.now()
          const tick = () => {
            fetch('/api/version', { cache: 'no-store' })
              .then(r => r.ok ? r.json() : Promise.reject(new Error('not ok')))
              .then(() => window.location.reload())
              .catch(() => {
                if (Date.now() - started > 30_000) {
                  setRestarting(false)
                  setUpdating(false)
                  return
                }
                setTimeout(tick, 500)
              })
          }
          setTimeout(tick, 800)
          return
        }
        setUpdating(false)
        setUpdateAvailable(false)
        setLatestVersion(null)
      })
      .catch(() => setUpdating(false))
  }, [])

  return (
    <ChatProvider>
      <div className="min-h-screen font-ibm-plex-sans" style={{ background: 'hsl(var(--bg-primary))' }}>
        <header
          className="sticky top-0 z-50 h-12 flex items-center justify-between px-6 border-b"
          style={{ background: 'hsl(var(--bg-primary))', borderColor: 'hsl(var(--border))' }}
        >
          <div className="flex items-center gap-5">
            <a
              href="/"
              className="flex items-center gap-1.5 font-semibold text-sm hover:opacity-80 transition-opacity"
              style={{ color: 'hsl(var(--text-high))' }}
            >
              <svg viewBox="0 0 24 24" className="w-4 h-4" aria-hidden="true">
                <g stroke="#e8792a" strokeWidth="2.3" strokeLinecap="round" fill="none">
                  <path d="M4,20 Q6,13 9,4" />
                  <path d="M10,20 Q12,13 15,4" />
                  <path d="M16,20 Q18,13 21,4" />
                </g>
              </svg>
              <span>
                <span style={{ color: 'hsl(var(--brand))' }}>Claw</span>Flow
              </span>
              <span
                className="text-[10px] font-normal ml-1"
                style={{ color: 'hsl(var(--text-low))' }}
                title={
                  hostMode === 'cloud' ? `Cloud · signed in as ${authUser?.login}` :
                  hostMode === 'cloud-anon' ? 'Cloud · not signed in' :
                  hostMode === 'local' ? 'Local clawflow web' : 'Loading…'
                }
              >
                {hostMode === undefined ? '' :
                 hostMode === 'local' ? 'local' : 'cloud'}
              </span>
            </a>
            {/* Version badge with upgrade indicator */}
            {currentVersion && (
              <div className="relative">
                <button
                  onClick={updateAvailable ? triggerUpdate : undefined}
                  onMouseEnter={() => setShowTooltip(true)}
                  onMouseLeave={() => setShowTooltip(false)}
                  disabled={!updateAvailable || updating || restarting}
                  className={`relative inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-mono transition-colors ${
                    updateAvailable && !updating && !restarting
                      ? 'cursor-pointer hover:bg-[hsl(var(--brand)/0.1)] text-[hsl(var(--text-mid,var(--text-low)))]'
                      : 'cursor-default text-[hsl(var(--text-low))]'
                  }`}
                  style={{ color: 'hsl(var(--text-low))' }}
                  aria-label={updateAvailable ? `Upgrade to ${latestVersion}` : `Version ${currentVersion}`}
                >
                  {restarting ? (
                    <><Loader2 className="w-3 h-3 animate-spin" /> Restarting…</>
                  ) : updating ? (
                    <><Loader2 className="w-3 h-3 animate-spin" /> Updating…</>
                  ) : (
                    <>
                      {currentVersion}
                      {updateAvailable && (
                        <svg className="w-2 h-2 shrink-0" viewBox="0 0 8 8" aria-hidden="true">
                          <circle cx="4" cy="4" r="4" fill="#ef4444" />
                        </svg>
                      )}
                    </>
                  )}
                </button>
                {/* Tooltip */}
                {showTooltip && updateAvailable && !updating && !restarting && (
                  <div
                    className="absolute top-full left-1/2 -translate-x-1/2 mt-1.5 px-2.5 py-1.5 rounded-md text-[11px] whitespace-nowrap shadow-lg z-50 border"
                    style={{
                      background: 'hsl(var(--bg-panel))',
                      borderColor: 'hsl(var(--border))',
                      color: 'hsl(var(--text-high))',
                    }}
                  >
                    Click to upgrade to <span className="font-semibold">{latestVersion}</span>
                  </div>
                )}
              </div>
            )}
            <nav className="flex gap-1">
              {/* Dashboard renders the cloud Jobs & Runs board in cloud
                  mode (see _app.dashboard.tsx). The old standalone
                  "Jobs" nav entry is gone — /cloud/jobs redirects here. */}
              <NavLink to="/dashboard">Dashboard</NavLink>
              <NavLink to="/repos">Repos</NavLink>
              <NavLink to="/projects">Projects</NavLink>
              <NavLink to="/operators">Operators</NavLink>
              {cloudConfigured && (
                <>
                  <NavLink to="/cloud/machines">Machines</NavLink>
                  <NavLink to="/cloud/bindings">Bindings</NavLink>
                </>
              )}
              {/* /usage is now real in cloud mode too — PR 4 wired the
                  worker → cloud usage upload and the cloud aggregation
                  endpoint, and the route renders CloudUsagePage. */}
              <NavLink to="/usage">Usage</NavLink>
              <NavLink to="/settings">Settings</NavLink>
            </nav>
          </div>

          <div className="flex items-center gap-2">
            {hostMode === 'cloud' && authUser && (
              <AccountChip user={authUser} />
            )}
            {hostMode === 'cloud-anon' && (
              <a
                href="/api/v1/github/app/login"
                className="text-xs font-medium px-2.5 py-1 rounded-sm transition-colors"
                style={{
                  background: 'hsl(var(--brand))',
                  color: 'white',
                }}
                title="Sign in with GitHub"
              >
                Sign in
              </a>
            )}
            <ReportIssueButton />
            <a
              href="https://github.com/zhoushoujianwork/clawflow"
              target="_blank"
              rel="noopener noreferrer"
              className="w-7 h-7 flex items-center justify-center rounded-sm transition-colors hover:opacity-80"
              style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
              aria-label="GitHub"
              title="GitHub"
            >
              <svg
                viewBox="0 0 16 16"
                className="w-3.5 h-3.5"
                fill="currentColor"
                aria-hidden="true"
              >
                <path
                  fillRule="evenodd"
                  d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8z"
                />
              </svg>
            </a>
            <button
              onClick={toggle}
              className="w-7 h-7 flex items-center justify-center rounded-sm transition-colors"
              style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
              aria-label="Toggle theme"
            >
              {theme === 'dark' ? '☀️' : '🌙'}
            </button>
          </div>
        </header>
        <main>
          <Outlet />
        </main>
        <ChatDrawer />
      </div>
    </ChatProvider>
  )
}

// NavLink is the single top-bar link template. Used 7-9 times across the
// header so factoring it out keeps the JSX skim-able and centralises the
// "active" styling (brand-tinted background + brand text).
function NavLink({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <Link
      to={to}
      className="text-sm font-medium px-2.5 py-1 rounded-sm transition-colors"
      style={{ color: 'hsl(var(--text-low))' }}
      activeProps={{ style: { color: 'hsl(var(--brand))', background: 'hsl(var(--brand) / 0.08)' } }}
    >
      {children}
    </Link>
  )
}

// AccountChip shows the signed-in cloud user in the top bar. Click for a
// tiny menu with Sign out. Avatar falls back to the user's first letter when
// avatar_url is absent or fails to load.
function AccountChip({ user }: { user: AuthMe }) {
  const [open, setOpen] = useState(false)
  const close = useCallback(() => setOpen(false), [])
  useEffect(() => {
    if (!open) return
    const onClick = (e: MouseEvent) => {
      const el = (e.target as HTMLElement).closest('[data-account-chip]')
      if (!el) close()
    }
    document.addEventListener('mousedown', onClick)
    return () => document.removeEventListener('mousedown', onClick)
  }, [open, close])

  return (
    <div className="relative" data-account-chip>
      <button
        onClick={() => setOpen(v => !v)}
        className="flex items-center gap-1.5 px-1.5 py-0.5 rounded-sm transition-colors hover:opacity-80"
        style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
        title={`Signed in as ${user.login}`}
      >
        {user.avatar_url ? (
          <img src={user.avatar_url} alt="" className="w-5 h-5 rounded-full" />
        ) : (
          <span
            className="w-5 h-5 rounded-full flex items-center justify-center text-[10px] font-semibold"
            style={{ background: 'hsl(var(--brand))', color: 'white' }}
          >
            {user.login.charAt(0).toUpperCase()}
          </span>
        )}
        <span className="text-xs font-medium">{user.login}</span>
      </button>
      {open && (
        <div
          className="absolute right-0 top-full mt-1 min-w-[160px] rounded-md shadow-lg border z-50"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))' }}
        >
          <button
            onClick={async () => {
              await signOut()
              window.location.reload()
            }}
            className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-[hsl(var(--bg-primary))] transition-colors"
            style={{ color: 'hsl(var(--text-high))' }}
          >
            <LogOut size={12} />
            Sign out
          </button>
        </div>
      )}
    </div>
  )
}

// ReportIssueButton sits in the top bar and spawns a `clawflow feedback`
// session in the user's native terminal. The actual issue gets filed on
// zhoushoujianwork/clawflow by Claude during the chat — we only launch it here.
function ReportIssueButton() {
  const { open } = useChatDrawer()
  return (
    <button
      onClick={() => { void open({ feedback: true }) }}
      className="w-7 h-7 flex items-center justify-center rounded-sm transition-colors hover:opacity-80"
      style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
      aria-label="Report an issue"
      title="Report an issue"
    >
      <svg
        viewBox="0 0 16 16"
        className="w-3.5 h-3.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M14 9.5a1.5 1.5 0 0 1-1.5 1.5h-6L3 14V4.5A1.5 1.5 0 0 1 4.5 3h8A1.5 1.5 0 0 1 14 4.5v5z" />
        <path d="M8 5.5v3" />
        <path d="M6.5 7h3" />
      </svg>
    </button>
  )
}
