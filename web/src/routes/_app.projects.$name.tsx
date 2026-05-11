import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useMemo, useRef, useState } from 'react'
import { ChevronLeft, ChevronDown, ChevronRight, FolderKanban, ListTodo, MessageSquare, Sparkles, X, Trash2, Plus, Loader2, Activity, RotateCw, Copy, Check, Settings2 } from 'lucide-react'
import { cn } from '../lib/utils'
import { useChatDrawer } from '../lib/chatContext'
import { Markdown } from '../components/Markdown'
import { ChatPanel } from '../components/ChatPanel'
import { useHealthCheck } from '../components/HealthCheckCard'
import {
  IssueList,
  PROJECT_SECTIONS,
  useIssueGroups,
  type IssueEntry,
  type PendingEntry,
  type Run,
} from '../components/IssueList'
import { useRepoInfoMap } from '../lib/vcsUrls'
import { useConfigChanged } from '../lib/configEvents'

// Long claude -p run (typically 30s–2min). The job runs server-side
// so the user is free to navigate away — re-opening the page resumes
// the spinner via /api/project/generate-context/status polling.
const GENERATE_HINT = 'Calling claude -p — usually 30s to 2min. Runs in the background, you can close this tab.'

interface Project {
  name: string
  repos: string[]
  created_at?: string
  context_md?: string
  testing_md?: string
  deployment_md?: string
  goals_md?: string
  automation?: ProjectAutomation
}

interface ProjectAutomation {
  enabled: boolean
  cooldown_minutes: number
  last_woken_at?: string
}

interface RepoEntry {
  full_name: string
  auto_approve?: boolean
  auto_merge?: boolean
  enabled?: boolean
}

interface PilotRun {
  project: string
  started_at: string
  ended_at?: string
  status: string
  result?: string
  error?: string
  summary?: string
  usage?: {
    duration_ms: number
    num_turns: number
    total_cost_usd: number
    input_tokens: number
    output_tokens: number
  }
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
  const navigate = useNavigate()
  const chatDrawer = useChatDrawer()

  // Health-check state lives in the parent so the compact summary
  // card (status row) and the full-width review panel (under the
  // repo list) can both consume the same hook instance.
  const healthCheck = useHealthCheck(name)

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

  // Per-repo auto_approve / auto_merge state, keyed by full_name. Populated
  // from /data/repos.json on mount so the member-repo list can render the
  // automation rollup badges without waiting for the add-repo dropdown to
  // open. Empty until the fetch resolves.
  const [repoMeta, setRepoMeta] = useState<Record<string, RepoEntry>>({})

  // Remove repo state
  const [removingRepo, setRemovingRepo] = useState<string | null>(null)

  // Pilot runs state
  const [pilotRuns, setPilotRuns] = useState<PilotRun[]>([])
  const [pilotRunsOpen, setPilotRunsOpen] = useState(false)
  const [pilotRunsVisible, setPilotRunsVisible] = useState(5)
  const [pilotRunDetail, setPilotRunDetail] = useState<PilotRun | null>(null)
  const [pilotCopied, setPilotCopied] = useState(false)

  // Automation popover
  const [automationPopoverOpen, setAutomationPopoverOpen] = useState(false)
  // Health check popover
  const [healthCheckPopoverOpen, setHealthCheckPopoverOpen] = useState(false)

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

  // Collapsed-by-default state for the two docs. Both can be long
  // (especially context.md), so the page would otherwise scroll past
  // the member-repo list for non-trivial projects. Empty docs render
  // their own "no content yet" panel and ignore this state.
  const [contextOpen, setContextOpen] = useState(false)
  const [testingOpen, setTestingOpen] = useState(false)
  const [deploymentOpen, setDeploymentOpen] = useState(false)
  const [goalsOpen, setGoalsOpen] = useState(false)
  const [goalsChatOpen, setGoalsChatOpen] = useState(false)

  // Generate deployment state — mirrors the context generation pattern.
  const [generatingDeployment, setGeneratingDeployment] = useState(false)
  const [generateDeploymentError, setGenerateDeploymentError] = useState<string | null>(null)
  const deploymentPollTimerRef = useRef<number | null>(null)

