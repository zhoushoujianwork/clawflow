import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { ChevronLeft, ChevronDown, ChevronRight, FolderKanban, FolderOpen, MessageSquare, Sparkles, X, Trash2, Plus, Loader2 } from 'lucide-react'
import { cn } from '../lib/utils'
import { useChatDrawer } from '../lib/chatContext'
import { Markdown } from '../components/Markdown'
import { useDocUpdater } from '../components/DocUpdater'
import { ContextWorkLinks } from '../components/ContextWorkLinks'
import { repoUrl, useRepoInfoMap } from '../lib/vcsUrls'
import { VcsIcon } from '../components/VcsIcon'
import { useConfigChanged } from '../lib/configEvents'
import { GitSyncCell, type GitStatus } from '../components/GitSyncCell'
// Long claude -p run (typically 30s–2min). The job runs server-side
// so the user is free to navigate away — re-opening the page resumes
// the spinner via /api/project/generate-context/status polling.
const GENERATE_HINT = 'Calling Offload — usually 30s to 2min. Runs in the background, you can close this tab.'

interface Project {
  name: string
  repos: string[]
  created_at?: string
  context_md?: string
  testing_md?: string
  deployment_md?: string
  automation?: ProjectAutomation
}
interface ProjectAutomation {
  enabled: boolean
  cooldown_minutes: number
  last_woken_at?: string
  // bound_machine pins the Pilot wake to a specific hostname. Empty =
  // any machine. Independent from repo.bound_machine (which gates the
  // operator/run path) — a project's Pilot may legitimately run on a
  // machine where none of its repos are cloned, since Pilot only does
  // API-layer triage.
  bound_machine?: string
  // last_skip_reason explains why the most recent Schedule pass declined
  // to wake the Pilot. Surfaces the "Pilot on but no recent activity"
  // failure mode (most often: bound_machine mismatch, or
  // require_binding=on with no bound_machine).
  last_skip_reason?: string
  last_skip_at?: string
}

interface RepoEntry {
  full_name: string
  auto_merge?: boolean
  enabled?: boolean
  bound_machine?: string
  // Absolute path to the local clone, when one is configured. Drives the
  // "open in IDE" shortcut; absent for repos with no local checkout.
  local_path?: string
}

