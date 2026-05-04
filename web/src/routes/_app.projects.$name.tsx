import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { ChevronLeft, FolderKanban, MessageSquare, Sparkles, X, Trash2, Plus, Loader2 } from 'lucide-react'
import { cn } from '../lib/utils'
import { useChatDrawer } from '../lib/chatContext'
import { Markdown } from '../components/Markdown'

// Long claude -p run (typically 30s–2min). The job runs server-side
// so the user is free to navigate away — re-opening the page resumes
// the spinner via /api/project/generate-context/status polling.
const GENERATE_HINT = 'Calling claude -p — usually 30s to 2min. Runs in the background, you can close this tab.'

interface Project {
  name: string
  repos: string[]
  created_at?: string
  context_md?: string
  automation?: ProjectAutomation
}

interface ProjectAutomation {
  enabled: boolean
  cooldown_minutes: number
  last_woken_at?: string
}

interface RepoEntry {
  full_name: string
}

export const Route = createFileRoute('/_app/projects/$name')({
  component: ProjectDetail,
})

function ProjectDetail() {
  const { name } = Route.useParams()
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

  // Remove repo state
  const [removingRepo, setRemovingRepo] = useState<string | null>(null)

  // Generate context state. Used only by the empty-state Initialize
  // button — once context.md exists, all further edits go through
  // `clawflow project chat` (the chat link in the header), which
  // handles regeneration via dialogue.
  const [generating, setGenerating] = useState(false)
  const [generateError, setGenerateError] = useState<string | null>(null)
  const pollTimerRef = useRef<number | null>(null)

  // Automation state. The cooldown input is held locally so the user
  // can edit it freely without each keystroke firing a save — the
  // server only learns the new value on toggle change or explicit Save.
  // Defaults to 30 min to match the CLI's `--cooldown` default.
  const [cooldownDraft, setCooldownDraft] = useState<string>('30')
  const [automationSaving, setAutomationSaving] = useState(false)
  const [automationError, setAutomationError] = useState<string | null>(null)

  function fetchProject() {
    fetch('/data/projects.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then(data => {
        const list: Project[] = Array.isArray(data) ? data : []
        const match = list.find(p => p.name === name) || null
        setProject(match)
        // Seed the cooldown input from the persisted value. Only do this
        // on initial load (or when the field comes back empty) so a user
        // mid-edit doesn't have their typing wiped by a refetch.
        if (match?.automation && (cooldownDraft === '30' || !cooldownDraft)) {
          setCooldownDraft(String(match.automation.cooldown_minutes ?? 30))
        }
        setLoading(false)
      })
  }

  async function saveAutomation(enabled: boolean, cooldownMinutes: number) {
    setAutomationSaving(true)
    setAutomationError(null)
    try {
      const r = await fetch('/api/project/automation', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: name, enabled, cooldown_minutes: cooldownMinutes }),
      })
      const d = await r.json().catch(() => ({}))
      if (!r.ok || d.error) throw new Error(d.error || `HTTP ${r.status}`)
      fetchProject()
    } catch (e) {
      setAutomationError(e instanceof Error ? e.message : 'Unknown error')
    } finally {
      setAutomationSaving(false)
    }
  }

  useEffect(() => {
    fetchProject()
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
    return () => { stopPolling() }
  }, [name])

  function fetchAvailableRepos() {
    fetch('/data/repos.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then(data => {
        const repos: RepoEntry[] = Array.isArray(data) ? data : []
        setAvailableRepos(repos.map(r => r.full_name).filter(Boolean))
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
              <div className="flex items-center gap-2 mt-1 text-xs text-muted-foreground">
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

          {/* Automation toggle — wakes the project-manager agent
              after each `clawflow run` pass when enabled. PM can only
              file new issues; existing issue state stays under
              operator control. */}
          <section className="mb-6">
            <div className="flex items-center justify-between mb-2">
              <h2 className="text-sm font-semibold text-foreground">Automation</h2>
              {project.automation?.last_woken_at && (
                <span className="text-xs text-muted-foreground tabular-nums">
                  last woken {new Date(project.automation.last_woken_at).toLocaleString()}
                </span>
              )}
            </div>
            <div className="bg-card border border-border rounded-xl p-4">
              <div className="flex items-start justify-between gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-3">
                    <button
                      type="button"
                      onClick={() => {
                        const cd = parseInt(cooldownDraft, 10)
                        saveAutomation(!project.automation?.enabled, isNaN(cd) ? 30 : cd)
                      }}
                      disabled={automationSaving}
                      role="switch"
                      aria-checked={project.automation?.enabled ?? false}
                      className={cn(
                        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors',
                        project.automation?.enabled ? 'bg-primary' : 'bg-secondary',
                        automationSaving && 'opacity-50 cursor-not-allowed',
                      )}
                    >
                      <span
                        className={cn(
                          'inline-block h-4 w-4 transform rounded-full bg-white transition-transform',
                          project.automation?.enabled ? 'translate-x-4' : 'translate-x-0.5',
                        )}
                      />
                    </button>
                    <span className="text-sm font-medium text-foreground">
                      {project.automation?.enabled ? 'Enabled' : 'Disabled'}
                    </span>
                  </div>
                  <p className="text-xs text-muted-foreground mt-2">
                    When on, every <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">clawflow run</code> pass wakes the project manager. The PM can file new issues to schedule work — it never touches existing issue state.
                  </p>
                </div>
                <div className="flex flex-col gap-1">
                  <label className="text-xs text-muted-foreground">Cooldown (min)</label>
                  <div className="flex items-center gap-2">
                    <input
                      type="number"
                      min={0}
                      step={1}
                      value={cooldownDraft}
                      onChange={e => setCooldownDraft(e.target.value)}
                      className="w-20 px-2 py-1 bg-background border border-border rounded-lg text-sm text-right tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
                    />
                    <button
                      type="button"
                      onClick={() => {
                        const cd = parseInt(cooldownDraft, 10)
                        saveAutomation(project.automation?.enabled ?? false, isNaN(cd) ? 30 : cd)
                      }}
                      disabled={
                        automationSaving ||
                        parseInt(cooldownDraft, 10) === (project.automation?.cooldown_minutes ?? 30)
                      }
                      className="px-2 py-1 rounded-md text-xs font-medium border border-border text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      Save
                    </button>
                  </div>
                </div>
              </div>
              {automationError && (
                <p className="mt-3 text-xs text-red-600">{automationError}</p>
              )}
            </div>
          </section>

          {/* Context.md */}
          <section className="mb-6">
            <div className="flex items-center justify-between mb-2">
              <h2 className="text-sm font-semibold text-foreground">Context</h2>
              {project.context_md && (
                <span className="text-xs text-muted-foreground inline-flex items-center gap-1">
                  <MessageSquare className="w-3 h-3" />
                  Edit via the <strong className="font-semibold text-foreground">chat</strong> link above
                </span>
              )}
            </div>
            {generateError && (
              <div className="mb-2 text-xs text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 dark:bg-red-950/30 dark:border-red-900">
                {generateError}
              </div>
            )}
            {project.context_md ? (
              <div className="bg-card border border-border rounded-xl p-4">
                <Markdown>{project.context_md}</Markdown>
              </div>
            ) : (
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
            )}
          </section>

          {/* Member repos */}
          <section>
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
              <div className="bg-card border border-border rounded-xl overflow-hidden divide-y divide-border">
                {project.repos.map(repo => (
                  <div
                    key={repo}
                    className="flex items-center justify-between px-4 py-2 hover:bg-secondary/20 group"
                  >
                    <Link
                      to="/repos/$repoName"
                      params={{ repoName: encodeURIComponent(repo) }}
                      className="font-mono text-sm text-foreground hover:underline"
                    >
                      {repo}
                    </Link>
                    <button
                      type="button"
                      onClick={() => handleRemoveRepo(repo)}
                      disabled={removingRepo === repo}
                      className="text-muted-foreground hover:text-red-600 transition-colors disabled:opacity-50"
                      title="Remove from project"
                    >
                      {removingRepo === repo ? (
                        <Loader2 className="w-3.5 h-3.5 animate-spin" />
                      ) : (
                        <X className="w-3.5 h-3.5" />
                      )}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}
