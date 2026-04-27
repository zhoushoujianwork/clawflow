import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { ExternalLink } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'

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

export const Route = createFileRoute('/_app/repos/')({
  component: RepoList,
})

function RepoList() {
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('/data/repos.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .then(r => setRepos(Array.isArray(r) ? r : []))
      .catch(() => setRepos([]))
      .finally(() => setLoading(false))
  }, [])

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

      {loading ? (
        <p className="text-sm text-muted-foreground text-center py-8">Loading…</p>
      ) : repos.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <p className="text-sm text-muted-foreground">
            No repos yet. Run <code className="px-1.5 py-0.5 bg-secondary rounded text-xs font-mono">clawflow repo add &lt;owner/repo&gt;</code>.
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
              {repos.map(r => (
                <tr key={r.full_name} className="hover:bg-secondary/20">
                  <td className="px-4 py-2">
                    <div className="flex items-center gap-2">
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