// previewMD returns a one-line preview suitable for a collapsed-card
// header — first non-empty heading if there is one, else the first
// non-empty line truncated. Used by the context.md / testing.md
// section headers when collapsed so the user sees what's inside
// without expanding.
function previewMD(md: string): string {
  const lines = md.split('\n').map(l => l.trim()).filter(Boolean)
  for (const l of lines) {
    if (l.startsWith('#')) return l.replace(/^#+\s*/, '').slice(0, 80)
  }
  return (lines[0] ?? '').slice(0, 80)
}

export const Route = createFileRoute('/_app/projects/$name')({
  component: ProjectDetail,
})

function ProjectDetail() {
  const { name } = Route.useParams()
  useDocumentTitle(name)
  const navigate = useNavigate()
  const chatDrawer = useChatDrawer()

  const [project, setProject] = useState<Project | null>(null)
  const [loading, setLoading] = useState(true)

  // Delete state
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)
  const [deleting, setDeleting] = useState(false)

  // Add repo state
  const [showAddRepo, setShowAddRepo] = useState(false)
  const [availableRepos, setAvailableRepos] = useState<string[]>([])
  const [repoSearch, setRepoSearch] = useState('')
  const [addingRepo, setAddingRepo] = useState<string | null>(null)
  const [repoError, setRepoError] = useState<string | null>(null)

  // Per-repo auto_merge state, keyed by full_name. Populated
  // from /data/repos.json on mount so the member-repo list can render the
  // automation rollup badges without waiting for the add-repo dropdown to
  // open. Empty until the fetch resolves.
  const [repoMeta, setRepoMeta] = useState<Record<string, RepoEntry>>({})

  // Git sync status keyed by repo full_name, seeded from the cached
  // /api/repo/git-status so the member-repo list shows the same main-branch
  // ahead/behind/dirty state as the Repos page (issue #243 follow-up).
  const [gitStatus, setGitStatus] = useState<Record<string, GitStatus>>({})

  // Remove repo state
  const [removingRepo, setRemovingRepo] = useState<string | null>(null)

  // IDE deep-link scheme for the per-repo "open in IDE" shortcut. Resolved
  // from /api/settings.global.default_ide — same logic as the repo list page
  // (_app.repos.index.tsx) so both surfaces honour the user's IDE choice.
  const [ideScheme, setIdeScheme] = useState('vscode://file/')

  // Generate context state. Used only by the empty-state Initialize
  // button — once context.md exists, all further edits go through
  // `clawflow project chat` (the chat link in the header), which
  // handles regeneration via dialogue.
  const [generating, setGenerating] = useState(false)
  const [generateError, setGenerateError] = useState<string | null>(null)
  const pollTimerRef = useRef<number | null>(null)

  // Collapsed-by-default state for the two docs. Both can be long
  // (especially context.md), so the page would otherwise scroll past
  // the member-repo list for non-trivial projects. Empty docs render
  // their own "no content yet" panel and ignore this state.
  const [contextOpen, setContextOpen] = useState(false)
  const [testingOpen, setTestingOpen] = useState(false)
  const [deploymentOpen, setDeploymentOpen] = useState(false)

  // "Update with AI" affordances for the three project-level docs.
  // The hook returns { trigger, form } per doc — trigger goes inline in
  // the card header row, form renders below when open.
  const contextUpdater = useDocUpdater({ project: name, file: 'context.md', onUpdated: fetchProject })
  const testingUpdater = useDocUpdater({ project: name, file: 'testing.md', onUpdated: fetchProject })
  const deploymentUpdater = useDocUpdater({ project: name, file: 'deployment.md', onUpdated: fetchProject })

  // Generate deployment state — mirrors the context generation pattern.
  const [generatingDeployment, setGeneratingDeployment] = useState(false)
  const [generateDeploymentError, setGenerateDeploymentError] = useState<string | null>(null)
  const deploymentPollTimerRef = useRef<number | null>(null)

  const repoInfoMap = useRepoInfoMap()

  function fetchProject() {
    // Live read from /api/project/get — bypasses the
    // /data/projects.json snapshot so out-of-band file edits
    // (most common path: `clawflow project chat` writing back an
    // updated context.md or testing.md) show up on the next page
    // refresh without waiting for a fresh `clawflow run` snapshot.
    fetch(`/api/project/get?name=${encodeURIComponent(name)}`, { cache: 'no-store' })
      .then(async r => {
        if (!r.ok) return null
        return r.json().catch(() => null) as Promise<Project | null>
      })
      .catch(() => null)
      .then(data => {
        setProject(data)
        setLoading(false)
      })
  }

  useEffect(() => {
    fetchProject()
    // Always fetch repo metadata on mount so the member-repo list can
    // render auto_merge badges immediately. The add-repo
    // dropdown re-fetches on open to catch any repos added since.
    fetchAvailableRepos()
    fetchGitStatus()
    // Pull the current machine's hostname once so the bind button can
    // render the actual name. Best-effort: failing leaves the label as
    // "this machine" — the API will still resolve correctly on submit.
    fetch('/api/settings', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : null))
      .catch(() => null)
      .then(settings => {
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
      })
    // Check if a generate job is already running for this project so
    // navigating away mid-run and coming back resumes the spinner.
    fetch(`/api/project/generate-context/status?project=${encodeURIComponent(name)}`, { cache: 'no-store' })
      .then(r => r.json().catch(() => ({})).then(d => ({ status: r.status, body: d })))
      .then(({ status, body }) => {
        if (status === 200 && body.status === 'running') {
          setGenerating(true)
          startPolling()
        }
      })
      .catch(() => { /* idle */ })
    // Same resume-on-navigate check for deployment generation.
    fetch(`/api/project/generate-deployment/status?project=${encodeURIComponent(name)}`, { cache: 'no-store' })
      .then(r => r.json().catch(() => ({})).then(d => ({ status: r.status, body: d })))
      .then(({ status, body }) => {
        if (status === 200 && body.status === 'running') {
          setGeneratingDeployment(true)
          startDeploymentPolling()
        }
      })
      .catch(() => { /* idle */ })
    return () => { stopPolling(); stopDeploymentPolling() }
  }, [name])

  useConfigChanged(() => {
    fetchProject()
    fetchAvailableRepos()
  })

  // Seed git sync status from the cache (instant). The Repos page owns the
  // background fetch+recompute hook; here we just read the shared cache so the
  // member list reflects the latest known ahead/behind without a live fetch.
  function fetchGitStatus() {
    fetch('/api/repo/git-status', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then((gs: GitStatus[]) => {
        if (!Array.isArray(gs)) return
        const m: Record<string, GitStatus> = {}
        for (const s of gs) if (s?.repo) m[s.repo] = s
        setGitStatus(m)
      })
  }

  // Apply a single repo's refreshed status (after pull/push/refresh) into the map.
  function updateGitStatus(s: GitStatus) {
    if (!s?.repo) return
    setGitStatus(prev => ({ ...prev, [s.repo]: s }))
  }

  function fetchAvailableRepos() {
    fetch('/data/repos.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then(data => {
        const repos: RepoEntry[] = Array.isArray(data) ? data : []
        setAvailableRepos(repos.map(r => r.full_name).filter(Boolean))
        const byName: Record<string, RepoEntry> = {}
        for (const r of repos) {
          if (r.full_name) byName[r.full_name] = r
        }
        setRepoMeta(byName)
      })
  }

  async function handleDelete() {
    setDeleting(true)
    try {
      const r = await fetch('/api/project/delete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      })
      const d = await r.json()
      if (!r.ok || d.error) throw new Error(d.error || `HTTP ${r.status}`)
      navigate({ to: '/projects' })
    } catch {
      setDeleting(false)
      setShowDeleteConfirm(false)
    }
  }

  async function handleAddRepo(repo: string) {
    setAddingRepo(repo)
    setRepoError(null)
    try {
      const r = await fetch('/api/project/add-repo', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: name, repo }),
      })
      const d = await r.json()
      if (!r.ok || d.error) throw new Error(d.error || `HTTP ${r.status}`)
      setShowAddRepo(false)
      setRepoSearch('')
      fetchProject()
    } catch (e) {
      setRepoError(e instanceof Error ? e.message : 'Unknown error')
    } finally {
      setAddingRepo(null)
    }
  }

  function startPolling() {
    if (pollTimerRef.current !== null) return
    const tick = async () => {
      try {
        const r = await fetch(`/api/project/generate-context/status?project=${encodeURIComponent(name)}`, { cache: 'no-store' })
        const d = await r.json().catch(() => ({}))
        if (r.status === 404 || d.status === 'idle') {
          stopPolling()
          setGenerating(false)
          return
        }
        if (d.status === 'running') return
        if (d.status === 'done') {
          stopPolling()
          setGenerating(false)
          fetchProject()
          return
        }
        if (d.status === 'error') {
          stopPolling()
          setGenerating(false)
          setGenerateError(d.error || 'generation failed')
          return
        }
      } catch {
        // network blip — just keep polling
      }
    }
    // Poll right away so the user sees state quickly, then every 2s.
    void tick()
    pollTimerRef.current = window.setInterval(tick, 2000)
  }

  function stopPolling() {
    if (pollTimerRef.current !== null) {
      window.clearInterval(pollTimerRef.current)
      pollTimerRef.current = null
    }
  }

  function startDeploymentPolling() {
    if (deploymentPollTimerRef.current !== null) return
    const tick = async () => {
      try {
        const r = await fetch(`/api/project/generate-deployment/status?project=${encodeURIComponent(name)}`, { cache: 'no-store' })
        const d = await r.json().catch(() => ({}))
        if (r.status === 404 || d.status === 'idle') {
          stopDeploymentPolling()
          setGeneratingDeployment(false)
          return
        }
        if (d.status === 'running') return
        if (d.status === 'done') {
          stopDeploymentPolling()
          setGeneratingDeployment(false)
          fetchProject()
          return
        }
        if (d.status === 'error') {
          stopDeploymentPolling()
          setGeneratingDeployment(false)
          setGenerateDeploymentError(d.error || 'generation failed')
          return
        }
      } catch {
        // network blip — keep polling
      }
    }
    void tick()
    deploymentPollTimerRef.current = window.setInterval(tick, 2000)
  }

  function stopDeploymentPolling() {
    if (deploymentPollTimerRef.current !== null) {
      window.clearInterval(deploymentPollTimerRef.current)
      deploymentPollTimerRef.current = null
    }
  }

  async function handleGenerateDeployment() {
    setGenerateDeploymentError(null)
    setGeneratingDeployment(true)
    try {
      const r = await fetch('/api/project/generate-deployment', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: name }),
      })
      const d = await r.json().catch(() => ({}))
      if (r.status === 202 || r.status === 409) {
        startDeploymentPolling()
        return
      }
      throw new Error(d.error || `HTTP ${r.status}`)
    } catch (e) {
      setGeneratingDeployment(false)
      setGenerateDeploymentError(e instanceof Error ? e.message : 'Unknown error')
    }
  }

  async function handleInitialize() {
    setGenerateError(null)
    setGenerating(true)
    try {
      const r = await fetch('/api/project/generate-context', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: name }),
      })
      const d = await r.json().catch(() => ({}))
      // 202 = accepted, 409 = already running — both mean a job exists,
      // so start polling. Anything else is a real error.
      if (r.status === 202 || r.status === 409) {
        startPolling()
        return
      }
      throw new Error(d.error || `HTTP ${r.status}`)
    } catch (e) {
      setGenerating(false)
      setGenerateError(e instanceof Error ? e.message : 'Unknown error')
    }
  }

  async function handleRemoveRepo(repo: string) {
    setRemovingRepo(repo)
    try {
      const r = await fetch('/api/project/remove-repo', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: name, repo }),
      })
      const d = await r.json()
      if (!r.ok || d.error) throw new Error(d.error || `HTTP ${r.status}`)
      fetchProject()
    } catch {
      // silent — the UI will just not update
    } finally {
      setRemovingRepo(null)
    }
  }

  const filteredAvailableRepos = availableRepos
    .filter(r => !project?.repos?.includes(r))
    .filter(r => !repoSearch || r.toLowerCase().includes(repoSearch.toLowerCase()))

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <Link
        to="/projects"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground mb-4"
      >
        <ChevronLeft className="w-3.5 h-3.5" /> Projects
      </Link>

      {loading ? (
        <p className="text-sm text-muted-foreground py-8">Loading…</p>
      ) : !project ? (
        <div className="bg-card border border-border rounded-xl p-6 text-sm text-muted-foreground">
          Project <code className="font-mono text-foreground">{name}</code> not found.
        </div>
      ) : (
        <>
          {/* Header */}
          <div className="flex items-start justify-between mb-6">
            <div>
              <h1 className="text-2xl font-bold text-foreground font-mono flex items-center gap-2">
                <FolderKanban className="w-5 h-5 text-muted-foreground" />
                {project.name}
              </h1>
              <div className="flex items-center gap-2 mt-1 text-xs text-muted-foreground flex-wrap">
                <span className="tabular-nums">
                  {project.repos?.length ?? 0} {(project.repos?.length ?? 0) === 1 ? 'repo' : 'repos'}
                </span>
                {project.created_at && (
                  <>
                    <span>·</span>
                    <span>created {new Date(project.created_at).toLocaleDateString()}</span>
                  </>
                )}
                <span>·</span>
                <button
                  onClick={() => chatDrawer.open({ project: project.name, action: 'chat' })}
                  className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline"
                  title="Open chat — spawns a terminal running clawflow project chat"
                >
                  <MessageSquare className="w-3 h-3" /> chat
                </button>
                <span>·</span>
                <button onClick={() => chatDrawer.open({ pilot: true })}
                  className="hover:text-foreground hover:underline">平台 steward</button>
              </div>
            </div>
            {/* Delete button */}
            {!showDeleteConfirm ? (
              <button
                type="button"
                onClick={() => setShowDeleteConfirm(true)}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border border-border text-muted-foreground hover:text-red-600 hover:border-red-300 transition-colors"
              >
                <Trash2 className="w-3.5 h-3.5" />
                Delete
              </button>
            ) : (
              <div className="flex items-center gap-2">
                <span className="text-xs text-red-600">Delete this project?</span>
                <button
                  type="button"
                  onClick={handleDelete}
                  disabled={deleting}
                  className="px-3 py-1.5 rounded-lg text-sm font-medium bg-red-600 text-white hover:bg-red-700 transition-colors disabled:opacity-50"
                >
                  {deleting ? <Loader2 className="w-4 h-4 animate-spin" /> : 'Confirm'}
                </button>
                <button
                  type="button"
                  onClick={() => setShowDeleteConfirm(false)}
                  className="px-3 py-1.5 rounded-lg text-sm font-medium border border-border text-muted-foreground hover:text-foreground transition-colors"
                >
                  Cancel
                </button>
              </div>
            )}
          </div>

          <ContextWorkLinks project={name} />
          <Link to="/projects/$name/pilot-runs" params={{ name }} className="inline-block text-sm underline mb-6">Pilot 历史记录</Link>

          {/* Member repos — moved above the doc sections so the
              user sees the repo list before the config controls. */}
          <section className="mb-6">
            <div className="flex items-center justify-between mb-2">
              <h2 className="text-sm font-semibold text-foreground">
                Member repos{' '}
                <span className="font-normal text-muted-foreground">
                  ({project.repos?.length ?? 0})
                </span>
              </h2>
              <button
                type="button"
                onClick={() => {
                  setShowAddRepo(true)
                  setRepoError(null)
                  fetchAvailableRepos()
                }}
                className="inline-flex items-center gap-1 px-2 py-1 rounded-md text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors"
              >
                <Plus className="w-3 h-3" />
                Add repo
              </button>
            </div>

            {/* Add repo panel */}
            {showAddRepo && (
              <div className="bg-card border border-border rounded-xl p-4 mb-3">
                <div className="flex items-center justify-between mb-3">
                  <h3 className="text-xs font-semibold text-foreground">Add a configured repo</h3>
                  <button
                    type="button"
                    onClick={() => { setShowAddRepo(false); setRepoSearch(''); setRepoError(null) }}
                    className="text-muted-foreground hover:text-foreground"
                  >
                    <X className="w-3.5 h-3.5" />
                  </button>
                </div>
                <input
                  type="text"
                  placeholder="Search repos..."
                  value={repoSearch}
                  onChange={e => setRepoSearch(e.target.value)}
                  className="w-full px-3 py-1.5 bg-background border border-border rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-primary font-mono mb-2"
                  autoFocus
                />
                {repoError && (
                  <p className="text-xs text-red-600 mb-2">{repoError}</p>
                )}
                {filteredAvailableRepos.length === 0 ? (
                  <p className="text-xs text-muted-foreground py-2">
                    {availableRepos.length === 0
                      ? 'No configured repos found. Add repos in the Repos page first.'
                      : 'No matching repos available to add.'}
                  </p>
                ) : (
                  <div className="max-h-48 overflow-y-auto divide-y divide-border rounded-lg border border-border">
                    {filteredAvailableRepos.map(repo => (
                      <div
                        key={repo}
                        className="flex items-center justify-between px-3 py-2 hover:bg-secondary/20"
                      >
                        <span className="font-mono text-sm text-foreground">{repo}</span>
                        <button
                          type="button"
                          onClick={() => handleAddRepo(repo)}
                          disabled={addingRepo === repo}
                          className={cn(
                            'px-2 py-0.5 rounded text-xs font-medium transition-colors',
                            addingRepo === repo
                              ? 'text-muted-foreground cursor-not-allowed'
                              : 'text-primary hover:bg-primary/10',
                          )}
                        >
                          {addingRepo === repo ? <Loader2 className="w-3 h-3 animate-spin" /> : 'Add'}
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {!project.repos || project.repos.length === 0 ? (
              <div className="bg-card border border-border rounded-xl p-6 text-center">
                <p className="text-sm text-muted-foreground">
                  No repos in this project. Click <strong>Add repo</strong> above to add one.
                </p>
              </div>
            ) : (
              <>
                {(() => {
                  const meta = project.repos.map(r => repoMeta[r] ?? null)
                  const known = meta.filter(m => m !== null) as RepoEntry[]
                  const mergeOn = known.filter(m => m.auto_merge).length
                  const pmOn = project.automation?.enabled
                  return (
                    <div className="text-xs text-muted-foreground mb-2 flex items-center gap-3 flex-wrap">
                      <span className="tabular-nums">
                        auto-merge: <span className="font-medium text-foreground">{mergeOn}/{project.repos.length}</span>
                      </span>
                      <span>Pilot scheduling {pmOn ? 'on' : 'off'} · independent of issue execution</span>
                    </div>
                  )
                })()}
                <div className="bg-card border border-border rounded-xl overflow-hidden divide-y divide-border">
                  {project.repos.map(repo => {
                    const m = repoMeta[repo]
                    return (
                      <div
                        key={repo}
                        className="flex items-center justify-between px-4 py-2 hover:bg-secondary/20 group gap-3"
                      >
                        <Link
                          to="/repos/$"
                          params={{ _splat: repo }}
                          className="font-mono text-sm text-foreground hover:underline truncate"
                          title={repo}
                        >
                          {repo}
                        </Link>
                        <div className="flex items-center gap-1.5 shrink-0">
                          <GitSyncCell
                            repo={repo}
                            status={gitStatus[repo]}
                            onUpdate={updateGitStatus}
                          />
                          <span
                            className={cn(
                              'px-1.5 py-0.5 rounded text-[10px] font-medium tabular-nums',
                              m?.auto_merge
                                ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400'
                                : 'bg-secondary text-muted-foreground',
                            )}
                            title={m?.auto_merge ? 'auto_merge enabled' : 'auto_merge off'}
                          >
                            merge {m?.auto_merge ? 'on' : 'off'}
                          </span>
                          <a
                            href={repoUrl(repo, repoInfoMap)}
                            target="_blank"
                            rel="noopener noreferrer"
                            title="Open in VCS"
                            className="inline-flex items-center text-muted-foreground hover:text-foreground shrink-0 ml-1"
                          >
                            <VcsIcon repo={repo} map={repoInfoMap} className="w-3.5 h-3.5" />
                          </a>
                          {m?.local_path && (
                            <a
                              href={`${ideScheme}${m.local_path}?windowId=_blank`}
                              title={`Open in IDE: ${m.local_path}`}
                              className="inline-flex items-center text-muted-foreground hover:text-foreground shrink-0"
                            >
                              <FolderOpen className="w-3.5 h-3.5" />
                            </a>
                          )}
                          <button
                            type="button"
                            onClick={() => handleRemoveRepo(repo)}
                            disabled={removingRepo === repo}
                            className="text-muted-foreground hover:text-red-600 transition-colors disabled:opacity-50 ml-1"
                            title="Remove from project"
                          >
                            {removingRepo === repo ? (
                              <Loader2 className="w-3.5 h-3.5 animate-spin" />
                            ) : (
                              <X className="w-3.5 h-3.5" />
                            )}
                          </button>
                        </div>
                      </div>
                    )
                  })}
                </div>
              </>
            )}
          </section>

          {/* Context.md — collapsible. Default collapsed because the
              doc is often long enough to push the rest of the page out
              of view. Header bar carries enough preview info (heading
              or size) so the user knows whether it's worth opening.
              Stays full-width (not paired with testing.md in a 2-col
              grid) so the collapsed/expanded card never visually
              merges with the status row above. */}
          <section className="mb-3">
            {generateError && (
              <div className="mb-2 text-xs text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 dark:bg-red-950/30 dark:border-red-900">
                {generateError}
              </div>
            )}
            {project.context_md ? (
              <div className="bg-card border border-border rounded-xl overflow-hidden">
                <button
                  type="button"
                  onClick={() => setContextOpen(o => !o)}
                  className="w-full flex items-center gap-2 px-4 py-3 hover:bg-secondary/30 transition-colors text-left"
                  aria-expanded={contextOpen}
                >
                  {contextOpen ? <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" /> : <ChevronRight className="w-4 h-4 text-muted-foreground shrink-0" />}
                  <span className="text-sm font-semibold text-foreground font-mono">context.md</span>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {(project.context_md.length / 1024).toFixed(1)}kb
                  </span>
                  {!contextOpen && (
                    <span className="text-xs text-muted-foreground truncate">
                      · {previewMD(project.context_md)}
                    </span>
                  )}
                  {contextUpdater.trigger}
                </button>
                {contextOpen && (
                  <div className="px-4 pb-4 pt-0 border-t border-border">
                    <Markdown>{project.context_md}</Markdown>
                  </div>
                )}
                {contextUpdater.form}
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between mb-2">
                  <h2 className="text-sm font-semibold text-foreground font-mono">context.md</h2>
                </div>
                <div className="bg-card border border-border rounded-xl p-6 text-center space-y-3">
                  <p className="text-sm text-muted-foreground">
                    No <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">context.md</code> yet.
                  </p>
                  <button
                    type="button"
                    onClick={handleInitialize}
                    disabled={generating || !project.repos?.length}
                    className={cn(
                      'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors',
                      'bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed',
                    )}
                  >
                    {generating ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Sparkles className="w-3.5 h-3.5" />}
                    {generating ? 'Generating…' : 'Initialize context.md'}
                  </button>
                  {!project.repos?.length && (
                    <p className="text-xs text-muted-foreground">Add at least one repo first.</p>
                  )}
                  {generating && (
                    <p className="text-xs text-muted-foreground">{GENERATE_HINT}</p>
                  )}
                </div>
              </>
            )}
          </section>

          {/* Testing.md — collapsible. Same pattern as Context above.
              No AI auto-generation path; authored interactively via
              project chat (the SOP relies on details only the user
              knows: which serial port, which board, startup order). */}
          <section className="mb-3">
            {project.testing_md ? (
              <div className="bg-card border border-border rounded-xl overflow-hidden">
                <button
                  type="button"
                  onClick={() => setTestingOpen(o => !o)}
                  className="w-full flex items-center gap-2 px-4 py-3 hover:bg-secondary/30 transition-colors text-left"
                  aria-expanded={testingOpen}
                >
                  {testingOpen ? <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" /> : <ChevronRight className="w-4 h-4 text-muted-foreground shrink-0" />}
                  <span className="text-sm font-semibold text-foreground font-mono">testing.md</span>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {(project.testing_md.length / 1024).toFixed(1)}kb
                  </span>
                  {!testingOpen && (
                    <span className="text-xs text-muted-foreground truncate">
                      · {previewMD(project.testing_md)}
                    </span>
                  )}
                  {testingUpdater.trigger}
                </button>
                {testingOpen && (
                  <div className="px-4 pb-4 pt-0 border-t border-border">
                    <Markdown>{project.testing_md}</Markdown>
                  </div>
                )}
                {testingUpdater.form}
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between mb-2">
                  <h2 className="text-sm font-semibold text-foreground font-mono">testing.md</h2>
                  <span className="text-xs text-muted-foreground">
                    How to bring up the local runtime — startup order, services, hardware
                  </span>
                </div>
                <div className="bg-card border border-border rounded-xl p-6 text-center space-y-3">
                  <p className="text-sm text-muted-foreground">
                    No <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">testing.md</code> yet.
                    Describe how to start your local env (frontend + backend + hardware/serial)
                    so <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">implement</code> can verify changes locally.
                  </p>
                  <button
                    type="button"
                    onClick={() => chatDrawer.open({ project: project.name, action: 'chat' })}
                    className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors bg-primary text-primary-foreground hover:bg-primary/90"
                  >
                    <MessageSquare className="w-3.5 h-3.5" />
                    Draft via chat
                  </button>
                  <p className="text-xs text-muted-foreground">
                    This is a runbook (startup steps), not a list of test cases.
                  </p>
                </div>
              </>
            )}
          </section>

          {/* Deployment.md — collapsible. Same pattern as Context and
              Testing above. Has an AI Generate button in the empty state
              (mirrors context.md's Initialize button) because deployment
              config can be inferred from CI workflows and context.md. */}
          <section className="mb-3">
            {generateDeploymentError && (
              <div className="mb-2 text-xs text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 dark:bg-red-950/30 dark:border-red-900">
                {generateDeploymentError}
              </div>
            )}
            {project.deployment_md ? (
              <div className="bg-card border border-border rounded-xl overflow-hidden">
                <button
                  type="button"
                  onClick={() => setDeploymentOpen(o => !o)}
                  className="w-full flex items-center gap-2 px-4 py-3 hover:bg-secondary/30 transition-colors text-left"
                  aria-expanded={deploymentOpen}
                >
                  {deploymentOpen ? <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" /> : <ChevronRight className="w-4 h-4 text-muted-foreground shrink-0" />}
                  <span className="text-sm font-semibold text-foreground font-mono">deployment.md</span>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {(project.deployment_md.length / 1024).toFixed(1)}kb
                  </span>
                  {!deploymentOpen && (
                    <span className="text-xs text-muted-foreground truncate">
                      · {previewMD(project.deployment_md)}
                    </span>
                  )}
                  {deploymentUpdater.trigger}
                </button>
                {deploymentOpen && (
                  <div className="px-4 pb-4 pt-0 border-t border-border">
                    <Markdown>{project.deployment_md}</Markdown>
                  </div>
                )}
                {deploymentUpdater.form}
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between mb-2">
                  <h2 className="text-sm font-semibold text-foreground font-mono">deployment.md</h2>
                  <span className="text-xs text-muted-foreground">
                    Runtime environment, log retrieval, health indicators
                  </span>
                </div>
                <div className="bg-card border border-border rounded-xl p-6 text-center space-y-3">
                  <p className="text-sm text-muted-foreground">
                    No <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">deployment.md</code> yet.
                    Describe how to inspect the runtime (logs, metrics, SSH/kubectl) so the Pilot can assess live health.
                  </p>
                  <div className="flex items-center justify-center gap-2 flex-wrap">
                    <button
                      type="button"
                      onClick={handleGenerateDeployment}
                      disabled={generatingDeployment || !project.repos?.length}
                      className={cn(
                        'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors',
                        'bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed',
                      )}
                    >
                      {generatingDeployment ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Sparkles className="w-3.5 h-3.5" />}
                      {generatingDeployment ? 'Generating…' : 'Generate deployment.md'}
                    </button>
                    <button
                      type="button"
                      onClick={() => chatDrawer.open({ project: project.name, action: 'chat' })}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors border border-border text-muted-foreground hover:text-foreground hover:bg-secondary/50"
                    >
                      <MessageSquare className="w-3.5 h-3.5" />
                      Draft via chat
                    </button>
                  </div>
                  {!project.repos?.length && (
                    <p className="text-xs text-muted-foreground">Add at least one repo first.</p>
                  )}
                  {generatingDeployment && (
                    <p className="text-xs text-muted-foreground">{GENERATE_HINT}</p>
                  )}
                </div>
              </>
            )}
          </section>


        </>
      )}
    </div>
  )
}
