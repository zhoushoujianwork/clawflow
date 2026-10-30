import { createFileRoute, Link } from '@tanstack/react-router'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { ChevronLeft, ExternalLink, MessageSquare, Download, Loader2, Link2, Link2Off, FolderOpen, FolderKanban, AlertTriangle } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { useChatDrawer } from '../lib/chatContext'
import { useConfigChanged } from '../lib/configEvents'
import { findProjectsForRepo, type ProjectEntry } from '../lib/projectMembership'
import { GitSyncCell, type GitStatus } from '../components/GitSyncCell'
import { ContextWorkLinks } from '../components/ContextWorkLinks'

interface Repo {
  full_name: string
  platform?: Platform
  base_url?: string
  base_branch: string
  local_path?: string
  enabled: boolean
  auto_merge: boolean
  bound_machine?: string
  primary_project?: string
  base_branch_valid?: boolean
  base_branch_hint?: string
}
export const Route = createFileRoute('/_app/repos/$')({
  component: RepoDetail,
})

function RepoDetail() {
  const { _splat } = Route.useParams()
  // Splat segment preserves `/` literally — `org/repo` arrives intact with
  // no encoding, so no decodeURIComponent dance and no `%2F`/`%252F`.
  const fullName = _splat ?? ''
  useDocumentTitle(fullName || undefined)
  const chatDrawer = useChatDrawer()

  const [repo, setRepo] = useState<Repo | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [cloning, setCloning] = useState(false)
  const [cloneError, setCloneError] = useState<string | null>(null)
  const [binding, setBinding] = useState(false)
  const [hostname, setHostname] = useState<string>('')
  const [ideScheme, setIdeScheme] = useState('vscode://file/')
  const [owningProjects, setOwningProjects] = useState<string[]>([])
  // Git sync status for this single repo. Seeded from the cached
  // /api/repo/git-status (instant) then refreshed in the background. Mirrors
  // the index page's gitStatus map, scoped to one repo.
  const [gitStatus, setGitStatus] = useState<GitStatus | undefined>(undefined)

  const cloneNow = useCallback(() => {
    if (!repo || cloning) return
    setCloning(true)
    setCloneError(null)
    fetch('/api/repo/clone', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo: fullName }),
    })
      .then(async r => {
        const data = await r.json().catch(() => null)
        if (!r.ok || !data) {
          throw new Error((data && data.error) || `HTTP ${r.status}`)
        }
        return data
      })
      .then(d => {
        if (d.status === 'ok' && d.local_path) {
          setRepo(prev => prev ? { ...prev, local_path: d.local_path } : prev)
        }
      })
      .catch(err => setCloneError(String(err.message || err)))
      .finally(() => setCloning(false))
  }, [repo, fullName, cloning])

  const refreshData = useCallback(() => {
    return Promise.all([
      fetch('/data/repos.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/api/settings', { cache: 'no-store' }).then(r => (r.ok ? r.json() : null)).catch(() => null),
      fetch('/data/projects.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/api/repo/git-status', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
    ]).then(([repos, settings, projects, gitStatuses]) => {
      const match = (Array.isArray(repos) ? repos : []).find((x: Repo) => x.full_name === fullName) || null
      setRepo(match)
      if (settings?.global?.hostname) setHostname((settings.global.hostname as string).toLowerCase().replace(/\.local$/, ''))
      if (settings?.global?.default_ide) {
        const ide = settings.global.default_ide as string
        const schemeMap: Record<string, string> = {
          vscode: 'vscode://file/',
          cursor: 'cursor://file/',
          qoder: 'qoder://file/',
          'vscode-insiders': 'vscode-insiders://file/',
        }
        setIdeScheme(schemeMap[ide] ?? 'vscode://file/')
      }
      setOwningProjects(findProjectsForRepo(fullName, Array.isArray(projects) ? projects as ProjectEntry[] : []))
      if (Array.isArray(gitStatuses)) {
        setGitStatus((gitStatuses as GitStatus[]).find(s => s?.repo === fullName))
      }
    })
  }, [fullName])

  // Apply this repo's refreshed git status (called by GitSyncCell on pull/push).
  const updateGitStatus = useCallback((s: GitStatus) => {
    if (s?.repo !== fullName) return
    setGitStatus(s)
  }, [fullName])

  const toggleBind = useCallback(() => {
    if (!repo || binding) return
    const shouldBind = !repo.bound_machine
    setBinding(true)
    fetch('/api/repo/bind', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo: fullName, bind: shouldBind }),
    })
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d && d.status === 'ok') {
          setRepo(prev => prev ? { ...prev, bound_machine: d.bound_machine || undefined } : prev)
        }
      })
      .catch(() => {})
      .finally(() => setBinding(false))
  }, [repo, fullName, binding])

  const toggleConfig = useCallback((field: 'enabled' | 'auto_merge') => {
    if (!repo || saving) return
    const newVal = !(repo as any)[field]
    setSaving(true)
    fetch('/api/repo/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo: fullName, [field]: newVal }),
    })
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d && d.status === 'ok') {
          setRepo(prev => prev ? { ...prev, [field]: newVal } : prev)
        }
      })
      .catch(() => {})
      .finally(() => setSaving(false))
  }, [repo, fullName, saving])

  useEffect(() => {
    refreshData().then(() => setLoading(false))
  }, [fullName, refreshData])
  useConfigChanged(refreshData)

  // Background git fetch+recompute for this repo so ahead/behind reflects the
  // latest remote without blocking first paint. Runs once per fullName.
  useEffect(() => {
    if (!fullName) return
    let cancelled = false
    fetch('/api/repo/git-status/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo: fullName }),
    })
      .then(r => (r.ok ? r.json() : []))
      .then((arr: GitStatus[]) => {
        if (cancelled || !Array.isArray(arr)) return
        const s = arr.find(x => x?.repo === fullName)
        if (s) setGitStatus(s)
      })
      .catch(() => { /* background best-effort */ })
    return () => { cancelled = true }
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

  // "Mine" = bound to the hostname this dashboard is running on. Until
  // hostname has loaded we err on "mine" so the page doesn't briefly
  // flash a disabled state for repos that are in fact bound here.
  const boundToMe = !hostname || (!!repo?.bound_machine && repo.bound_machine === hostname)
  const inactive = !boundToMe

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
              <span className="inline-flex items-center gap-1">
                <VcsIcon repo={repo.full_name} map={repoMap} className="w-3.5 h-3.5 shrink-0" />
                {repo.platform || 'github'}
              </span>
              <span>·</span>
              {repo.base_branch_valid === false ? (
                <span
                  className="inline-flex items-center gap-1 font-mono text-destructive"
                  title={repo.base_branch_hint || 'base_branch does not exist on origin'}
                >
                  <AlertTriangle className="w-3.5 h-3.5 shrink-0" aria-hidden="true" />
                  base: {repo.base_branch}
                  <span className="sr-only">
                    {repo.base_branch_hint || 'base_branch does not exist on origin'}
                  </span>
                </span>
              ) : (
                <span className="font-mono">base: {repo.base_branch}</span>
              )}
              {owningProjects.length === 1 && (
                <>
                  <span>·</span>
                  <Link
                    to="/projects/$name"
                    params={{ name: owningProjects[0] }}
                    title={`Belongs to project: ${owningProjects[0]}`}
                    aria-label={`Open owning project: ${owningProjects[0]}`}
                    className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
                  >
                    <FolderKanban className="w-3 h-3" /> {owningProjects[0]}
                  </Link>
                </>
              )}
              {owningProjects.length > 1 && (() => {
                // Mirror the backend rule (FindProjectByRepo): the primary
                // project is the configured one, else the lexicographically
                // first owning project.
                const fallbackPrimary = [...owningProjects].sort()[0]
                const primaryName = repo.primary_project || fallbackPrimary
                return owningProjects.map(pname => {
                  const isPrimary = pname === primaryName
                  return (
                  <span key={pname}>
                    <span>·</span>
                    <Link
                      to="/projects/$name"
                      params={{ name: pname }}
                      title={isPrimary ? `Primary project (context source): ${pname}` : `Belongs to project: ${pname}`}
                      aria-label={`Open owning project: ${pname}`}
                      className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline ml-1"
                    >
                      <FolderKanban className="w-3 h-3" /> {pname}{isPrimary ? ' ★' : ''}
                    </Link>
                  </span>
                  )
                })
              })()}
              {repoVcsUrl && (
                <>
                  <span>·</span>
                  <a href={repoVcsUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline">
                    view <ExternalLink className="w-3 h-3" />
                  </a>
                </>
              )}
              <span>·</span>
              <button
                onClick={() => chatDrawer.open({ repo: fullName })}
                className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
              >
                <MessageSquare className="w-3 h-3" /> chat
              </button>
              {repo.local_path && (
                <>
                  <span>·</span>
                  <a
                    href={`${ideScheme}${repo.local_path}?windowId=_blank`}
                    title={`Open in IDE: ${repo.local_path}`}
                    className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
                  >
                    <FolderOpen className="w-3 h-3" /> open
                  </a>
                </>
              )}
              <span>·</span>
              <GitSyncCell repo={fullName} status={gitStatus} onUpdate={updateGitStatus} />
            </div>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-2 mb-6">
            <ToggleCard label="Status" enabled={repo.enabled} onToggle={() => toggleConfig('enabled')} disabled={saving || inactive} />
            <ToggleCard label="Auto-merge" enabled={repo.auto_merge} onToggle={() => toggleConfig('auto_merge')} disabled={saving || inactive} />
            <div className="bg-card border border-border rounded-xl p-3 min-w-0">
              <div className="text-xs text-muted-foreground">Local path</div>
              {repo.local_path ? (
                <div className="text-xs font-mono mt-1.5 text-foreground truncate" title={repo.local_path}>
                  {repo.local_path}
                </div>
              ) : (
                <button
                  type="button"
                  onClick={cloneNow}
                  disabled={cloning}
                  title={cloneError ?? 'git clone into the default location and save the path'}
                  className={cn(
                    'mt-1 inline-flex items-center gap-1 text-xs font-medium px-2 py-1 rounded border transition-colors',
                    cloning
                      ? 'border-border text-muted-foreground bg-secondary/30'
                      : cloneError
                        ? 'border-red-200 text-red-700 bg-red-50 hover:bg-red-100'
                        : 'border-border text-foreground hover:bg-secondary/50',
                  )}
                >
                  {cloning ? (
                    <>
                      <Loader2 className="w-3 h-3 animate-spin" /> cloning…
                    </>
                  ) : cloneError ? (
                    <>retry clone</>
                  ) : (
                    <>
                      <Download className="w-3 h-3" /> Clone now
                    </>
                  )}
                </button>
              )}
              {cloneError && !cloning && (
                <div className="mt-1 text-[11px] text-red-600 truncate" title={cloneError}>
                  {cloneError}
                </div>
              )}
            </div>
            {/* Bind to this machine */}
            <button
              onClick={toggleBind}
              disabled={binding}
              title={repo.bound_machine
                ? `Bound to ${repo.bound_machine} — click to unbind`
                : 'Bind this repo to the current machine so other machines skip it'}
              className={cn(
                'bg-card border rounded-xl p-3 text-left transition-all',
                repo.bound_machine ? 'border-blue-300' : 'border-border',
                binding ? 'opacity-60 cursor-not-allowed' : 'hover:shadow-sm cursor-pointer',
              )}
            >
              <div className="flex items-center justify-between">
                <div className="text-xs text-muted-foreground">Bound machine</div>
                {binding ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin text-muted-foreground" />
                ) : repo.bound_machine ? (
                  <Link2 className="w-3.5 h-3.5 text-blue-500" />
                ) : (
                  <Link2Off className="w-3.5 h-3.5 text-muted-foreground/50" />
                )}
              </div>
              {repo.bound_machine ? (
                <div className="text-xs font-mono mt-1.5 text-blue-600 truncate" title={repo.bound_machine}>
                  {repo.bound_machine}
                </div>
              ) : (
                <div className="text-base font-semibold mt-0.5 text-muted-foreground">unbound</div>
              )}
            </button>
          </div>

          <ContextWorkLinks repo={fullName} />
          <RepositoryPolicy fullName={fullName} />

        </>
      )}
    </div>
  )
}


