import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useMemo, useState, useCallback, useRef } from 'react'
import { createPortal } from 'react-dom'
import { FolderOpen, Plus, Trash2, Link2, Link2Off, Search, X, Check, Loader2 } from 'lucide-react'
import { cn } from '../lib/utils'
import { repoUrl, type RepoInfoMap, type Platform } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { GitSyncCell, type GitStatus } from '../components/GitSyncCell'
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
  // Git sync status keyed by repo full_name. Seeded from the cached
  // /api/repo/git-status (instant) then refreshed in the background (the
  // Hook: git fetch + recompute) so the user sees ahead/behind without
  // waiting on a live fetch per render.
  const [gitStatus, setGitStatus] = useState<Record<string, GitStatus>>({})
  const [ideScheme, setIdeScheme] = useState('vscode://file/')
  const [hostname, setHostname] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [didApplyDefault, setDidApplyDefault] = useState(false)
  const [query, setQuery] = useState('')
  // Per-row "work in progress" indicators, keyed by full_name + field.
  // Kept as a Set so toggling one repo doesn't block the rest.
  const [busy, setBusy] = useState<Set<string>>(new Set())
  const [bindMenuFor, setBindMenuFor] = useState<string | null>(null)

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
      fetch('/api/repo/git-status', { cache: 'no-store' })
        .then(r => (r.ok ? r.json() : []))
        .catch(() => []),
    ]).then(([rp, rn, settings, gs]) => {
      setRepos(Array.isArray(rp) ? rp : [])
      setRuns(Array.isArray(rn) ? rn : [])
      if (Array.isArray(gs)) {
        const m: Record<string, GitStatus> = {}
        for (const s of gs as GitStatus[]) if (s?.repo) m[s.repo] = s
        setGitStatus(m)
      }
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
      if (settings?.global?.hostname) setHostname(settings.global.hostname as string)
      setLoading(false)
    })
  }, [])

  useEffect(() => { loadAll() }, [loadAll])
  useConfigChanged(loadAll)

  // Apply a single repo's refreshed git status into the map.
  const updateGitStatus = useCallback((s: GitStatus) => {
    if (!s?.repo) return
    setGitStatus(prev => ({ ...prev, [s.repo]: s }))
  }, [])

  // Hook: once repos are loaded, kick off a background fetch+recompute for
  // all of them so ahead/behind reflects the latest remote without blocking
  // first paint. Runs once per mount; the cache covers subsequent renders and
  // the 5-min web backstop keeps it warm.
  const didRefreshGit = useRef(false)
  useEffect(() => {
    if (loading || didRefreshGit.current) return
    didRefreshGit.current = true
    fetch('/api/repo/git-status/refresh', { method: 'POST' })
      .then(r => (r.ok ? r.json() : []))
      .then((arr: GitStatus[]) => {
        if (!Array.isArray(arr)) return
        const m: Record<string, GitStatus> = {}
        for (const s of arr) if (s?.repo) m[s.repo] = s
        setGitStatus(m)
      })
      .catch(() => { /* background best-effort */ })
  }, [loading])

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

  // Known machines = union of every non-empty bound_machine seen across
  // all repos. Powers the bind-dropdown so users can reassign a repo to
  // another box without typing the hostname.
  const knownMachines = useMemo<string[]>(() => {
    const s = new Set<string>()
    if (hostname) s.add(hostname)
    for (const r of repos) {
      if (r.bound_machine) s.add(r.bound_machine)
    }
    return Array.from(s).sort()
  }, [repos, hostname])

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

  const showTabs = repos.length > 0 && platformKeys.length > 1

  const markBusy = useCallback((key: string, on: boolean) => {
    setBusy(prev => {
      const next = new Set(prev)
      if (on) next.add(key); else next.delete(key)
      return next
    })
  }, [])

  // POST /api/repo/config for a single field toggle. Optimistic UI:
  // we update local state on success only so a network hiccup doesn't
  // lie to the user.
  const toggleField = useCallback(async (name: string, field: 'enabled' | 'auto_approve' | 'auto_merge') => {
    const repo = repos.find(r => r.full_name === name)
    if (!repo) return
    const key = `${name}:${field}`
    if (busy.has(key)) return
    const nextVal = !repo[field]
    markBusy(key, true)
    try {
      const resp = await fetch('/api/repo/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repo: name, [field]: nextVal }),
      })
      if (resp.ok) {
        setRepos(prev => prev.map(r => r.full_name === name ? { ...r, [field]: nextVal } : r))
      }
    } catch { /* ignore */ }
    finally { markBusy(key, false) }
  }, [repos, busy, markBusy])

  const handleRemove = useCallback(async (name: string) => {
    const confirmed = window.confirm(
      `确认移除 ${name}？\n\n（仅从配置中移除，不会删除本地项目文件）`
    )
    if (!confirmed) return
    const key = `${name}:remove`
    markBusy(key, true)
    try {
      const resp = await fetch('/api/repo/remove', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repos: [name] }),
      })
      if (resp.ok) {
        setRepos(prev => prev.filter(r => r.full_name !== name))
      } else {
        const data = await resp.json().catch(() => ({}))
        alert(data.error || '移除失败')
      }
    } catch {
      alert('网络错误，请重试')
    } finally {
      markBusy(key, false)
    }
  }, [markBusy])

  const handleBind = useCallback(async (name: string, machine: string | null) => {
    // machine=null  → unbind
    // machine=''    → treat like null
    // machine=<hn>  → bind to that hostname
    const key = `${name}:bind`
    markBusy(key, true)
    setBindMenuFor(null)
    try {
      const resp = await fetch('/api/repo/bind', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(
          machine
            ? { repo: name, bind: true, machine }
            : { repo: name, bind: false }
        ),
      })
      if (resp.ok) {
        const data = await resp.json()
        setRepos(prev => prev.map(r =>
          r.full_name === name ? { ...r, bound_machine: data.bound_machine || undefined } : r
        ))
      }
    } catch { /* ignore */ }
    finally { markBusy(key, false) }
  }, [markBusy])

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
    <div className="max-w-6xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-5">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Monitored repos</h1>
          <p className="text-xs text-muted-foreground mt-1">
            Click toggles and machine badges to edit directly. Source of truth is{' '}
            <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">~/.clawflow/config/config.yaml</code>.
          </p>
        </div>
        <Link
          to="/repos/add"
          className="inline-flex items-center gap-2 px-4 py-2 bg-primary text-primary-foreground rounded-lg text-sm font-semibold hover:bg-primary/90 transition-colors"
        >
          <Plus className="w-4 h-4" />
          Add from VCS
        </Link>
      </div>

      {repos.length > 0 && (
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
          <span className="text-xs text-muted-foreground tabular-nums ml-auto">
            {repos.length} repo{repos.length === 1 ? '' : 's'}
          </span>
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
                <th className="text-left px-4 py-2 font-semibold">Repo</th>
                <th className="text-left px-4 py-2 font-semibold">Last activity</th>
                <th className="text-left px-4 py-2 font-semibold">Base</th>
                <th className="text-left px-4 py-2 font-semibold">Sync</th>
                <th className="text-left px-4 py-2 font-semibold">Enabled</th>
                <th className="text-left px-4 py-2 font-semibold">Auto-approve</th>
                <th className="text-left px-4 py-2 font-semibold">Auto-merge</th>
                <th className="text-left px-4 py-2 font-semibold">Bound</th>
                <th className="px-2 py-2 w-8"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {filtered.map(r => (
                <tr key={r.full_name} className="hover:bg-secondary/20 group">
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
                  <td className="px-4 py-2 text-muted-foreground font-mono text-xs">{r.base_branch}</td>
                  <td className="px-4 py-2">
                    <GitSyncCell
                      repo={r.full_name}
                      status={gitStatus[r.full_name]}
                      onUpdate={updateGitStatus}
                    />
                  </td>
                  <td className="px-4 py-2">
                    <TogglePill
                      on={r.enabled}
                      busy={busy.has(`${r.full_name}:enabled`)}
                      onLabel="enabled"
                      offLabel="disabled"
                      onClick={() => toggleField(r.full_name, 'enabled')}
                    />
                  </td>
                  <td className="px-4 py-2">
                    <TogglePill
                      on={r.auto_approve}
                      busy={busy.has(`${r.full_name}:auto_approve`)}
                      onLabel="on"
                      offLabel="off"
                      onClick={() => toggleField(r.full_name, 'auto_approve')}
                    />
                  </td>
                  <td className="px-4 py-2">
                    <TogglePill
                      on={r.auto_merge}
                      busy={busy.has(`${r.full_name}:auto_merge`)}
                      onLabel="on"
                      offLabel="off"
                      onClick={() => toggleField(r.full_name, 'auto_merge')}
                    />
                  </td>
                  <td className="px-4 py-2">
                    <BindButton
                      repo={r.full_name}
                      bound={r.bound_machine || ''}
                      hostname={hostname}
                      knownMachines={knownMachines}
                      busy={busy.has(`${r.full_name}:bind`)}
                      open={bindMenuFor === r.full_name}
                      onOpen={() => setBindMenuFor(bindMenuFor === r.full_name ? null : r.full_name)}
                      onClose={() => setBindMenuFor(null)}
                      onBind={(m) => handleBind(r.full_name, m)}
                    />
                  </td>
                  <td className="px-2 py-2">
                    <button
                      type="button"
                      onClick={() => handleRemove(r.full_name)}
                      disabled={busy.has(`${r.full_name}:remove`)}
                      title={`Remove ${r.full_name} from config`}
                      className={cn(
                        'inline-flex items-center justify-center w-7 h-7 rounded transition-all',
                        'text-muted-foreground/60 opacity-0 group-hover:opacity-100',
                        'hover:bg-destructive/10 hover:text-destructive',
                        'disabled:opacity-50 disabled:cursor-not-allowed',
                      )}
                    >
                      {busy.has(`${r.full_name}:remove`)
                        ? <Loader2 className="w-3.5 h-3.5 animate-spin" />
                        : <Trash2 className="w-3.5 h-3.5" />}
                    </button>
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

function TogglePill({
  on, busy, onLabel, offLabel, onClick,
}: {
  on: boolean
  busy: boolean
  onLabel: string
  offLabel: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      className={cn(
        'inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[11px] font-semibold border transition-colors',
        on ? 'bg-green-100 text-green-700 border-green-200 hover:bg-green-200' : 'bg-muted text-muted-foreground border-border hover:bg-secondary',
        busy && 'opacity-60 cursor-wait',
      )}
    >
      {busy && <Loader2 className="w-3 h-3 animate-spin" />}
      {on ? onLabel : offLabel}
    </button>
  )
}

function BindButton({
  repo, bound, hostname, knownMachines, busy, open, onOpen, onClose, onBind,
}: {
  repo: string
  bound: string
  hostname: string
  knownMachines: string[]
  busy: boolean
  open: boolean
  onOpen: () => void
  onClose: () => void
  onBind: (machine: string | null) => void
}) {
  const boundToMe = !!bound && bound === hostname
  const btnRef = useRef<HTMLButtonElement>(null)
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null)

  // Menu is rendered via portal with position:fixed so the parent
  // table's overflow-x-auto can't clip it onto the next row. We
  // compute the anchor rect on open and on scroll/resize; close on
  // Esc or outside click.
  useEffect(() => {
    if (!open) {
      setPos(null)
      return
    }
    function place() {
      const el = btnRef.current
      if (!el) return
      const r = el.getBoundingClientRect()
      setPos({ top: r.bottom + 4, right: window.innerWidth - r.right })
    }
    place()
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [open, onClose])

  return (
    <div className="relative inline-block">
      <button
        ref={btnRef}
        type="button"
        onClick={onOpen}
        disabled={busy}
        title={bound ? `Bound to ${bound} — click to change` : 'Unbound — click to assign a machine'}
        className={cn(
          'inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[11px] font-semibold border transition-colors max-w-[160px]',
          bound
            ? boundToMe
              ? 'bg-blue-100 text-blue-700 border-blue-200 hover:bg-blue-200'
              : 'bg-amber-50 text-amber-700 border-amber-200 hover:bg-amber-100'
            : 'border-dashed border-muted-foreground/40 text-muted-foreground hover:bg-secondary',
          busy && 'opacity-60 cursor-wait',
        )}
      >
        {busy
          ? <Loader2 className="w-3 h-3 animate-spin" />
          : bound ? <Link2 className="w-3 h-3" /> : <Link2Off className="w-3 h-3" />}
        <span className="truncate" title={bound || 'unbound'}>{bound || 'unbound'}</span>
      </button>

      {open && pos && typeof document !== 'undefined' && createPortal(
        <>
          <div className="fixed inset-0 z-40" onClick={onClose} />
          <div
            role="menu"
            aria-label={`Bind ${repo}`}
            style={{ top: pos.top, right: pos.right }}
            className="fixed z-50 min-w-[220px] bg-card border border-border rounded-lg shadow-lg py-1"
          >
            <div className="px-3 py-1 text-[10px] uppercase tracking-wide text-muted-foreground">
              Bind to machine
            </div>
            {knownMachines.length === 0 ? (
              <div className="px-3 py-2 text-xs text-muted-foreground">
                No known machines yet.
              </div>
            ) : (
              knownMachines.map(m => (
                <MenuItem
                  key={m}
                  active={bound === m}
                  onClick={() => onBind(m)}
                >
                  <Link2 className="w-3.5 h-3.5 text-muted-foreground" />
                  <span className="font-mono text-xs truncate flex-1" title={m}>{m}</span>
                  {m === hostname && (
                    <span className="text-[10px] text-blue-600 font-semibold">this</span>
                  )}
                  {bound === m && <Check className="w-3.5 h-3.5 text-blue-600" />}
                </MenuItem>
              ))
            )}
            {bound && (
              <>
                <div className="h-px bg-border my-1" />
                <MenuItem onClick={() => onBind(null)} danger>
                  <Link2Off className="w-3.5 h-3.5" />
                  <span className="text-xs">Unbind</span>
                </MenuItem>
              </>
            )}
          </div>
        </>,
        document.body,
      )}
    </div>
  )
}

function MenuItem({
  active, danger, onClick, children,
}: {
  active?: boolean
  danger?: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className={cn(
        'w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-secondary/60 transition-colors',
        active && 'bg-secondary/40',
        danger ? 'text-destructive hover:bg-destructive/10' : 'text-foreground',
      )}
    >
      {children}
    </button>
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
