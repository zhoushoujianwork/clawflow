import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { FolderGit2, Plus, RefreshCw, Trash2, Loader2, Search, LogIn, MessageSquare } from 'lucide-react'
import { useChatDrawer } from '../lib/chatContext'
import {
  deleteRepo,
  fetchBindings,
  fetchMachines,
  fetchProjects,
  fetchRepos,
  timeAgo,
  type Binding,
  type Machine,
  type Project,
  type Repo,
} from '../lib/cloudApi'
import { VcsIcon } from '../components/VcsIcon'
import type { RepoInfoMap, Platform } from '../lib/vcsUrls'

export const Route = createFileRoute('/_app/repos/')({
  component: ReposPage,
})

// PlatformFilter is the top-tab selector. 'all' is the default; the others
// match the literal `Repo.platform` values from the cloud Store.
type PlatformFilter = 'all' | 'github' | 'gitlab'

// ProjectFilter is keyed by project id, plus the synthetic 'all' (default)
// and 'orphan' (repos with no project_id) buckets.
type ProjectFilter = string

function ReposPage() {
  const navigate = useNavigate()
  const chatDrawer = useChatDrawer()
  const [repos, setRepos] = useState<Repo[]>([])
  const [projects, setProjects] = useState<Project[]>([])
  const [bindings, setBindings] = useState<Binding[]>([])
  const [machines, setMachines] = useState<Machine[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [unauth, setUnauth] = useState(false)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [platform, setPlatform] = useState<PlatformFilter>('all')
  const [projectF, setProjectF] = useState<ProjectFilter>('all')
  const [query, setQuery] = useState('')

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    setUnauth(false)
    Promise.all([fetchRepos(), fetchProjects(), fetchBindings(), fetchMachines()])
      .then(([r, p, b, m]) => {
        setRepos(r.repos ?? [])
        setProjects(p.projects ?? [])
        setBindings(b.bindings ?? [])
        setMachines(m.machines ?? [])
      })
      .catch(e => {
        // 401 = "no session"; 404 = "endpoint missing" (a local
        // `clawflow web` doesn't serve /api/cloud/*); JSON parse
        // errors on HTML responses look like SyntaxError from a raw
        // doctype. All three cases are "this Repos UI needs cloud
        // sign-in" — render the Sign-in panel instead of leaking the
        // raw error string.
        const s = String(e)
        if (
          s.includes('401') ||
          s.includes('404') ||
          s.includes('Unexpected token') ||
          s.includes('not valid JSON')
        ) {
          setUnauth(true)
        } else {
          setError(s)
        }
      })
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const projectName = useCallback(
    (id?: string) => {
      if (!id) return '—'
      const p = projects.find(p => p.id === id)
      return p ? p.name : id
    },
    [projects],
  )

  // For each repo, look up its most-recently-updated binding and resolve
  // the machine name from it. Repos with no binding get an em dash.
  const lastMachineForRepo = useCallback(
    (repoId: string): { name: string; updated_at: string } | null => {
      const matching = bindings.filter(b => b.repo_id === repoId)
      if (matching.length === 0) return null
      const newest = matching.reduce((a, b) =>
        (a.updated_at || '') > (b.updated_at || '') ? a : b,
      )
      const m = machines.find(m => m.id === newest.machine_id)
      return {
        name: m ? (m.display_name || m.hostname) : newest.machine_id,
        updated_at: newest.updated_at,
      }
    },
    [bindings, machines],
  )

  // Reuse the existing VcsIcon by feeding it a minimal RepoInfoMap built
  // from the cloud repo list (we only know platform here — no base_url).
  const repoMap = useMemo<RepoInfoMap>(() => {
    const m: RepoInfoMap = {}
    for (const r of repos) {
      const platform: Platform = r.platform === 'gitlab' ? 'gitlab' : 'github'
      m[r.name] = {
        platform,
        host: platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com',
      }
    }
    return m
  }, [repos])

  // Apply platform / project / query filters in a stable order so the
  // filter chips can show live counts that reflect the OTHER filters
  // already in effect (i.e. counts on the platform tabs respect the
  // current project + search filter).
  const platformCount = useCallback(
    (p: PlatformFilter) => {
      return repos.filter(r => {
        if (p !== 'all' && r.platform !== p) return false
        if (projectF === 'all') {
          // nothing
        } else if (projectF === 'orphan') {
          if (r.project_id) return false
        } else if (r.project_id !== projectF) {
          return false
        }
        if (query && !r.name.toLowerCase().includes(query.toLowerCase())) return false
        return true
      }).length
    },
    [repos, projectF, query],
  )

  const projectCount = useCallback(
    (pf: ProjectFilter) => {
      return repos.filter(r => {
        if (platform !== 'all' && r.platform !== platform) return false
        if (pf === 'all') {
          // nothing
        } else if (pf === 'orphan') {
          if (r.project_id) return false
        } else if (r.project_id !== pf) {
          return false
        }
        if (query && !r.name.toLowerCase().includes(query.toLowerCase())) return false
        return true
      }).length
    },
    [repos, platform, query],
  )

  const filtered = useMemo(() => {
    return repos.filter(r => {
      if (platform !== 'all' && r.platform !== platform) return false
      if (projectF === 'all') {
        // nothing
      } else if (projectF === 'orphan') {
        if (r.project_id) return false
      } else if (r.project_id !== projectF) {
        return false
      }
      if (query && !r.name.toLowerCase().includes(query.toLowerCase())) return false
      return true
    })
  }, [repos, platform, projectF, query])

  const handleDelete = useCallback(
    async (repo: Repo) => {
      const confirmed = window.confirm(
        `Delete repo "${repo.name}" from the cloud?\n\nThis removes its bindings too. Local clones on worker machines are not touched.`,
      )
      if (!confirmed) return
      setDeletingId(repo.id)
      try {
        await deleteRepo(repo.id)
        load()
      } catch (e) {
        setError(String(e))
      } finally {
        setDeletingId(null)
      }
    },
    [load],
  )

  return (
    <div className="px-6 py-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-base font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
          Repos
        </h1>
        <div className="flex items-center gap-2">
          <button
            onClick={load}
            disabled={loading}
            className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
            style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
          >
            <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
            Refresh
          </button>
          <button
            onClick={() => navigate({ to: '/repos/add' })}
            className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors"
            style={{
              borderColor: 'hsl(var(--brand))',
              color: 'hsl(var(--brand))',
              background: 'hsl(var(--brand) / 0.08)',
            }}
          >
            <Plus size={12} />
            Add repo
          </button>
        </div>
      </div>

      {unauth && <SignInPanel />}

      {error && (
        <div
          className="mb-4 px-4 py-3 rounded-md text-sm border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
        >
          {error}
        </div>
      )}

      {!loading && !unauth && repos.length === 0 && !error && <EmptyRepos />}

      {repos.length > 0 && (
        <>
          {/* Filter chip row: platforms + projects + free-text search. */}
          <div className="flex flex-wrap items-center gap-2 mb-3">
            {([
              { id: 'all',    label: 'All' },
              { id: 'github', label: 'GitHub' },
              { id: 'gitlab', label: 'GitLab' },
            ] as { id: PlatformFilter; label: string }[]).map(opt => {
              const active = platform === opt.id
              const count = platformCount(opt.id)
              return (
                <button
                  key={opt.id}
                  onClick={() => setPlatform(opt.id)}
                  className="text-xs px-2.5 py-1 rounded-full transition-colors border font-medium"
                  style={{
                    background: active ? 'hsl(var(--brand) / 0.1)' : 'transparent',
                    color: active ? 'hsl(var(--brand))' : 'hsl(var(--text-low))',
                    borderColor: active ? 'hsl(var(--brand) / 0.4)' : 'hsl(var(--border))',
                  }}
                >
                  {opt.label}
                  <span className="ml-1 opacity-70">{count}</span>
                </button>
              )
            })}
            <span className="mx-1" style={{ color: 'hsl(var(--border))' }}>·</span>
            {([
              { id: 'all',    label: 'All projects' },
              ...projects.map(p => ({ id: p.id as ProjectFilter, label: p.name })),
              { id: 'orphan', label: 'No project' },
            ]).map(opt => {
              const active = projectF === opt.id
              const count = projectCount(opt.id)
              return (
                <button
                  key={opt.id}
                  onClick={() => setProjectF(opt.id)}
                  className="text-xs px-2.5 py-1 rounded-full transition-colors border font-medium"
                  style={{
                    background: active ? 'hsl(var(--brand) / 0.1)' : 'transparent',
                    color: active ? 'hsl(var(--brand))' : 'hsl(var(--text-low))',
                    borderColor: active ? 'hsl(var(--brand) / 0.4)' : 'hsl(var(--border))',
                  }}
                >
                  {opt.label}
                  <span className="ml-1 opacity-70">{count}</span>
                </button>
              )
            })}
            <div className="ml-auto relative">
              <Search
                size={12}
                className="absolute left-2.5 top-1/2 -translate-y-1/2"
                style={{ color: 'hsl(var(--text-low))' }}
              />
              <input
                type="text"
                placeholder="Filter by name…"
                value={query}
                onChange={e => setQuery(e.target.value)}
                className="text-xs pl-7 pr-2 py-1 rounded-sm border bg-transparent w-44 focus:outline-none focus:ring-1"
                style={{
                  borderColor: 'hsl(var(--border))',
                  color: 'hsl(var(--text-high))',
                }}
              />
            </div>
          </div>
        </>
      )}

      {repos.length > 0 && (
        <div
          className="rounded-lg border overflow-hidden"
          style={{ borderColor: 'hsl(var(--border))' }}
        >
          <table className="w-full text-sm">
            <thead>
              <tr
                className="text-xs font-medium border-b"
                style={{
                  background: 'hsl(var(--bg-panel))',
                  borderColor: 'hsl(var(--border))',
                  color: 'hsl(var(--text-low))',
                }}
              >
                <th className="text-left px-4 py-2 w-8"></th>
                <th className="text-left px-4 py-2">Name</th>
                <th className="text-left px-4 py-2">Base branch</th>
                <th className="text-left px-4 py-2">Project</th>
                <th className="text-left px-4 py-2">Last bound machine</th>
                <th className="text-left px-4 py-2 w-20">Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.length === 0 && (
                <tr>
                  <td
                    colSpan={6}
                    className="px-4 py-8 text-center text-xs"
                    style={{ color: 'hsl(var(--text-low))' }}
                  >
                    No repos match the current filter.
                  </td>
                </tr>
              )}
              {filtered.map((r, i) => {
                const lastMachine = lastMachineForRepo(r.id)
                return (
                  <tr
                    key={r.id}
                    className="border-b last:border-b-0"
                    style={{
                      borderColor: 'hsl(var(--border))',
                      background: i % 2 === 0 ? 'transparent' : 'hsl(var(--bg-panel) / 0.4)',
                    }}
                  >
                    <td className="px-4 py-2.5">
                      <VcsIcon repo={r.name} map={repoMap} className="w-3.5 h-3.5 shrink-0" />
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs" style={{ color: 'hsl(var(--text-high))' }}>
                      <Link
                        to="/repos/$repoName"
                        params={{ repoName: encodeURIComponent(r.name) }}
                        className="hover:underline"
                      >
                        {r.name}
                      </Link>
                    </td>
                    <td
                      className="px-4 py-2.5 font-mono text-xs"
                      style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}
                    >
                      {r.base_branch || '—'}
                    </td>
                    <td className="px-4 py-2.5 text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {projectName(r.project_id)}
                    </td>
                    <td className="px-4 py-2.5 text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {lastMachine ? (
                        <span className="inline-flex items-center gap-1.5">
                          <span className="font-mono" style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
                            {lastMachine.name}
                          </span>
                          <span style={{ color: 'hsl(var(--text-low))' }}>
                            ({timeAgo(lastMachine.updated_at)})
                          </span>
                        </span>
                      ) : (
                        '—'
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex items-center gap-0.5">
                        <button
                          onClick={() => { void chatDrawer.open({ repo: r.name }) }}
                          title={`Chat with claude about ${r.name}`}
                          className="inline-flex items-center justify-center w-7 h-7 rounded transition-colors"
                          style={{ color: 'hsl(var(--text-low))' }}
                        >
                          <MessageSquare size={14} />
                        </button>
                        <button
                          onClick={() => handleDelete(r)}
                          disabled={deletingId === r.id}
                          title={`Delete ${r.name}`}
                          className="inline-flex items-center justify-center w-7 h-7 rounded transition-colors disabled:opacity-50"
                          style={{ color: 'hsl(var(--text-low))' }}
                        >
                          {deletingId === r.id ? (
                            <Loader2 size={14} className="animate-spin" />
                          ) : (
                            <Trash2 size={14} />
                          )}
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// SignInPanel replaces the raw 401 JSON error with a friendly "you need
// to sign in" call-to-action. /api/v1/github/app/login is a server-side
// redirect endpoint, so a plain anchor element is enough.
function SignInPanel() {
  return (
    <div
      className="rounded-lg border px-6 py-12 text-center"
      style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
    >
      <LogIn size={32} className="mx-auto mb-3 opacity-30" style={{ color: 'hsl(var(--text-low))' }} />
      <p className="text-sm font-medium mb-1" style={{ color: 'hsl(var(--text-high))' }}>
        Sign in to view your repos
      </p>
      <p className="text-xs mb-4" style={{ color: 'hsl(var(--text-low))' }}>
        ClawFlow uses your GitHub identity. The repos you've registered with this cloud appear once you're signed in.
      </p>
      <a
        href="/api/v1/github/app/login"
        className="inline-flex items-center text-xs font-medium px-3 py-1.5 rounded-sm"
        style={{ background: 'hsl(var(--brand))', color: 'white' }}
      >
        Sign in with GitHub
      </a>
    </div>
  )
}

function EmptyRepos() {
  return (
    <div
      className="rounded-lg border px-6 py-12 text-center"
      style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
    >
      <FolderGit2 size={32} className="mx-auto mb-3 opacity-30" style={{ color: 'hsl(var(--text-low))' }} />
      <p className="text-sm font-medium mb-1" style={{ color: 'hsl(var(--text-high))' }}>
        No repos registered
      </p>
      <p className="text-xs mb-4" style={{ color: 'hsl(var(--text-low))' }}>
        Add a repo with the button above, or import existing config from the CLI.
      </p>
      <div
        className="inline-block text-left rounded-md px-3 py-2 font-mono text-xs border"
        style={{ background: 'hsl(var(--bg-primary))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
      >
        clawflow cloud import
      </div>
    </div>
  )
}
