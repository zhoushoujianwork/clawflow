import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { ExternalLink } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'

interface Repo {
  full_name: string
  platform?: Platform
  base_url?: string
  base_branch: string
  local_path?: string
  enabled: boolean
  auto_fix: boolean
  auto_merge: boolean
}

const PROVIDER_KEY = 'clawflow.repos.provider'

type RepoSearch = { provider?: string }

export const Route = createFileRoute('/_app/repos/')({
  component: RepoList,
  validateSearch: (s: Record<string, unknown>): RepoSearch => {
    return typeof s.provider === 'string' ? { provider: s.provider } : {}
  },
})

function RepoList() {
  const { provider } = Route.useSearch()
  const navigate = Route.useNavigate()

  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [didApplyDefault, setDidApplyDefault] = useState(false)

  useEffect(() => {
    fetch('/data/repos.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .then(r => setRepos(Array.isArray(r) ? r : []))
      .catch(() => setRepos([]))
      .finally(() => setLoading(false))
  }, [])

  const counts = useMemo(() => {
    const c = new Map<string, number>()
    for (const r of repos) {
      const p = r.platform || 'github'
      c.set(p, (c.get(p) || 0) + 1)
    }
    return c
  }, [repos])

  const platformKeys = useMemo(() => Array.from(counts.keys()).sort(), [counts])

  // Apply persisted default once after data loads, only if URL has no
  // explicit provider. If the stored value points to a platform that no
  // longer has any repos, we silently fall through to "all".
  useEffect(() => {
    if (loading || didApplyDefault) return
    setDidApplyDefault(true)
    if (provider !== undefined) return
    if (typeof window === 'undefined') return
    const stored = window.localStorage.getItem(PROVIDER_KEY)
    if (!stored) return
    if (stored === 'all' || counts.has(stored)) {
      navigate({ search: { provider: stored }, replace: true })
    }
  }, [loading, didApplyDefault, provider, counts, navigate])

  const active = provider || 'all'

  function pickProvider(next: string) {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(PROVIDER_KEY, next)
    }
    navigate({ search: { provider: next } })
  }

  const filtered = useMemo(() => {
    if (active === 'all') return repos
    return repos.filter(r => (r.platform || 'github') === active)
  }, [repos, active])

  const repoMap = useMemo<RepoInfoMap>(() => {
    const m: RepoInfoMap = {}
    for (const r of repos) {
      const platform: Platform = r.platform || 'github'
      const defaultHost = platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com'
      m[r.full_name] = {
        platform,
        host: (r.base_url || defaultHost).replace(/\/$/, ''),
      }
    }
    return m
  }, [repos])

  const showTabs = repos.length > 0 && platformKeys.length > 1

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-5">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Monitored repos</h1>
          <p className="text-xs text-muted-foreground mt-1">
            Read-only view of <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">~/.clawflow/config/config.yaml</code>.
            Use <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">clawflow repo add</code> to add more.
          </p>
        </div>
      </div>

      {showTabs && (
        <div className="inline-flex bg-card border border-border rounded-xl overflow-hidden mb-4">
          <TabButton active={active === 'all'} onClick={() => pickProvider('all')}>
            All <span className="ml-1 text-xs opacity-60 tabular-nums">{repos.length}</span>
          </TabButton>
          {platformKeys.map(p => (
            <TabButton key={p} active={active === p} onClick={() => pickProvider(p)}>
              <span className="capitalize">{p}</span>
              <span className="ml-1 text-xs opacity-60 tabular-nums">{counts.get(p)}</span>
            </TabButton>
          ))}
        </div>
      )}

      {loading ? (
        <p className="text-sm text-muted-foreground text-center py-8">Loading…</p>
      ) : repos.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <p className="text-sm text-muted-foreground">
            No repos yet. Run <code className="px-1.5 py-0.5 bg-secondary rounded text-xs font-mono">clawflow repo add &lt;owner/repo&gt;</code>.
          </p>
        </div>
      ) : filtered.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <p className="text-sm text-muted-foreground">
            No <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">{active}</code> repos. Pick another tab or add one with{' '}
            <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">clawflow repo add</code>.
          </p>
        </div>
      ) : (
        <div className="bg-card border border-border rounded-xl overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-secondary/30 text-xs uppercase text-muted-foreground">
              <tr>
                <th className="text-left px-4 py-2 font-semibold">Repo</th>
                <th className="text-left px-4 py-2 font-semibold">Platform</th>
                <th className="text-left px-4 py-2 font-semibold">Base</th>
                <th className="text-left px-4 py-2 font-semibold">Enabled</th>
                <th className="text-left px-4 py-2 font-semibold">Auto-fix</th>
                <th className="text-left px-4 py-2 font-semibold">Auto-merge</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {filtered.map(r => (
                <tr key={r.full_name} className="hover:bg-secondary/20">
                  <td className="px-4 py-2">
                    <div className="flex items-center gap-2">
                      <VcsIcon
                        repo={r.full_name}
                        map={repoMap}
                        className="w-3.5 h-3.5 text-muted-foreground shrink-0"
                      />
                      <Link
                        to="/repos/$repoName"
                        params={{ repoName: encodeURIComponent(r.full_name) }}
                        className="font-mono text-foreground hover:underline"
                      >
                        {r.full_name}
                      </Link>
                      <a
                        href={repoUrl(r.full_name, repoMap)}
                        target="_blank"
                        rel="noopener noreferrer"
                        title="Open in VCS"
                        className="inline-flex items-center text-muted-foreground hover:text-foreground"
                      >
                        <ExternalLink className="w-3 h-3" />
                      </a>
                    </div>
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">{r.platform || 'github'}</td>
                  <td className="px-4 py-2 text-muted-foreground font-mono text-xs">{r.base_branch}</td>
                  <td className="px-4 py-2">
                    <Pill on={r.enabled}>{r.enabled ? 'enabled' : 'disabled'}</Pill>
                  </td>
                  <td className="px-4 py-2">
                    <Pill on={r.auto_fix}>{r.auto_fix ? 'on' : 'off'}</Pill>
                  </td>
                  <td className="px-4 py-2">
                    <Pill on={r.auto_merge}>{r.auto_merge ? 'on' : 'off'}</Pill>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function TabButton({
  active, onClick, children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'px-3 py-1.5 text-sm border-r border-border last:border-r-0 transition-colors',
        active
          ? 'bg-secondary text-foreground font-semibold'
          : 'text-muted-foreground hover:text-foreground hover:bg-secondary/50',
      )}
    >
      {children}
    </button>
  )
}

function Pill({ on, children }: { on: boolean; children: React.ReactNode }) {
  return (
    <span
      className={cn(
        'inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-semibold border',
        on ? 'bg-green-100 text-green-700 border-green-200' : 'bg-muted text-muted-foreground border-border',
      )}
    >
      {children}
    </span>
  )
}
