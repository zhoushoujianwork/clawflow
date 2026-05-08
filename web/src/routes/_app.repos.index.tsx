import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useMemo, useState, useCallback } from 'react'
import { FolderOpen, Plus, Trash2, Link2, Link2Off, Search, X } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { useConfigChanged } from '../lib/configEvents'

interface Repo {
  full_name: string
  platform?: Platform
  base_url?: string
  base_branch: string
  local_path?: string
  enabled: boolean
  auto_approve: boolean
  auto_merge: boolean
  bound_machine?: string
}

interface RunEntry {
  repo: string
  started_at: string
}

const PROVIDER_KEY = 'clawflow.repos.provider'

function timeAgo(iso: string): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (!isFinite(t)) return '—'
  const diff = Math.floor((Date.now() - t) / 1000)
  if (diff < 0) return 'just now'
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}min ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

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
  const [runs, setRuns] = useState<RunEntry[]>([])
  const [ideScheme, setIdeScheme] = useState('vscode://file/')
  const [loading, setLoading] = useState(true)
  const [didApplyDefault, setDidApplyDefault] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [removing, setRemoving] = useState(false)
  const [binding, setBinding] = useState(false)
  const [query, setQuery] = useState('')

  const loadAll = useCallback(() => {
    return Promise.all([
      fetch('/data/repos.json', { cache: 'no-store' })
        .then(r => (r.ok ? r.json() : []))
        .catch(() => []),
      fetch('/data/runs.json', { cache: 'no-store' })
        .then(r => (r.ok ? r.json() : []))
        .catch(() => []),
      fetch('/api/settings', { cache: 'no-store' })
        .then(r => (r.ok ? r.json() : null))
        .catch(() => null),
    ]).then(([rp, rn, settings]) => {
      setRepos(Array.isArray(rp) ? rp : [])
      setRuns(Array.isArray(rn) ? rn : [])
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
      setLoading(false)
    })
  }, [])

  useEffect(() => { loadAll() }, [loadAll])
  useConfigChanged(loadAll)

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

  // Build a map of repo → most recent run timestamp
  const lastActivityMap = useMemo<Record<string, string>>(() => {
    const m: Record<string, string> = {}
    for (const r of runs) {
      if (!r.repo || !r.started_at) continue
      if (!m[r.repo] || r.started_at > m[r.repo]) {
        m[r.repo] = r.started_at
      }
    }
    return m
  }, [runs])

  const filtered = useMemo(() => {
    let list = active === 'all' ? [...repos] : repos.filter(r => (r.platform || 'github') === active)
    const q = query.trim().toLowerCase()
    if (q) {
      list = list.filter(r =>
        r.full_name.toLowerCase().includes(q) ||
        (r.local_path || '').toLowerCase().includes(q) ||
        (r.base_branch || '').toLowerCase().includes(q) ||
        (r.bound_machine || '').toLowerCase().includes(q),
      )
    }
    list.sort((a, b) => {
      const ta = lastActivityMap[a.full_name] || ''
      const tb = lastActivityMap[b.full_name] || ''
      if (ta !== tb) return tb.localeCompare(ta)
      return a.full_name.localeCompare(b.full_name)
    })
    return list
  }, [repos, active, lastActivityMap, query])

  // Clear selection when filter changes (so stale selections from another tab don't linger)
  useEffect(() => {
    setSelected(new Set())
  }, [active])

  const toggleSelect = useCallback((name: string) => {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }, [])

  const toggleSelectAll = useCallback(() => {
    setSelected(prev => {
      if (prev.size === filtered.length) return new Set()
      return new Set(filtered.map(r => r.full_name))
    })
  }, [filtered])

  const handleRemoveSelected = useCallback(async () => {
    if (selected.size === 0) return
    const names = Array.from(selected)
    const confirmed = window.confirm(
      `确认移除 ${names.length} 个仓库？\n\n${names.join('\n')}\n\n（仅从配置中移除，不会删除本地项目文件）`
    )
    if (!confirmed) return

    setRemoving(true)
    try {
      const resp = await fetch('/api/repo/remove', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repos: names }),
      })
      if (resp.ok) {
        setRepos(prev => prev.filter(r => !selected.has(r.full_name)))
        setSelected(new Set())
      } else {
        const data = await resp.json().catch(() => ({}))
        alert(data.error || '移除失败')
      }
    } catch {
      alert('网络错误，请重试')
    } finally {
      setRemoving(false)
    }
  }, [selected])

  const handleBatchBind = useCallback(async (bind: boolean) => {
    if (selected.size === 0 || binding) return
    setBinding(true)
    try {
      const resp = await fetch('/api/repo/bind', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repos: Array.from(selected), bind }),
      })
      if (resp.ok) {
        const data = await resp.json()
        const results: Record<string, string> = data.results || {}
        setRepos(prev => prev.map(r =>
          selected.has(r.full_name)
            ? { ...r, bound_machine: results[r.full_name] || undefined }
            : r
        ))
        setSelected(new Set())
      }
    } catch { /* ignore */ }
    finally { setBinding(false) }
  }, [selected, binding])

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
    <div className="max-w-6xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-5">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Monitored repos</h1>
          <p className="text-xs text-muted-foreground mt-1">
            Read-only view of <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">~/.clawflow/config/config.yaml</code>.
            Use <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">clawflow repo add</code> to add more.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {selected.size > 0 && (
            <>
              <button
                type="button"
                onClick={() => handleBatchBind(true)}
                disabled={binding}
                className="inline-flex items-center gap-2 px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-semibold hover:bg-blue-700 transition-colors disabled:opacity-50"
              >
                <Link2 className="w-4 h-4" />
                {binding ? 'Binding…' : `Bind (${selected.size})`}
              </button>
              <button
                type="button"
                onClick={() => handleBatchBind(false)}
                disabled={binding}
                className="inline-flex items-center gap-2 px-4 py-2 bg-secondary text-foreground rounded-lg text-sm font-semibold hover:bg-secondary/80 transition-colors disabled:opacity-50"
              >
                <Link2Off className="w-4 h-4" />
                {binding ? 'Unbinding…' : `Unbind (${selected.size})`}
              </button>
            </>
          )}
          {selected.size > 0 && (
            <button
              type="button"
              onClick={handleRemoveSelected}
              disabled={removing}
              className="inline-flex items-center gap-2 px-4 py-2 bg-destructive text-destructive-foreground rounded-lg text-sm font-semibold hover:bg-destructive/90 transition-colors disabled:opacity-50"
            >
              <Trash2 className="w-4 h-4" />
              {removing ? '移除中…' : `移除 (${selected.size})`}
            </button>
          )}
          <Link
            to="/repos/add"
            className="inline-flex items-center gap-2 px-4 py-2 bg-primary text-primary-foreground rounded-lg text-sm font-semibold hover:bg-primary/90 transition-colors"
          >
            <Plus className="w-4 h-4" />
            Add from VCS
          </Link>
        </div>
      </div>

      {(showTabs || repos.length > 0) && (
        <div className="flex items-center gap-3 mb-4 flex-wrap">
          {showTabs && (
            <div className="inline-flex bg-card border border-border rounded-xl overflow-hidden">
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
          <div className="relative flex-1 min-w-[200px] max-w-sm">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground pointer-events-none" />
            <input
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              placeholder="Search repo, branch, path…"
              aria-label="Search repos"
              className="w-full bg-card border border-border rounded-lg pl-8 pr-8 py-1.5 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-blue-500/30 focus:border-blue-400"
            />
            {query && (
              <button
                type="button"
                onClick={() => setQuery('')}
                aria-label="Clear search"
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            )}
          </div>
          {query && (
            <span className="text-xs text-muted-foreground tabular-nums">
              {filtered.length} match{filtered.length === 1 ? '' : 'es'}
            </span>
          )}
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
          {query ? (
            <p className="text-sm text-muted-foreground">
              No repos match <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">{query}</code>.{' '}
              <button type="button" onClick={() => setQuery('')} className="underline hover:text-foreground">Clear search</button>
            </p>
          ) : (
            <p className="text-sm text-muted-foreground">
              No <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">{active}</code> repos. Pick another tab or add one with{' '}
              <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">clawflow repo add</code>.
            </p>
          )}
        </div>
      ) : (
        <div className="bg-card border border-border rounded-xl overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-secondary/30 text-xs uppercase text-muted-foreground">
              <tr>
                <th className="text-left px-4 py-2 w-10">
                  <input
                    type="checkbox"
                    checked={filtered.length > 0 && selected.size === filtered.length}
                    onChange={toggleSelectAll}
                    className="rounded border-border"
                    aria-label="Select all repos"
                  />
                </th>
                <th className="text-left px-4 py-2 font-semibold">Repo</th>
                <th className="text-left px-4 py-2 font-semibold">Last activity</th>
                <th className="text-left px-4 py-2 font-semibold">Platform</th>
                <th className="text-left px-4 py-2 font-semibold">Base</th>
                <th className="text-left px-4 py-2 font-semibold">Enabled</th>
                <th className="text-left px-4 py-2 font-semibold">Auto-approve</th>
                <th className="text-left px-4 py-2 font-semibold">Auto-merge</th>
                <th className="text-left px-4 py-2 font-semibold">Bound</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {filtered.map(r => (
                <tr key={r.full_name} className={cn("hover:bg-secondary/20", selected.has(r.full_name) && "bg-secondary/30")}>
                  <td className="px-4 py-2">
                    <input
                      type="checkbox"
                      checked={selected.has(r.full_name)}
                      onChange={() => toggleSelect(r.full_name)}
                      className="rounded border-border"
                      aria-label={`Select ${r.full_name}`}
                    />
                  </td>
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
                        className="inline-flex items-center text-muted-foreground hover:text-foreground shrink-0"
                      >
                        <VcsIcon repo={r.full_name} map={repoMap} className="w-3.5 h-3.5" />
                      </a>
                      {r.local_path && (
                        <a
                          href={`${ideScheme}${r.local_path}?windowId=_blank`}
                          title={`Open in IDE: ${r.local_path}`}
                          className="inline-flex items-center text-muted-foreground hover:text-foreground shrink-0"
                        >
                          <FolderOpen className="w-3.5 h-3.5" />
                        </a>
                      )}
                    </div>
                  </td>
                  <td className="px-4 py-2 text-muted-foreground text-xs tabular-nums">
                    {lastActivityMap[r.full_name] ? timeAgo(lastActivityMap[r.full_name]) : '—'}
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">{r.platform || 'github'}</td>
                  <td className="px-4 py-2 text-muted-foreground font-mono text-xs">{r.base_branch}</td>
                  <td className="px-4 py-2">
                    <Pill on={r.enabled}>{r.enabled ? 'enabled' : 'disabled'}</Pill>
                  </td>
                  <td className="px-4 py-2">
                    <Pill on={r.auto_approve}>{r.auto_approve ? 'on' : 'off'}</Pill>
                  </td>
                  <td className="px-4 py-2">
                    <Pill on={r.auto_merge}>{r.auto_merge ? 'on' : 'off'}</Pill>
                  </td>
                  <td className="px-4 py-2">
                    {r.bound_machine ? (
                      <span
                        className="inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-semibold border bg-blue-100 text-blue-700 border-blue-200 truncate max-w-[120px]"
                        title={r.bound_machine}
                      >
                        {r.bound_machine}
                      </span>
                    ) : (
                      <span
                        className="inline-flex items-center px-1.5 py-0.5 rounded text-[11px] border border-dashed border-muted-foreground/40 text-muted-foreground"
                        title="Not bound to any machine"
                      >
                        unbound
                      </span>
                    )}
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