function ToggleCard({ label, enabled, onToggle, disabled }: {
  label: string
  enabled: boolean
  onToggle: () => void
  disabled?: boolean
}) {
  return (
    <button
      onClick={onToggle}
      disabled={disabled}
      className={cn(
        'bg-card border rounded-xl p-3 text-left transition-all',
        enabled ? 'border-green-300' : 'border-border',
        disabled ? 'opacity-60 cursor-not-allowed' : 'hover:shadow-sm cursor-pointer',
      )}
    >
      <div className="flex items-center justify-between">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div
          className={cn(
            'w-8 h-[18px] rounded-full transition-colors relative',
            enabled ? 'bg-green-500' : 'bg-muted-foreground/30',
          )}
        >
          <div
            className={cn(
              'absolute top-[2px] w-[14px] h-[14px] rounded-full bg-white transition-transform shadow-sm',
              enabled ? 'translate-x-[16px]' : 'translate-x-[2px]',
            )}
          />
        </div>
      </div>
      <div className={cn('text-base font-semibold mt-0.5', enabled ? 'text-green-600' : 'text-muted-foreground')}>
        {enabled ? (label === 'Status' ? 'enabled' : 'on') : (label === 'Status' ? 'disabled' : 'off')}
      </div>
    </button>
  )
}

// Read the existing pipeline contract; absent evidence stays unknown.
function RepositoryPolicy({ fullName }: { fullName: string }) {
  const [policy, setPolicy] = useState<{ ci_required?: boolean; acceptance_required?: boolean } | null>(null)
  useEffect(() => {
    let cancelled = false
    setPolicy(null)
    fetch('/api/pipeline', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : null)
      .then(data => { if (!cancelled) setPolicy(data?.repos?.find((r: { repo: string }) => r.repo === fullName) ?? null) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [fullName])
  const status = (value?: boolean) => value === undefined ? '未报告' : value ? '必需' : '非必需'
  return <section aria-label="交付策略" className="border rounded-xl p-4 mt-4 text-sm">
    <h2 className="font-semibold mb-2">交付策略</h2>
    <p>CI：{status(policy?.ci_required)} · 独立验收：{status(policy?.acceptance_required)}</p>
    <p className="text-muted-foreground mt-2">使用 clawflow repo set 管理 CI / 验收策略；自动合并开关保留在上方。</p>
  </section>
}
