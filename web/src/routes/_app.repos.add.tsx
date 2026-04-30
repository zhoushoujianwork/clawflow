import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { ArrowLeft, Search, Loader2, CheckCircle2, XCircle, ExternalLink } from 'lucide-react'
import { cn } from '../lib/utils'
import { VcsIcon } from '../components/VcsIcon'

interface RemoteRepo {
  full_name: string
  platform: string
  description: string
  default_branch: string
  private: boolean
  html_url: string
}

type Platform = 'github' | 'gitlab'

export const Route = createFileRoute('/_app/repos/add')({
  component: AddRemoteRepo,
})

function AddRemoteRepo() {
  const [platform, setPlatform] = useState<Platform>('github')
  const [repos, setRepos] = useState<RemoteRepo[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [addingRepo, setAddingRepo] = useState<string | null>(null)
  const [addedRepos, setAddedRepos] = useState<Set<string>>(new Set())
  const [addErrors, setAddErrors] = useState<Map<string, string>>(new Map())

  useEffect(() => {
    fetchRepos()
  }, [platform])

  async function fetchRepos() {
    setLoading(true)
    setError(null)
    setRepos([])

    try {
      const response = await fetch(`/api/repos/list-remote?platform=${platform}`)
      const data = await response.json()

      if (!response.ok) {
        throw new Error(data.error || 'Failed to fetch repositories')
      }

      if (data.error) {
        throw new Error(data.error)
      }

      setRepos(data.repos || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
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

  const filteredRepos = repos.filter(repo => {
    if (!searchQuery) return true
    const q = searchQuery.toLowerCase()
    return (
      repo.full_name.toLowerCase().includes(q) ||
      (repo.description && repo.description.toLowerCase().includes(q))
    )
  })

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

      {/* Platform selector */}
      <div className="inline-flex bg-card border border-border rounded-xl overflow-hidden mb-4">
        <PlatformButton
          active={platform === 'github'}
          onClick={() => setPlatform('github')}
        >
          GitHub
        </PlatformButton>
        <PlatformButton
          active={platform === 'gitlab'}
          onClick={() => setPlatform('gitlab')}
        >
          GitLab
        </PlatformButton>
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

      {/* Empty state */}
      {!loading && !error && repos.length === 0 && (
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
            {filteredRepos.map(repo => {
              const isAdding = addingRepo === repo.full_name
              const isAdded = addedRepos.has(repo.full_name)
              const addError = addErrors.get(repo.full_name)

              return (
                <div
                  key={repo.full_name}
                  className="p-4 hover:bg-secondary/20 transition-colors"
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
                          {repo.full_name}
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
                      </div>
                      {repo.description && (
                        <p className="text-xs text-muted-foreground line-clamp-2 mb-1">
                          {repo.description}
                        </p>
                      )}
                      <p className="text-xs text-muted-foreground">
                        Default branch: <code className="px-1 py-0.5 bg-secondary rounded font-mono">{repo.default_branch}</code>
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
                        <div className="flex items-center gap-2 text-green-600">
                          <CheckCircle2 className="w-4 h-4" />
                          <span className="text-sm font-semibold">Added</span>
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
                              : 'bg-primary text-primary-foreground hover:bg-primary/90',
                          )}
                        >
                          {isAdding ? (
                            <span className="flex items-center gap-2">
                              <Loader2 className="w-4 h-4 animate-spin" />
                              Cloning...
                            </span>
                          ) : (
                            'Add'
                          )}
                        </button>
                      )}
                    </div>
                  </div>
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
