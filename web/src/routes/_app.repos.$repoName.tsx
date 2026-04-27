import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { ChevronLeft, ExternalLink } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, issueUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'

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

interface Run {
  operator: string
  repo: string
  issue_number: number
  issue_title?: string
  started_at: string
  ended_at?: string
  status: 'success' | 'failed' | 'skipped' | 'running'
  summary?: string
  path: string
  pr_url?: string
  error?: string
}

export const Route = createFileRoute('/_app/repos/$repoName')({
  component: RepoDetail,
})

function RepoDetail() {
  const { repoName } = Route.useParams()
  const fullName = decodeURIComponent(repoName)

  const [repo, setRepo] = useState<Repo | null>(null)
  const [runs, setRuns] = useState<Run[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    Promise.all([
      fetch('/data/repos.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/data/runs.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
    ]).then(([repos, allRuns]) => {
      const match = (Array.isArray(repos) ? repos : []).find((x: Repo) => x.full_name === fullName) || null
      setRepo(match)
      setRuns((Array.isArray(allRuns) ? allRuns : []).filter((r: Run) => r.repo === fullName))
      setLoading(false)
    })
  }, [fullName])

  const repoMap = useMemo<RepoInfoMap>(() => {
    if (!repo) return {}
    const platform: Platform = repo.platform || 'github'
    const defaultHost = platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com'
    return {
      [repo.full_name]: {
        platform,
        host: (repo.base_url || defaultHost).replace(/\/$/, ''),
      },
    }
  }, [repo])

  const repoVcsUrl = repo ? repoUrl(repo.full_name, repoMap) : null

  const slug = fullName.replace(/\//g, '__')

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <Link to="/repos" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground mb-4">
        <ChevronLeft className="w-3.5 h-3.5" /> Repos
      </Link>

      {loading ? (
        <p className="text-sm text-muted-foreground py-8">Loading…</p>
      ) : !repo ? (
        <div className="bg-card border border-border rounded-xl p-6 text-sm text-muted-foreground">
          Repo <code className="font-mono text-foreground">{fullName}</code> is not in your config. Run{' '}
          <code className="px-1 py-0.5 bg-secondary rounded font-mono">clawflow repo add {fullName}</code>.
        </div>
      ) : (
        <>
          <div className="mb-6">
            <h1 className="text-2xl font-bold text-foreground font-mono">{repo.full_name}</h1>
            <div className="flex items-center gap-2 mt-1 text-xs text-muted-foreground">
              <span>{repo.platform || 'github'}</span>
              <span>·</span>
              <span className="font-mono">base: {repo.base_branch}</span>
              {repoVcsUrl && (
                <>
                  <span>·</span>
                  <a href={repoVcsUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline">
                    view <ExternalLink className="w-3 h-3" />
                  </a>
                </>
              )}
            </div>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 mb-6">
            <StatusCard label="Status" value={repo.enabled ? 'enabled' : 'disabled'} tone={repo.enabled ? 'green' : 'muted'} />
            <StatusCard label="Auto-fix" value={repo.auto_fix ? 'on' : 'off'} tone={repo.auto_fix ? 'green' : 'muted'} />
            <StatusCard label="Auto-merge" value={repo.auto_merge ? 'on' : 'off'} tone={repo.auto_merge ? 'green' : 'muted'} />
            <StatusCard label="Local path" value={repo.local_path ? '✓' : '—'} tone={repo.local_path ? 'neutral' : 'muted'} />
          </div>

          {repo.local_path && (
            <div className="bg-card border border-border rounded-xl p-3 mb-6 text-xs font-mono text-muted-foreground">
              {repo.local_path}
            </div>
          )}

          <section>
            <h2 className="text-sm font-semibold text-foreground mb-2">
              Recent runs <span className="font-normal text-muted-foreground">({runs.length})</span>
            </h2>
            {runs.length === 0 ? (
              <p className="text-sm text-muted-foreground py-4">
                No runs yet for this repo. Run <code className="px-1 py-0.5 bg-secondary rounded font-mono">clawflow run --repo {repo.full_name}</code>.
              </p>
            ) : (
              <div className="bg-card border border-border rounded-xl overflow-hidden divide-y divide-border">
                {runs.map(r => (
                  <Link
                    key={r.path}
                    to="/runs/$slug/$issue/$ts"
                    params={{ slug, issue: `issue-${r.issue_number}`, ts: r.path.replace(/\/$/, '').split('/').pop() || '' }}
                    className="flex items-center gap-3 px-4 py-2 hover:bg-secondary/30"
                  >
                    <a
                      href={issueUrl(r.repo, r.issue_number, repoMap)}
                      target="_blank"
                      rel="noopener noreferrer"
                      onClick={e => e.stopPropagation()}
                      className="font-mono text-xs text-muted-foreground hover:text-foreground hover:underline shrink-0 w-12"
                    >
                      #{r.issue_number}
                    </a>
                    <span className={cn(
                      'inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-semibold border shrink-0',
                      r.status === 'success' && 'bg-green-100 text-green-700 border-green-200',
                      r.status === 'failed' && 'bg-red-100 text-red-700 border-red-200',
                      r.status === 'skipped' && 'bg-muted text-muted-foreground border-border',
                      r.status === 'running' && 'bg-blue-100 text-blue-700 border-blue-200',
                    )}>{r.status}</span>
                    <span className="text-sm truncate flex-1">{r.operator} · {r.issue_title || '(no title)'}</span>
                    <span className="text-xs text-muted-foreground shrink-0 tabular-nums">{new Date(r.started_at).toLocaleString()}</span>
                  </Link>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function StatusCard({ label, value, tone }: { label: string; value: string; tone: 'green' | 'muted' | 'neutral' }) {
  const toneCls = {
    green: 'text-green-600',
    muted: 'text-muted-foreground',
    neutral: 'text-foreground',
  }[tone]
  return (
    <div className="bg-card border border-border rounded-xl p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={cn('text-base font-semibold mt-0.5', toneCls)}>{value}</div>
    </div>
  )
}
