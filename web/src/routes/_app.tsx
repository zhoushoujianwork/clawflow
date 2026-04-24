import { createFileRoute, Outlet, Link } from '@tanstack/react-router'
import { BookOpen } from 'lucide-react'
import { useTheme } from '../lib/useTheme'

export const Route = createFileRoute('/_app')({
  component: AppLayout,
})

function AppLayout() {
  const { theme, toggle } = useTheme()

  return (
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
            <span className="text-[10px] font-normal ml-1" style={{ color: 'hsl(var(--text-low))' }}>
              local
            </span>
          </a>
          <nav className="flex gap-1">
            <Link
              to="/dashboard"
              className="text-sm font-medium px-2.5 py-1 rounded-sm transition-colors"
              style={{ color: 'hsl(var(--text-low))' }}
              activeProps={{ style: { color: 'hsl(var(--brand))', background: 'hsl(var(--brand) / 0.08)' } }}
            >
              Dashboard
            </Link>
            <Link
              to="/repos"
              className="text-sm font-medium px-2.5 py-1 rounded-sm transition-colors"
              style={{ color: 'hsl(var(--text-low))' }}
              activeProps={{ style: { color: 'hsl(var(--brand))', background: 'hsl(var(--brand) / 0.08)' } }}
            >
              Repos
            </Link>
            <Link
              to="/operators"
              className="text-sm font-medium px-2.5 py-1 rounded-sm transition-colors"
              style={{ color: 'hsl(var(--text-low))' }}
              activeProps={{ style: { color: 'hsl(var(--brand))', background: 'hsl(var(--brand) / 0.08)' } }}
            >
              Operators
            </Link>
          </nav>
        </div>

        <div className="flex items-center gap-2">
          <a
            href="https://github.com/zhoushoujianwork/clawflow"
            target="_blank"
            rel="noopener noreferrer"
            className="w-7 h-7 flex items-center justify-center rounded-sm transition-colors hover:opacity-80"
            style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
            aria-label="GitHub"
            title="GitHub"
          >
            <BookOpen className="w-3.5 h-3.5" />
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
    </div>
  )
}