  // Cross-repo backlog state — same data sources the per-repo page
  // reads, just filtered to project.repos here. The shared IssueList
  // component does the merging, sectioning, and rendering.
  const [allIssues, setAllIssues] = useState<IssueEntry[]>([])
  const [allRuns, setAllRuns] = useState<Run[]>([])
  const [allPending, setAllPending] = useState<PendingEntry[]>([])
  const [backlogExpanded, setBacklogExpanded] = useState<Set<string>>(new Set())
  const [backlogSyncing, setBacklogSyncing] = useState(false)
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
        // Seed the cooldown input from the persisted value. Only do this
        // on initial load (or when the field comes back empty) so a user
        // mid-edit doesn't have their typing wiped by a refetch.
        if (data?.automation && (cooldownDraft === '30' || !cooldownDraft)) {
          setCooldownDraft(String(data.automation.cooldown_minutes ?? 30))
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
    // Always fetch repo metadata on mount so the member-repo list can
    // render auto_approve / auto_merge badges immediately. The add-repo
    // dropdown re-fetches on open to catch any repos added since.
    fetchAvailableRepos()
    fetchPilotRuns()
    fetchBacklog()
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
    fetchBacklog()
  })

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

  // fetchBacklog pulls the three snapshot files in parallel. No
  // server-side filtering — the backlog is a small client-side
  // .filter() across what's already on disk.
  function fetchBacklog() {
    Promise.all([
      fetch('/data/issues.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/data/runs.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetch('/data/pending.json', { cache: 'no-store' }).then(r => (r.ok ? r.json() : [])).catch(() => []),
    ]).then(([issues, runs, pending]) => {
      setAllIssues(Array.isArray(issues) ? issues : [])
      setAllRuns(Array.isArray(runs) ? runs : [])
      setAllPending(Array.isArray(pending) ? pending : [])
    })
  }

  // syncBacklog calls /api/repo/refresh-issues for every member repo
  // sequentially — same endpoint the repo page's Sync button uses.
  // Sequential because the endpoint hits the VCS provider per call;
  // parallel fan-out would just multiply rate-limit pressure.
  async function syncBacklog() {
    if (!project || backlogSyncing) return
    setBacklogSyncing(true)
    try {
      for (const repo of project.repos ?? []) {
        try {
          await fetch('/api/repo/refresh-issues', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ repo }),
          })
        } catch {
          // One repo failing (auth, network) shouldn't abort the rest.
        }
      }
      fetchBacklog()
    } finally {
      setBacklogSyncing(false)
    }
  }

  function fetchPilotRuns() {
    fetch(`/api/project/pilot-runs?project=${encodeURIComponent(name)}`, { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then(data => setPilotRuns(Array.isArray(data) ? data : []))
  }

  // Persist a goals.md draft produced in the ChatPanel. Refetches the
  // project on success so the card re-renders with the new content
  // immediately (rather than waiting for the next page load).
  async function saveGoals(content: string) {
    const r = await fetch('/api/project/write-goals', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ project: name, content }),
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok || d.error) throw new Error(d.error || `HTTP ${r.status}`)
    // Best-effort refresh; if the refetch fails the user can still see
    // the card update on next navigation.
    fetch(`/api/project/get?name=${encodeURIComponent(name)}`, { cache: 'no-store' })
      .then(res => (res.ok ? res.json() : null))
      .then(data => {
        if (data && !data.error) setProject(data as Project)
      })
      .catch(() => {})
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
                {/* At-a-glance status pills: Pilot scheduling state and
                    latest health-check outcome. They duplicate info
                    surfaced lower on the page on purpose — the user
                    might be looking at the repo list far from those
                    sections and still want a quick read on whether
                    automation is on and whether the docs are healthy. */}
                <span>·</span>
                <div className="relative">
                  <button
                    type="button"
                    onClick={() => { setAutomationPopoverOpen(o => !o); setHealthCheckPopoverOpen(false) }}
                    className={cn(
                      'inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[10px] font-medium transition-colors',
                      project.automation?.enabled
                        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400 hover:bg-emerald-200 dark:hover:bg-emerald-950/60'
                        : 'bg-slate-100 text-slate-600 dark:bg-slate-800/60 dark:text-slate-400 hover:bg-slate-200 dark:hover:bg-slate-700/60',
                    )}
                    title="Automation settings"
                  >
                    <span
                      className={cn(
                        'inline-block w-1.5 h-1.5 rounded-full',
                        project.automation?.enabled ? 'bg-emerald-500 animate-pulse' : 'bg-slate-400',
                      )}
                    />
                    Pilot {project.automation?.enabled ? 'on' : 'off'}
                    <Settings2 className="w-2.5 h-2.5 ml-0.5 opacity-60" />
                  </button>
                  {automationPopoverOpen && (
                    <>
                      <div className="fixed inset-0 z-10" onClick={() => setAutomationPopoverOpen(false)} />
                      <div className="absolute left-0 top-full mt-2 z-20 w-72 bg-card border border-border rounded-xl shadow-lg p-4">
                        <div className="flex items-center justify-between mb-3">
                          <span className="text-sm font-semibold text-foreground">Automation</span>
                          <button type="button" onClick={() => setAutomationPopoverOpen(false)} className="text-muted-foreground hover:text-foreground"><X className="w-3.5 h-3.5" /></button>
                        </div>
                        <div className="flex items-center gap-3 mb-2">
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
                              'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors',
                              project.automation?.enabled ? 'bg-emerald-500' : 'bg-slate-300 dark:bg-slate-700',
                              automationSaving && 'opacity-50 cursor-not-allowed',
                            )}
                          >
                            <span className={cn('inline-block h-5 w-5 transform rounded-full bg-white shadow transition-transform', project.automation?.enabled ? 'translate-x-5' : 'translate-x-0.5')} />
                          </button>
                          <span className={cn('text-sm font-medium', project.automation?.enabled ? 'text-emerald-700 dark:text-emerald-400' : 'text-slate-600 dark:text-slate-400')}>
                            {project.automation?.enabled ? 'Enabled' : 'Disabled'}
                          </span>
                        </div>
                        <p className="text-xs text-muted-foreground mb-3">
                          When on, every <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">clawflow run</code> pass wakes the Pilot. It triages the backlog: files new work, fixes missing labels, closes stale/duplicate issues, and resolves merge conflicts on existing PRs.
                        </p>
                        {project.automation?.last_woken_at && (
                          <p className="text-[11px] text-muted-foreground mb-3 tabular-nums">
                            Last woken {new Date(project.automation.last_woken_at).toLocaleString()}
                          </p>
                        )}
                        <div className="flex items-center gap-2">
                          <label className="text-xs text-muted-foreground shrink-0">Cooldown (min)</label>
                          <input
                            type="number"
                            min={0}
                            step={1}
                            value={cooldownDraft}
                            onChange={e => setCooldownDraft(e.target.value)}
                            className="w-16 px-2 py-1 bg-background border border-border rounded-lg text-sm text-right tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
                          />
                          <button
                            type="button"
                            onClick={() => {
                              const cd = parseInt(cooldownDraft, 10)
                              saveAutomation(project.automation?.enabled ?? false, isNaN(cd) ? 30 : cd)
                            }}
                            disabled={automationSaving || parseInt(cooldownDraft, 10) === (project.automation?.cooldown_minutes ?? 30)}
                            className="px-2 py-1 rounded-md text-xs font-medium border border-border text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                          >
                            Save
                          </button>
                        </div>
                        {automationError && <p className="mt-2 text-xs text-red-600">{automationError}</p>}
                      </div>
                    </>
                  )}
                </div>
                <div className="relative">
                  {healthCheck.status === 'done' && healthCheck.result?.outcome === 'healthy' && (
                    <button
                      type="button"
                      onClick={() => { setHealthCheckPopoverOpen(o => !o); setAutomationPopoverOpen(false) }}
                      className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400 hover:bg-emerald-200 dark:hover:bg-emerald-950/60 transition-colors"
                    >
                      ✓ healthy
                      <Settings2 className="w-2.5 h-2.5 ml-0.5 opacity-60" />
                    </button>
                  )}
                  {healthCheck.status === 'done' && healthCheck.result?.outcome === 'changes-proposed' && (
                    <button
                      type="button"
                      onClick={() => { setHealthCheckPopoverOpen(o => !o); setAutomationPopoverOpen(false) }}
                      className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-amber-100 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400 hover:bg-amber-200 dark:hover:bg-amber-950/60 transition-colors"
                    >
                      ⚠ {healthCheck.result.changes.length} pending
                      <Settings2 className="w-2.5 h-2.5 ml-0.5 opacity-60" />
                    </button>
                  )}
                  {healthCheck.status === 'running' && (
                    <button
                      type="button"
                      onClick={() => { setHealthCheckPopoverOpen(o => !o); setAutomationPopoverOpen(false) }}
                      className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-blue-100 text-blue-700 dark:bg-blue-950/40 dark:text-blue-400 hover:bg-blue-200 dark:hover:bg-blue-950/60 transition-colors"
                    >
                      <Loader2 className="w-2.5 h-2.5 animate-spin" /> running
                    </button>
                  )}
                  {healthCheck.status === 'idle' && (
                    <button
                      type="button"
                      onClick={() => { setHealthCheckPopoverOpen(o => !o); setAutomationPopoverOpen(false) }}
                      className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-slate-100 text-slate-600 dark:bg-slate-800/60 dark:text-slate-400 hover:bg-slate-200 dark:hover:bg-slate-700/60 transition-colors"
                    >
                      health check
                      <Settings2 className="w-2.5 h-2.5 ml-0.5 opacity-60" />
                    </button>
                  )}
                  {healthCheckPopoverOpen && (
                    <>
                      <div className="fixed inset-0 z-10" onClick={() => setHealthCheckPopoverOpen(false)} />
                      <div className="absolute left-0 top-full mt-2 z-20 w-80 bg-card border border-border rounded-xl shadow-lg p-4">
                        <div className="flex items-center justify-between mb-3">
                          <span className="text-sm font-semibold text-foreground">Health Check</span>
                          <button type="button" onClick={() => setHealthCheckPopoverOpen(false)} className="text-muted-foreground hover:text-foreground"><X className="w-3.5 h-3.5" /></button>
                        </div>
                        <p className="text-xs text-muted-foreground mb-3">
                          Audits each repo's CLAUDE.md and project docs against a 12-dimension rubric.
                        </p>
                        {healthCheck.status === 'done' && healthCheck.endedAt && (
                          <p className="text-[11px] text-muted-foreground mb-3 tabular-nums">
                            Last run {new Date(healthCheck.endedAt).toLocaleString()}
                          </p>
                        )}
                        {healthCheck.error && (
                          <div className="mb-3 text-xs text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 dark:bg-red-950/30 dark:border-red-900">
                            {healthCheck.error}
                          </div>
                        )}
                        <div className="flex items-center gap-2">
                          <button
                            type="button"
                            onClick={() => { healthCheck.handleRun(); setHealthCheckPopoverOpen(false) }}
                            disabled={healthCheck.status === 'running'}
                            className={cn(
                              'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors',
                              'bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed',
                            )}
                          >
                            {healthCheck.status === 'running' ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Activity className="w-3.5 h-3.5" />}
                            {healthCheck.status === 'running' ? 'Running…' : healthCheck.status === 'done' ? 'Re-run' : 'Run'}
                          </button>
                          {healthCheck.result?.outcome === 'changes-proposed' && (
                            <button
                              type="button"
                              onClick={() => setHealthCheckPopoverOpen(false)}
                              className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border border-border text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors"
                            >
                              View changes
                            </button>
                          )}
                        </div>
                      </div>
                    </>
                  )}
                </div>
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

          {/* Pilot Activity — collapsible log of recent Pilot wakes with
              their PILOT-RESULT summaries, cost, and duration. Always
              rendered (even with 0 runs) so the board is discoverable;
              empty state explains why there's nothing yet. */}
          <section className="mb-6">
            <div className="bg-card border border-border rounded-xl overflow-hidden">
              <button
                type="button"
                onClick={() => setPilotRunsOpen(o => !o)}
                className="w-full flex items-center gap-2 px-4 py-3 hover:bg-secondary/30 transition-colors text-left"
                aria-expanded={pilotRunsOpen}
              >
                {pilotRunsOpen ? <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" /> : <ChevronRight className="w-4 h-4 text-muted-foreground shrink-0" />}
                <Activity className="w-4 h-4 text-muted-foreground" />
                <span className="text-sm font-semibold text-foreground">Pilot Activity</span>
                <span className="text-xs text-muted-foreground tabular-nums">
                  {pilotRuns.length} run{pilotRuns.length !== 1 ? 's' : ''}
                </span>
                {!pilotRunsOpen && pilotRuns.length === 0 && (
                  <span className="text-xs text-muted-foreground truncate ml-1">
                    · no wakes yet
                  </span>
                )}
                {!pilotRunsOpen && pilotRuns[0]?.result && (
                  <span
                    className="text-xs text-muted-foreground truncate ml-1"
                    title={pilotRuns[0].result.replace(/^PILOT-RESULT:\s*/, '')}
                  >
                    · {pilotRuns[0].result.replace(/^PILOT-RESULT:\s*/, '')}
                  </span>
                )}
                {pilotRunsOpen && pilotRuns.length > 0 && (
                  <button
                    type="button"
                    onClick={e => {
                      e.stopPropagation()
                      const text = pilotRuns.map(r => {
                        const ts = new Date(r.started_at).toLocaleString()
                        const result = r.result ? r.result.replace(/^PILOT-RESULT:\s*/, '') : r.error ?? 'no result'
                        const cost = r.usage ? ` $${r.usage.total_cost_usd.toFixed(2)}` : ''
                        return `[${ts}]${cost} ${result}`
                      }).join('\n')
                      navigator.clipboard.writeText(text).then(() => {
                        setPilotCopied(true)
                        setTimeout(() => setPilotCopied(false), 2000)
                      })
                    }}
                    className="ml-auto inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs text-muted-foreground hover:text-foreground hover:bg-secondary/50 transition-colors shrink-0"
                    title="Copy all activity"
                  >
                    {pilotCopied ? <Check className="w-3 h-3 text-emerald-500" /> : <Copy className="w-3 h-3" />}
                    {pilotCopied ? 'Copied' : 'Copy'}
                  </button>
                )}
              </button>
              {pilotRunsOpen && pilotRuns.length === 0 && (
                <div className="border-t border-border px-4 py-6 text-center">
                  <p className="text-sm text-muted-foreground mb-1">No Pilot wakes recorded yet.</p>
                  <p className="text-xs text-muted-foreground">
                    {project.automation?.enabled
                      ? 'Wait for the next clawflow run pass — the Pilot wakes after operators finish.'
                      : 'Enable Pilot scheduling above to start. Each clawflow run pass will record a wake here.'}
                  </p>
                </div>
              )}
              {pilotRunsOpen && pilotRuns.length > 0 && (
                <div className="border-t border-border divide-y divide-border">
                  {pilotRuns.slice(0, pilotRunsVisible).map((run, i) => (
                      <button
                        key={i}
                        type="button"
                        onClick={() => setPilotRunDetail(run)}
                        className="w-full text-left px-4 py-3 hover:bg-secondary/20 transition-colors"
                      >
                        <div className="flex items-center gap-2 mb-1">
                          <span
                            className={cn(
                              'inline-block w-2 h-2 rounded-full shrink-0',
                              run.status === 'success' ? 'bg-emerald-500' : run.status === 'failed' ? 'bg-red-500' : 'bg-amber-500 animate-pulse',
                            )}
                          />
                          <span className="text-xs text-muted-foreground tabular-nums">
                            {new Date(run.started_at).toLocaleString()}
                          </span>
                          {run.ended_at && (
                            <span className="text-xs text-muted-foreground tabular-nums">
                              · {Math.round((new Date(run.ended_at).getTime() - new Date(run.started_at).getTime()) / 1000)}s
                            </span>
                          )}
                          {run.usage && (
                            <span className="text-xs text-muted-foreground tabular-nums">
                              · ${run.usage.total_cost_usd.toFixed(2)}
                            </span>
                          )}
                          {run.usage && (
                            <span className="text-xs text-muted-foreground tabular-nums">
                              · {run.usage.num_turns} turns
                            </span>
                          )}
                        </div>
                        {run.result ? (
                          <p className="text-sm text-foreground truncate" title={run.result.replace(/^PILOT-RESULT:\s*/, '')}>
                            {run.result.replace(/^PILOT-RESULT:\s*/, '')}
                          </p>
                        ) : run.error ? (
                          <p className="text-sm text-red-600 font-mono text-xs truncate" title={run.error}>{run.error}</p>
                        ) : (
                          <p className="text-xs text-muted-foreground italic">no result line</p>
                        )}
                      </button>
                    ))}
                    {pilotRuns.length > pilotRunsVisible && (
                      <button
                        type="button"
                        onClick={() => setPilotRunsVisible(v => v + 10)}
                        className="w-full px-4 py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-secondary/20 transition-colors text-center"
                      >
                        Show {Math.min(10, pilotRuns.length - pilotRunsVisible)} more ({pilotRuns.length - pilotRunsVisible} remaining)
                      </button>
                    )}
                </div>
              )}
            </div>
          </section>

          {pilotRunDetail && <PilotRunDetailModal run={pilotRunDetail} onClose={() => setPilotRunDetail(null)} />}

          {goalsChatOpen && (
            <ChatPanel
              kind="goals"
              project={name}
              draftTag="goals.md"
              initialDraft={project.goals_md ?? ''}
              onSave={async content => { await saveGoals(content); setGoalsChatOpen(false) }}
              onClose={() => setGoalsChatOpen(false)}
            />
          )}

          {/* Member repos — moved above automation/health-check so the
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
                  const approveOn = known.filter(m => m.auto_approve).length
                  const mergeOn = known.filter(m => m.auto_merge).length
                  const pmOn = project.automation?.enabled
                  const allOn = pmOn && approveOn === project.repos.length && mergeOn === project.repos.length
                  return (
                    <div className="text-xs text-muted-foreground mb-2 flex items-center gap-3 flex-wrap">
                      <span className="tabular-nums">
                        auto-approve: <span className="font-medium text-foreground">{approveOn}/{project.repos.length}</span>
                      </span>
                      <span className="tabular-nums">
                        auto-merge: <span className="font-medium text-foreground">{mergeOn}/{project.repos.length}</span>
                      </span>
                      {allOn ? (
                        <span className="text-emerald-700 dark:text-emerald-400 font-medium">· fully autopiloted</span>
                      ) : pmOn ? (
                        <span>· Pilot scheduling on; some repos need manual approve/merge</span>
                      ) : (
                        <span>· Pilot scheduling off — toggle Automation above</span>
                      )}
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
                          to="/repos/$repoName"
                          params={{ repoName: encodeURIComponent(repo) }}
                          className="font-mono text-sm text-foreground hover:underline truncate"
                          title={repo}
                        >
                          {repo}
                        </Link>
                        <div className="flex items-center gap-1.5 shrink-0">
                          <span
                            className={cn(
                              'px-1.5 py-0.5 rounded text-[10px] font-medium tabular-nums',
                              m?.auto_approve
                                ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400'
                                : 'bg-secondary text-muted-foreground',
                            )}
                            title={m?.auto_approve ? 'auto_approve enabled' : 'auto_approve off'}
                          >
                            approve {m?.auto_approve ? 'on' : 'off'}
                          </span>
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
                  <span className="text-sm font-semibold text-foreground">Context</span>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {(project.context_md.length / 1024).toFixed(1)}kb
                  </span>
                  {!contextOpen && (
                    <span className="text-xs text-muted-foreground truncate">
                      · {previewMD(project.context_md)}
                    </span>
                  )}
                  <span className="ml-auto text-xs text-muted-foreground inline-flex items-center gap-1 shrink-0">
                    <MessageSquare className="w-3 h-3" />
                    Edit via chat
                  </span>
                </button>
                {contextOpen && (
                  <div className="px-4 pb-4 pt-0 border-t border-border">
                    <Markdown>{project.context_md}</Markdown>
                  </div>
                )}
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between mb-2">
                  <h2 className="text-sm font-semibold text-foreground">Context</h2>
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

          {/* Goals.md — collapsible. The user's requirements file:
              priorities, objectives, constraints. Read-only for the
              Pilot, who reads it as its "flight plan". Edit flow uses
              the embedded ChatPanel (Draft with AI) — never auto-
              generated since the user owns these priorities. */}
          <section className="mb-3">
            {project.goals_md ? (
              <div className="bg-card border border-border rounded-xl overflow-hidden">
                <button
                  type="button"
                  onClick={() => setGoalsOpen(o => !o)}
                  className="w-full flex items-center gap-2 px-4 py-3 hover:bg-secondary/30 transition-colors text-left"
                  aria-expanded={goalsOpen}
                >
                  {goalsOpen ? <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" /> : <ChevronRight className="w-4 h-4 text-muted-foreground shrink-0" />}
                  <span className="text-sm font-semibold text-foreground">Goals & priorities</span>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {(project.goals_md.length / 1024).toFixed(1)}kb
                  </span>
                  {!goalsOpen && (
                    <span className="text-xs text-muted-foreground truncate">
                      · {previewMD(project.goals_md)}
                    </span>
                  )}
                  <span
                    role="button"
                    tabIndex={0}
                    onClick={e => { e.stopPropagation(); setGoalsChatOpen(true) }}
                    onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); setGoalsChatOpen(true) } }}
                    className="ml-auto text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 shrink-0 px-2 py-0.5 rounded hover:bg-secondary/50 transition-colors cursor-pointer"
                  >
                    <MessageSquare className="w-3 h-3" />
                    Edit with AI
                  </span>
                </button>
                {goalsOpen && (
                  <div className="px-4 pb-4 pt-0 border-t border-border">
                    <Markdown>{project.goals_md}</Markdown>
                  </div>
                )}
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between mb-2">
                  <h2 className="text-sm font-semibold text-foreground">Goals & priorities</h2>
                  <span className="text-xs text-muted-foreground">
                    What you want the Pilot to focus on; constraints; current priorities
                  </span>
                </div>
                <div className="bg-card border border-border rounded-xl p-6 text-center space-y-3">
                  <p className="text-sm text-muted-foreground">
                    No <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">goals.md</code> yet.
                    Tell the Pilot what's important — chat with AI to draft your priorities and constraints.
                  </p>
                  <button
                    type="button"
                    onClick={() => setGoalsChatOpen(true)}
                    className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors bg-primary text-primary-foreground hover:bg-primary/90"
                  >
                    <Sparkles className="w-3.5 h-3.5" />
                    Draft with AI
                  </button>
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
                  <span className="text-sm font-semibold text-foreground">Local environment SOP</span>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {(project.testing_md.length / 1024).toFixed(1)}kb
                  </span>
                  {!testingOpen && (
                    <span className="text-xs text-muted-foreground truncate">
                      · {previewMD(project.testing_md)}
                    </span>
                  )}
                  <span className="ml-auto text-xs text-muted-foreground inline-flex items-center gap-1 shrink-0">
                    <MessageSquare className="w-3 h-3" />
                    Edit via chat
                  </span>
                </button>
                {testingOpen && (
                  <div className="px-4 pb-4 pt-0 border-t border-border">
                    <Markdown>{project.testing_md}</Markdown>
                  </div>
                )}
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between mb-2">
                  <h2 className="text-sm font-semibold text-foreground">Local environment SOP</h2>
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
                  <span className="text-sm font-semibold text-foreground">Deployment</span>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {(project.deployment_md.length / 1024).toFixed(1)}kb
                  </span>
                  {!deploymentOpen && (
                    <span className="text-xs text-muted-foreground truncate">
                      · {previewMD(project.deployment_md)}
                    </span>
                  )}
                  <span className="ml-auto text-xs text-muted-foreground inline-flex items-center gap-1 shrink-0">
                    <MessageSquare className="w-3 h-3" />
                    Edit via chat
                  </span>
                </button>
                {deploymentOpen && (
                  <div className="px-4 pb-4 pt-0 border-t border-border">
                    <Markdown>{project.deployment_md}</Markdown>
                  </div>
                )}
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between mb-2">
                  <h2 className="text-sm font-semibold text-foreground">Deployment</h2>
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

          {/* Backlog — cross-repo issue + run + pending aggregate so the
              user can see project progress without bouncing into each
              repo page. Same renderer as the per-repo page (shared
              IssueList component); the project-level section config
              adds an "Awaiting evaluation" bucket up front and shows
              the repo column on each row. */}
          <BacklogSection
            project={project}
            issues={allIssues}
            runs={allRuns}
            pending={allPending}
            repoMap={repoInfoMap}
            expanded={backlogExpanded}
            onToggle={k =>
              setBacklogExpanded(prev => {
                const next = new Set(prev)
                if (next.has(k)) next.delete(k)
                else next.add(k)
                return next
              })
            }
            onSync={syncBacklog}
            syncing={backlogSyncing}
          />

        </>
      )}
    </div>
  )
}

