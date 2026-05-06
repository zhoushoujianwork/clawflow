import { createFileRoute, Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { ArrowLeft, Search, Loader2, CheckCircle2, XCircle, ExternalLink, ChevronRight, ChevronDown, RefreshCw, KeyRound, FolderCheck } from 'lucide-react'
import { cn } from '../lib/utils'
import { VcsIcon } from '../components/VcsIcon'

interface RemoteRepo {
  full_name: string
  platform: string
  description: string
  default_branch: string
  private: boolean
  html_url: string
  base_url?: string
  local_path?: string
}

type Platform = 'github' | 'gitlab'

type ReposAddSearch = {
  platform?: Platform
}

export const Route = createFileRoute('/_app/repos/add')({
  component: AddRemoteRepo,
  validateSearch: (search: Record<string, unknown>): ReposAddSearch => {
    return {
      platform: search.platform === 'gitlab' ? 'gitlab' : 'github',
    }
  },
})

interface RepoGroup {
  groupPath: string
  repos: RemoteRepo[]
}

function AddRemoteRepo() {
  const navigate = useNavigate({ from: Route.fullPath })
  const search = useSearch({ from: Route.id })
  const platform = search.platform || 'github'

  // Cache: Map<platform, repos[]>
  const [reposCache, setReposCache] = useState<Map<Platform, RemoteRepo[]>>(new Map())
  const [fetchedPlatforms, setFetchedPlatforms] = useState<Set<Platform>>(new Set())
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [tokenMissing, setTokenMissing] = useState<Set<Platform>>(new Set())
  const [searchQuery, setSearchQuery] = useState('')
  const [addingRepo, setAddingRepo] = useState<string | null>(null)
  const [addedRepos, setAddedRepos] = useState<Set<string>>(new Set())
  const [preExistingRepos, setPreExistingRepos] = useState<Set<string>>(new Set())
  const [addErrors, setAddErrors] = useState<Map<string, string>>(new Map())
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())

  const repos = reposCache.get(platform) || []

  // Fetch local config repos on mount to mark already-added ones
  useEffect(() => {
    fetch('/data/repos.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .then(data => {
        if (Array.isArray(data)) {
          const names = new Set(data.map((r: { full_name: string }) => r.full_name))
          setAddedRepos(names)
          setPreExistingRepos(names)
        }
      })
      .catch(() => {})
  }, [])

  useEffect(() => {
    // Only fetch if not already fetched
    if (!fetchedPlatforms.has(platform)) {
      fetchRepos()
    }
  }, [platform, fetchedPlatforms])

  async function fetchRepos() {
    setLoading(true)
    setError(null)
    setFetchedPlatforms(prev => new Set(prev).add(platform))

    try {
      const response = await fetch(`/api/repos/list-remote?platform=${platform}`)
      const data = await response.json()

      // Handle token not configured (returned as 200 with token_configured=false)
      if (data.token_configured === false) {
        setTokenMissing(prev => new Set(prev).add(platform))
        setReposCache(prev => new Map(prev).set(platform, []))
        return
      }

      if (!response.ok) {
        throw new Error(data.error || 'Failed to fetch repositories')
      }

      if (data.error) {
        throw new Error(data.error)
      }

      // Token is configured and working
      setTokenMissing(prev => {
        const next = new Set(prev)
        next.delete(platform)
        return next
      })

      const fetchedRepos = data.repos || []
      setReposCache(prev => new Map(prev).set(platform, fetchedRepos))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
      // On error, remove from fetched set so it can be retried
      setFetchedPlatforms(prev => {
        const next = new Set(prev)
        next.delete(platform)
        return next
      })
    } finally {
      setLoading(false)
    }
  }

  function handlePlatformChange(newPlatform: Platform) {
    navigate({ search: { platform: newPlatform } })
  }

  function handleRefresh() {
    // Clear cache, fetched flag, and token-missing state for current platform, then re-fetch
    setReposCache(prev => {
      const next = new Map(prev)
      next.delete(platform)
      return next
    })
    setFetchedPlatforms(prev => {
      const next = new Set(prev)
      next.delete(platform)
      return next
    })
    setTokenMissing(prev => {
      const next = new Set(prev)
      next.delete(platform)
      return next
    })
    fetchRepos()
  }

  async function addRepo(repo: RemoteRepo) {
    setAddingRepo(repo.full_name)
    setAddErrors(prev => {
      const next = new Map(prev)
      next.delete(repo.full_name)
      return next
    })

    try {
      const response = await fetch('/api/repos/add-remote', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          full_name: repo.full_name,
          platform: repo.platform,
          default_branch: repo.default_branch,
          description: repo.description,
          base_url: repo.base_url,
        }),
      })

      const data = await response.json()

      if (!response.ok || data.error) {
        throw new Error(data.error || 'Failed to add repository')
      }

      setAddedRepos(prev => new Set(prev).add(repo.full_name))
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unknown error'
      setAddErrors(prev => new Map(prev).set(repo.full_name, message))
    } finally {
      setAddingRepo(null)
    }
  }

  function extractGroupPath(fullName: string): string {
    const lastSlashIndex = fullName.lastIndexOf('/')
    if (lastSlashIndex === -1) return '' // Top-level repo (no group)
    return fullName.substring(0, lastSlashIndex)
  }

  function groupRepos(repos: RemoteRepo[]): RepoGroup[] {
    const groupMap = new Map<string, RemoteRepo[]>()

    repos.forEach(repo => {
      const groupPath = extractGroupPath(repo.full_name)
      if (!groupMap.has(groupPath)) {
        groupMap.set(groupPath, [])
      }
      groupMap.get(groupPath)!.push(repo)
    })

    return Array.from(groupMap.entries())
      .map(([groupPath, repos]) => ({ groupPath, repos }))
      .sort((a, b) => {
        // Top-level repos (empty groupPath) come first
        if (a.groupPath === '' && b.groupPath !== '') return -1
        if (a.groupPath !== '' && b.groupPath === '') return 1
        return a.groupPath.localeCompare(b.groupPath)
      })
  }

  function toggleGroup(groupPath: string) {
    setExpandedGroups(prev => {
      const next = new Set(prev)
      if (next.has(groupPath)) {
        next.delete(groupPath)
      } else {
        next.add(groupPath)
      }
      return next
    })
  }

  const filteredRepos = repos.filter(repo => {
    if (!searchQuery) return true
    const q = searchQuery.toLowerCase()
    return (
      repo.full_name.toLowerCase().includes(q) ||
      (repo.description && repo.description.toLowerCase().includes(q))
    )
  })

  const groupedRepos = groupRepos(filteredRepos)

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <div className="mb-6">
        <Link
          to="/repos"
          className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground mb-4"
        >
          <ArrowLeft className="w-4 h-4" />
          Back to repos
        </Link>
        <h1 className="text-2xl font-bold text-foreground">Add repository from VCS</h1>
        <p className="text-sm text-muted-foreground mt-1">
          Browse and clone repositories from your GitHub or GitLab account
        </p>
      </div>

      {/* Platform selector with refresh button */}
      <div className="flex items-center gap-3 mb-4">
        <div className="inline-flex bg-card border border-border rounded-xl overflow-hidden">
          <PlatformButton
            active={platform === 'github'}
            onClick={() => handlePlatformChange('github')}
          >
            GitHub
          </PlatformButton>
          <PlatformButton
            active={platform === 'gitlab'}
            onClick={() => handlePlatformChange('gitlab')}
          >
            GitLab
          </PlatformButton>
        </div>
        <button
          type="button"
          onClick={handleRefresh}
          disabled={loading}
          className={cn(
            'p-2 rounded-lg border border-border bg-card transition-colors',
            loading
              ? 'text-muted-foreground cursor-not-allowed'
              : 'text-foreground hover:bg-secondary/50'
          )}
          title="Refresh repositories"
        >
          <RefreshCw className={cn('w-4 h-4', loading && 'animate-spin')} />
        </button>
      </div>

      {/* Search bar */}
      {!loading && !error && repos.length > 0 && (
        <div className="relative mb-4">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
          <input
            type="text"
            placeholder="Search repositories..."
            value={searchQuery}
            onChange={e => setSearchQuery(e.target.value)}
            className="w-full pl-10 pr-4 py-2 bg-card border border-border rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-primary"
          />
        </div>
      )}

      {/* Loading state */}
      {loading && (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <Loader2 className="w-6 h-6 animate-spin mx-auto mb-2 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">Loading repositories...</p>
        </div>
      )}

      {/* Error state */}
      {error && (
        <div className="bg-red-50 border border-red-200 rounded-xl p-4">
          <div className="flex items-start gap-3">
            <XCircle className="w-5 h-5 text-red-600 shrink-0 mt-0.5" />
            <div>
              <p className="text-sm font-semibold text-red-900">Failed to load repositories</p>
              <p className="text-sm text-red-700 mt-1">{error}</p>
              {error.includes('token') && (
                <p className="text-xs text-red-600 mt-2">
                  Configure your {platform === 'github' ? 'GitHub' : 'GitLab'} token in{' '}
                  <Link to="/settings" className="underline font-semibold">
                    Settings
                  </Link>
                </p>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Token not configured state */}
      {!loading && !error && tokenMissing.has(platform) && (
        <div className="bg-amber-50 border border-amber-200 rounded-xl p-6">
          <div className="flex items-start gap-4">
            <div className="p-2 bg-amber-100 rounded-lg">
              <KeyRound className="w-5 h-5 text-amber-600" />
            </div>
            <div>
              <p className="text-sm font-semibold text-amber-900">
                {platform === 'github' ? 'GitHub' : 'GitLab'} Token Not Configured
              </p>
              <p className="text-sm text-amber-700 mt-1">
                A {platform === 'github' ? 'GitHub' : 'GitLab'} personal access token is required to list and clone repositories.
                Without it, cloning will fall back to SSH keys (which may not be configured).
              </p>
              <Link
                to="/settings"
                className="inline-flex items-center gap-1.5 mt-3 px-3 py-1.5 text-sm font-semibold text-amber-800 bg-amber-100 hover:bg-amber-200 border border-amber-300 rounded-lg transition-colors"
              >
                <KeyRound className="w-3.5 h-3.5" />
                Configure in Settings
              </Link>
            </div>
          </div>
        </div>
      )}

      {/* Empty state */}
      {!loading && !error && !tokenMissing.has(platform) && repos.length === 0 && (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <p className="text-sm text-muted-foreground">
            No repositories found for your {platform === 'github' ? 'GitHub' : 'GitLab'} account.
          </p>
        </div>
      )}

      {/* Repos list */}
      {!loading && !error && filteredRepos.length > 0 && (
        <div className="bg-card border border-border rounded-xl overflow-hidden">
          <div className="divide-y divide-border">
            {groupedRepos.map(group => {
              const isExpanded = expandedGroups.has(group.groupPath)
              const groupDisplayName = group.groupPath || 'Top-level repositories'

              return (
                <div key={group.groupPath}>
                  {/* Group header */}
                  <button
                    type="button"
                    onClick={() => toggleGroup(group.groupPath)}
                    className="w-full px-4 py-3 flex items-center gap-2 hover:bg-secondary/20 transition-colors text-left"
                  >
                    {isExpanded ? (
                      <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" />
                    ) : (
                      <ChevronRight className="w-4 h-4 text-muted-foreground shrink-0" />
                    )}
                    <span className="font-semibold text-sm text-foreground">
                      {groupDisplayName}
                    </span>
                    <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-semibold bg-secondary text-muted-foreground">
                      {group.repos.length}
                    </span>
                  </button>

                  {/* Repos in group */}
                  {isExpanded && (
                    <div className="divide-y divide-border">
                      {group.repos.map(repo => {
                        const isAdding = addingRepo === repo.full_name
                        const isAdded = addedRepos.has(repo.full_name)
                        const addError = addErrors.get(repo.full_name)
                        const hasLocalClone = !!repo.local_path
                        const repoName = group.groupPath
                          ? repo.full_name.substring(group.groupPath.length + 1)
                          : repo.full_name

                        return (
                          <div
                            key={repo.full_name}
                            className="pl-10 pr-4 py-4 hover:bg-secondary/20 transition-colors"
                          >
                            <div className="flex items-start justify-between gap-4">
                              <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-2 mb-1">
                                  <VcsIcon
                                    repo={repo.full_name}
                                    map={{
                                      [repo.full_name]: {
                                        platform: repo.platform as 'github' | 'gitlab',
                                        host: repo.platform === 'github' ? 'https://github.com' : 'https://gitlab.com',
                                      },
                                    }}
                                    className="w-4 h-4 text-muted-foreground shrink-0"
                                  />
                                  <h3 className="font-mono text-sm font-semibold text-foreground truncate">
                                    {repoName}
                                  </h3>
                                  <a
                                    href={repo.html_url}
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    title="Open in VCS"
                                    className="inline-flex items-center text-muted-foreground hover:text-foreground"
                                  >
                                    <ExternalLink className="w-3 h-3" />
                                  </a>
                                  {repo.private && (
                                    <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-yellow-100 text-yellow-700 border border-yellow-200">
                                      Private
                                    </span>
                                  )}
                                  {hasLocalClone && !isAdded && (
                                    <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-semibold bg-blue-50 text-blue-700 border border-blue-200">
                                      <FolderCheck className="w-3 h-3" />
                                      Local clone found
                                    </span>
                                  )}
                                </div>
                                {repo.description && (
                                  <p className="text-xs text-muted-foreground line-clamp-2 mb-1">
                                    {repo.description}
                                  </p>
                                )}
                                <p className="text-xs text-muted-foreground">
                                  Default branch: <code className="px-1 py-0.5 bg-secondary rounded font-mono">{repo.default_branch}</code>
                                  {hasLocalClone && !isAdded && (
                                    <span className="ml-2 text-blue-600" title={repo.local_path}>
                                      · {repo.local_path}
                                    </span>
                                  )}
                                </p>
                                {addError && (
                                  <p className="text-xs text-red-600 mt-2 flex items-start gap-1">
                                    <XCircle className="w-3 h-3 shrink-0 mt-0.5" />
                                    <span>{addError}</span>
                                  </p>
                                )}
                              </div>
                              <div className="shrink-0">
                                {isAdded ? (
                                  <div className={cn(
                                    "flex items-center gap-2",
                                    preExistingRepos.has(repo.full_name) ? "text-muted-foreground" : "text-green-600"
                                  )}>
                                    <CheckCircle2 className="w-4 h-4" />
                                    <span className="text-sm font-semibold">
                                      {preExistingRepos.has(repo.full_name) ? 'Added' : 'Just added'}
                                    </span>
                                  </div>
                                ) : (
                                  <button
                                    type="button"
                                    onClick={() => addRepo(repo)}
                                    disabled={isAdding}
                                    className={cn(
                                      'px-4 py-2 rounded-lg text-sm font-semibold transition-colors',
                                      isAdding
                                        ? 'bg-secondary text-muted-foreground cursor-not-allowed'
                                        : hasLocalClone
                                          ? 'bg-blue-600 text-white hover:bg-blue-700'
                                          : 'bg-primary text-primary-foreground hover:bg-primary/90',
                                    )}
                                  >
                                    {isAdding ? (
                                      <span className="flex items-center gap-2">
                                        <Loader2 className="w-4 h-4 animate-spin" />
                                        {hasLocalClone ? 'Linking...' : 'Cloning...'}
                                      </span>
                                    ) : (
                                      hasLocalClone ? 'Link' : 'Add'
                                    )}
                                  </button>
                                )}
                              </div>
                            </div>
                          </div>
                        )
                      })}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {!loading && !error && repos.length > 0 && filteredRepos.length === 0 && (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <p className="text-sm text-muted-foreground">
            No repositories match your search query.
          </p>
        </div>
      )}
    </div>
  )
}

function PlatformButton({
  active,
  onClick,
  children,
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
        'px-4 py-2 text-sm border-r border-border last:border-r-0 transition-colors',
        active
          ? 'bg-secondary text-foreground font-semibold'
          : 'text-muted-foreground hover:text-foreground hover:bg-secondary/50',
      )}
    >
      {children}
    </button>
  )
}