// BacklogSection is the project-level cross-repo issue aggregate. It
// owns the collapsed/open state and the bucket-counts header, and
// delegates the actual list rendering to the shared IssueList. Heavy
// per-row markup (StatusBadge, Timeline, etc.) lives in IssueList so
// adding/changing row chrome doesn't require editing this file too.
function BacklogSection({
  project,
  issues,
  runs,
  pending,
  repoMap,
  expanded,
  onToggle,
  onSync,
  syncing,
}: {
  project: Project
  issues: IssueEntry[]
  runs: Run[]
  pending: PendingEntry[]
  repoMap: ReturnType<typeof useRepoInfoMap>
  expanded: Set<string>
  onToggle: (key: string) => void
  onSync: () => void
  syncing: boolean
}) {
  // Filter all three datasets to this project's member repos before
  // merging. Cheap (a few hundred entries at most) so doing it inline
  // beats threading per-project filtered slices through the API.
  const memberSet = useMemo(() => new Set(project.repos ?? []), [project.repos])
  const filteredIssues = useMemo(() => issues.filter(i => memberSet.has(i.repo)), [issues, memberSet])
  const filteredRuns = useMemo(() => runs.filter(r => memberSet.has(r.repo)), [runs, memberSet])
  const filteredPending = useMemo(() => pending.filter(p => memberSet.has(p.repo)), [pending, memberSet])
  const groups = useIssueGroups({
    issues: filteredIssues,
    runs: filteredRuns,
    pending: filteredPending,
  })

  // Counts roll up per section without rebuilding the buckets — same
  // predicates as PROJECT_SECTIONS, applied once for the header
  // summary line. Cheap, no need to memoize.
  const counts = {
    inFlight: 0,
    queued: 0,
    awaiting: 0,
    done: 0,
    closed: 0,
  }
  for (const g of groups) {
    if (g.state === 'closed') counts.closed++
    else if (g.runs[0]?.status === 'running') counts.inFlight++
    else if (g.pending.length > 0) counts.queued++
    else {
      const labels = g.labels ?? []
      const hasTrigger = labels.includes('bug') || labels.includes('feature')
      const hasAgentLabel = labels.some(l => l.startsWith('agent-'))
      if (g.runs.length === 0 && hasTrigger && !hasAgentLabel) counts.awaiting++
      else counts.done++
    }
  }

  const [open, setOpen] = useState(true)

  if ((project.repos?.length ?? 0) === 0) return null

  return (
    <section className="mb-6">
      <div className="flex items-center gap-2 mb-2">
        <button
          type="button"
          onClick={() => setOpen(o => !o)}
          className="inline-flex items-center gap-1 text-sm font-semibold text-foreground hover:text-foreground"
          aria-expanded={open}
        >
          {open ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
          <ListTodo className="w-3.5 h-3.5 text-muted-foreground" />
          Backlog{' '}
          <span className="font-normal text-muted-foreground">({groups.length})</span>
        </button>
        <span className="text-xs text-muted-foreground">
          {counts.inFlight} in flight · {counts.queued} queued ·{' '}
          <span className={cn(counts.awaiting > 0 && 'text-red-600 dark:text-red-400 font-medium')}>
            {counts.awaiting} awaiting eval
          </span>{' '}
          · {counts.done} done · {counts.closed} closed
        </span>
        <button
          onClick={onSync}
          disabled={syncing}
          title="Refresh issue state for every member repo"
          className={cn(
            'ml-auto inline-flex items-center gap-1 text-xs font-medium px-2 py-1 rounded border transition-colors',
            syncing
              ? 'border-border text-muted-foreground bg-secondary/30'
              : 'border-border text-foreground hover:bg-secondary/50',
          )}
        >
          {syncing ? (
            <>
              <Loader2 className="w-3 h-3 animate-spin" /> syncing…
            </>
          ) : (
            <>
              <RotateCw className="w-3 h-3" /> Sync all
            </>
          )}
        </button>
      </div>
      {open && (
        groups.length === 0 ? (
          <p className="text-sm text-muted-foreground py-4">
            No issues across {project.repos?.length ?? 0} repo{project.repos?.length === 1 ? '' : 's'} yet.
            Click <strong>Sync all</strong> to pull current state from VCS.
          </p>
        ) : (
          <IssueList
            groups={groups}
            sections={PROJECT_SECTIONS}
            showRepo={true}
            repoMap={repoMap}
            expanded={expanded}
            onToggle={onToggle}
          />
        )
      )}
    </section>
  )
}

function PilotRunDetailModal({ run, onClose }: { run: PilotRun; onClose: () => void }) {
  useEffect(() => {
    const handleEsc = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', handleEsc)
    return () => document.removeEventListener('keydown', handleEsc)
  }, [onClose])

  const duration = run.ended_at
    ? Math.round((new Date(run.ended_at).getTime() - new Date(run.started_at).getTime()) / 1000)
    : null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm"
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="relative w-full max-w-2xl max-h-[80vh] mx-4 bg-card border border-border rounded-xl shadow-2xl flex flex-col overflow-hidden">
        <div className="flex items-center justify-between px-5 py-4 border-b border-border shrink-0">
          <div className="flex items-center gap-2">
            <span
              className={cn(
                'inline-block w-2.5 h-2.5 rounded-full shrink-0',
                run.status === 'success' ? 'bg-emerald-500' : run.status === 'failed' ? 'bg-red-500' : 'bg-amber-500 animate-pulse',
              )}
            />
            <h2 className="text-sm font-semibold text-foreground">Pilot Run Detail</h2>
            <span className="text-xs text-muted-foreground tabular-nums">
              {new Date(run.started_at).toLocaleString()}
            </span>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-muted-foreground hover:text-foreground transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
          <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
            {duration !== null && (
              <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                Duration: {duration}s
              </span>
            )}
            {run.usage && (
              <>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Cost: ${run.usage.total_cost_usd.toFixed(3)}
                </span>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Turns: {run.usage.num_turns}
                </span>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Input: {(run.usage.input_tokens / 1000).toFixed(1)}k tokens
                </span>
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-secondary rounded-md tabular-nums">
                  Output: {(run.usage.output_tokens / 1000).toFixed(1)}k tokens
                </span>
              </>
            )}
          </div>

          {run.result && (
            <div>
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Result</h3>
              <div className="text-sm text-foreground bg-secondary/40 rounded-lg px-4 py-3 whitespace-pre-wrap">
                {run.result.replace(/^PILOT-RESULT:\s*/, '')}
              </div>
            </div>
          )}

          {run.summary && (
            <div>
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Summary</h3>
              <div className="text-sm text-foreground bg-secondary/40 rounded-lg px-4 py-3 whitespace-pre-wrap">
                {run.summary}
              </div>
            </div>
          )}

          {run.error && (
            <div>
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Error</h3>
              <div className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-4 py-3 font-mono whitespace-pre-wrap dark:bg-red-950/30 dark:border-red-900">
                {run.error}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
