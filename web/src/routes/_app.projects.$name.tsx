import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronLeft, FolderKanban, Trash2, Loader2, Plus, X, RefreshCw, Zap } from 'lucide-react'
import {
  fetchProjects,
  fetchRepos,
  updateRepo,
  deleteProject,
  timeAgo,
  type Project,
  type Repo,
} from '../lib/cloudApi'
import {
  type PilotRun,
  DUTY_KEYS,
  DUTY_LABELS,
  dutyStatusColour,
  PilotRunDetailModal,
} from '../components/PilotRun'
import { cn } from '../lib/utils'

export const Route = createFileRoute('/_app/projects/$name')({
  component: ProjectDetail,
})

function ProjectDetail() {
  const { name } = Route.useParams()
  const navigate = useNavigate()

  const [projects, setProjects] = useState<Project[]>([])
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Per-row pending state for repo attach/detach (keyed by repo id).
  const [pendingRepoId, setPendingRepoId] = useState<string | null>(null)
  const [repoActionError, setRepoActionError] = useState<string | null>(null)

  // Add-repo dropdown selection.
  const [addRepoId, setAddRepoId] = useState<string>('')

  // Delete-project confirmation.
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  // Pilot wake history for this project. Local-mode-only — the cloud
  // endpoint returns 404 and pilotRuns stays empty so the section
  // collapses to nothing. The full history page is /pilot-runs;
  // here we surface only the latest with-duties wake + a Wake button.
  const [pilotRuns, setPilotRuns] = useState<PilotRun[]>([])
  const [pilotRunDetail, setPilotRunDetail] = useState<PilotRun | null>(null)
  const [waking, setWaking] = useState(false)
  const [wakeMessage, setWakeMessage] = useState<string | null>(null)

  const load = () => {
    setLoading(true)
    setError(null)
    Promise.all([fetchProjects(), fetchRepos()])
      .then(([p, r]) => {
        setProjects(p.projects ?? [])
        setRepos(r.repos ?? [])
      })
      .catch(e => setError(String(e)))
      .finally(() => setLoading(false))
  }

  useEffect(load, [])

  // fetchPilotRuns hits the local-mode endpoint. Cloud returns 404 →
  // catch swallows it, list stays empty, the Pilot section collapses.
  const fetchPilotRuns = useCallback(() => {
    fetch(`/api/project/pilot-runs?project=${encodeURIComponent(name)}`, { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .then(data => setPilotRuns(Array.isArray(data) ? data : []))
      .catch(() => setPilotRuns([]))
  }, [name])

  useEffect(() => {
    fetchPilotRuns()
    // Refresh every 30s while the page is open so a manual wake's
    // result lands without the user having to refresh.
    const id = setInterval(fetchPilotRuns, 30_000)
    return () => clearInterval(id)
  }, [fetchPilotRuns])

  const handleWake = useCallback(async () => {
    setWaking(true)
    setWakeMessage(null)
    try {
      const r = await fetch('/api/project/pilot/wake', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project: name }),
      })
      const data = await r.json().catch(() => ({}))
      if (!r.ok) {
        setWakeMessage(data.error || `Wake failed (${r.status})`)
        return
      }
      setWakeMessage('Wake started — refreshing pilot activity…')
      // Re-poll a few times to catch the new run as it appears.
      fetchPilotRuns()
      setTimeout(fetchPilotRuns, 2_000)
      setTimeout(fetchPilotRuns, 5_000)
    } catch (e) {
      setWakeMessage(e instanceof Error ? e.message : String(e))
    } finally {
      setWaking(false)
    }
  }, [name, fetchPilotRuns])

  // The most-recent successful wake whose output included duties.
  // Used to render the at-a-glance duty digest. Older shapeless wakes
  // (pre-duty schema) are skipped — they're still in the history page.
  const latestWakeWithDuties = useMemo<PilotRun | null>(() => {
    for (const r of pilotRuns) {
      if (r.status === 'success' && r.duties) return r
    }
    return null
  }, [pilotRuns])

  // Look up the project by name (URL slug). Cloud projects have an opaque
  // id we use for API calls, but the URL key is the human-readable name.
  const project = useMemo(
    () => projects.find(p => p.name === name) ?? null,
    [projects, name],
  )

  const memberRepos = useMemo(
    () => (project ? repos.filter(r => r.project_id === project.id) : []),
    [repos, project],
  )

  const availableRepos = useMemo(
    () => (project ? repos.filter(r => r.project_id !== project.id) : []),
    [repos, project],
  )

  async function handleAttachRepo() {
    if (!project || !addRepoId) return
    setPendingRepoId(addRepoId)
    setRepoActionError(null)
    try {
      await updateRepo(addRepoId, { project_id: project.id })
      setAddRepoId('')
      load()
    } catch (e) {
      setRepoActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setPendingRepoId(null)
    }
  }

  async function handleDetachRepo(repo: Repo) {
    setPendingRepoId(repo.id)
    setRepoActionError(null)
    try {
      // Empty string clears the project_id on the server side.
      await updateRepo(repo.id, { project_id: '' })
      load()
    } catch (e) {
      setRepoActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setPendingRepoId(null)
    }
  }

  async function handleDeleteProject() {
    if (!project) return
    setDeleting(true)
    setDeleteError(null)
    try {
      await deleteProject(project.id)
      navigate({ to: '/projects' })
    } catch (e) {
      setDeleteError(e instanceof Error ? e.message : String(e))
      setDeleting(false)
    }
  }

  if (loading) {
    return (
      <div className="px-6 py-6 max-w-5xl mx-auto">
        <p className="text-sm" style={{ color: 'hsl(var(--text-low))' }}>Loading…</p>
      </div>
    )
  }

  if (!project) {
    return (
      <div className="px-6 py-6 max-w-5xl mx-auto">
        <Link
          to="/projects"
          className="inline-flex items-center gap-1.5 text-xs mb-4"
          style={{ color: 'hsl(var(--text-low))' }}
        >
          <ChevronLeft size={14} />
          Back to projects
        </Link>
        {error ? (
          <div
            className="px-4 py-3 rounded-md text-sm border"
            style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
          >
            {error}
          </div>
        ) : (
          <p className="text-sm" style={{ color: 'hsl(var(--text-low))' }}>
            Project <span className="font-mono">{name}</span> not found.
          </p>
        )}
      </div>
    )
  }

  return (
    <div className="px-6 py-6 max-w-5xl mx-auto">
      <Link
        to="/projects"
        className="inline-flex items-center gap-1.5 text-xs mb-4"
        style={{ color: 'hsl(var(--text-low))' }}
      >
        <ChevronLeft size={14} />
        Back to projects
      </Link>

      <div className="flex items-start justify-between mb-5">
        <div>
          <h1 className="text-base font-semibold flex items-center gap-2" style={{ color: 'hsl(var(--text-high))' }}>
            <FolderKanban size={16} style={{ color: 'hsl(var(--text-low))' }} />
            <span className="font-mono">{project.name}</span>
          </h1>
          {project.description && (
            <p className="text-sm mt-1" style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
              {project.description}
            </p>
          )}
          <p className="text-xs mt-1" style={{ color: 'hsl(var(--text-low))' }}>
            Created {timeAgo(project.created_at)}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={load}
            className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors"
            style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
          >
            <RefreshCw size={12} />
            Refresh
          </button>
          <button
            onClick={() => { setShowDeleteConfirm(true); setDeleteError(null) }}
            className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors"
            style={{
              borderColor: 'hsl(0 70% 50% / 0.5)',
              color: 'hsl(0 70% 60%)',
            }}
          >
            <Trash2 size={12} />
            Delete project
          </button>
        </div>
      </div>

      {error && (
        <div
          className="mb-4 px-4 py-3 rounded-md text-sm border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
        >
          {error}
        </div>
      )}

      {/* Pilot section — only renders when /api/project/pilot-runs
          returned data (i.e. local mode AND this project has wake
          history). Cloud anon mode quietly hides it. */}
      {pilotRuns.length > 0 && (
        <section className="mb-6">
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-sm font-semibold flex items-center gap-2" style={{ color: 'hsl(var(--text-high))' }}>
              <Zap size={14} style={{ color: 'hsl(var(--text-low))' }} />
              Pilot
              <span className="text-xs font-normal tabular-nums" style={{ color: 'hsl(var(--text-low))' }}>
                {pilotRuns.length} wake{pilotRuns.length === 1 ? '' : 's'}
              </span>
            </h2>
            <div className="flex items-center gap-2">
              <button
                onClick={handleWake}
                disabled={waking}
                className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
                style={{
                  borderColor: 'hsl(var(--brand))',
                  color: 'hsl(var(--brand))',
                  background: 'hsl(var(--brand) / 0.08)',
                }}
              >
                {waking ? <Loader2 size={12} className="animate-spin" /> : <Zap size={12} />}
                Wake now
              </button>
              {/* Full /pilot-runs route hasn't been ported to the
                  cloud-style React routes yet — see the legacy commit
                  at 5a4d9c9^. For now expose the modal-detail flow via
                  the "Details" button on the latest-wake card below. */}
            </div>
          </div>

          {wakeMessage && (
            <div
              className="mb-3 px-3 py-2 rounded-md text-xs border"
              style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-mid, var(--text-low)))' }}
            >
              {wakeMessage}
            </div>
          )}

          {latestWakeWithDuties && (
            <div
              className="rounded-lg border p-4"
              style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
            >
              <div className="flex items-center justify-between mb-3 flex-wrap gap-2">
                <div className="text-xs flex items-center gap-2" style={{ color: 'hsl(var(--text-low))' }}>
                  Latest wake
                  <span style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
                    {timeAgo(latestWakeWithDuties.started_at)}
                  </span>
                </div>
                <button
                  onClick={() => setPilotRunDetail(latestWakeWithDuties)}
                  className="text-xs underline-offset-2 hover:underline"
                  style={{ color: 'hsl(var(--text-low))' }}
                >
                  Details
                </button>
              </div>

              <div className="flex flex-wrap gap-1.5 mb-3">
                {DUTY_KEYS.map(k => {
                  const duty = latestWakeWithDuties.duties?.[k]
                  if (!duty) return null
                  return (
                    <span
                      key={k}
                      className={cn(
                        'inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border',
                        dutyStatusColour(duty.status),
                      )}
                      title={duty.note || duty.status}
                    >
                      {DUTY_LABELS[k]}: {duty.status}
                    </span>
                  )
                })}
              </div>

              {latestWakeWithDuties.duties?.issue_digest?.summary && (
                <p className="text-xs leading-relaxed" style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
                  {latestWakeWithDuties.duties.issue_digest.summary}
                </p>
              )}
            </div>
          )}

          {!latestWakeWithDuties && (
            <div
              className="rounded-lg border px-4 py-6 text-center text-xs"
              style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
            >
              {pilotRuns.length} wake{pilotRuns.length === 1 ? '' : 's'} on record, none with a parseable duties block yet.
            </div>
          )}
        </section>
      )}

      {pilotRunDetail && (
        <PilotRunDetailModal run={pilotRunDetail} onClose={() => setPilotRunDetail(null)} />
      )}

      <section className="mb-6">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
            Repos
          </h2>
          <span className="text-xs tabular-nums" style={{ color: 'hsl(var(--text-low))' }}>
            {memberRepos.length} {memberRepos.length === 1 ? 'repo' : 'repos'}
          </span>
        </div>

        {repoActionError && (
          <div
            className="mb-3 px-3 py-2 rounded-md text-xs border"
            style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(0 70% 60%)' }}
          >
            {repoActionError}
          </div>
        )}

        {memberRepos.length === 0 ? (
          <div
            className="rounded-lg border px-4 py-6 text-center text-xs"
            style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}
          >
            No repos attached to this project yet.
          </div>
        ) : (
          <div
            className="rounded-lg border overflow-hidden"
            style={{ borderColor: 'hsl(var(--border))' }}
          >
            <table className="w-full text-sm">
              <thead>
                <tr
                  className="text-xs font-medium border-b"
                  style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
                >
                  <th className="text-left px-4 py-2">Repo</th>
                  <th className="text-left px-4 py-2">Platform</th>
                  <th className="text-left px-4 py-2">Base branch</th>
                  <th className="text-right px-4 py-2">Actions</th>
                </tr>
              </thead>
              <tbody>
                {memberRepos.map((r, i) => (
                  <tr
                    key={r.id}
                    className="border-b last:border-b-0"
                    style={{ borderColor: 'hsl(var(--border))', background: i % 2 === 0 ? 'transparent' : 'hsl(var(--bg-panel) / 0.4)' }}
                  >
                    <td className="px-4 py-2.5 font-mono text-xs" style={{ color: 'hsl(var(--text-high))' }}>
                      {r.name}
                    </td>
                    <td className="px-4 py-2.5 text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {r.platform || '—'}
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {r.base_branch || '—'}
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex justify-end">
                        <button
                          onClick={() => handleDetachRepo(r)}
                          disabled={pendingRepoId === r.id}
                          className="flex items-center gap-1 text-xs px-2 py-1 rounded-sm border transition-colors disabled:opacity-50"
                          style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
                        >
                          {pendingRepoId === r.id ? (
                            <Loader2 size={12} className="animate-spin" />
                          ) : (
                            <X size={12} />
                          )}
                          Remove
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Attach repo affordance */}
        {availableRepos.length > 0 && (
          <div className="mt-3 flex items-center gap-2">
            <select
              value={addRepoId}
              onChange={e => setAddRepoId(e.target.value)}
              disabled={pendingRepoId !== null}
              className="text-xs px-2 py-1.5 rounded-sm border focus:outline-none disabled:opacity-50"
              style={{
                background: 'hsl(var(--bg-primary))',
                borderColor: 'hsl(var(--border))',
                color: 'hsl(var(--text-high))',
              }}
            >
              <option value="">Attach a repo…</option>
              {availableRepos.map(r => (
                <option key={r.id} value={r.id}>
                  {r.name}
                  {r.project_id ? ' (already in another project)' : ''}
                </option>
              ))}
            </select>
            <button
              onClick={handleAttachRepo}
              disabled={!addRepoId || pendingRepoId !== null}
              className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
              style={{
                background: addRepoId ? 'hsl(var(--brand))' : 'transparent',
                borderColor: addRepoId ? 'hsl(var(--brand))' : 'hsl(var(--border))',
                color: addRepoId
                  ? 'hsl(var(--brand-foreground, 0 0% 100%))'
                  : 'hsl(var(--text-low))',
              }}
            >
              {pendingRepoId !== null && pendingRepoId === addRepoId ? (
                <Loader2 size={12} className="animate-spin" />
              ) : (
                <Plus size={12} />
              )}
              Attach
            </button>
          </div>
        )}
      </section>

      {/* Delete confirmation modal */}
      {showDeleteConfirm && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center px-4"
          style={{ background: 'rgba(0,0,0,0.4)' }}
          onClick={() => !deleting && setShowDeleteConfirm(false)}
        >
          <div
            className="w-full max-w-md rounded-lg border p-5"
            style={{ background: 'hsl(var(--bg-primary))', borderColor: 'hsl(var(--border))' }}
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-sm font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
              Delete project
            </h3>
            <p className="text-sm mb-4" style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
              Are you sure you want to delete <span className="font-mono">{project.name}</span>?
              The {memberRepos.length} attached {memberRepos.length === 1 ? 'repo' : 'repos'} will be detached but not deleted.
            </p>
            {deleteError && (
              <p className="text-xs mb-3" style={{ color: 'hsl(0 70% 60%)' }}>{deleteError}</p>
            )}
            <div className="flex items-center justify-end gap-2">
              <button
                onClick={() => setShowDeleteConfirm(false)}
                disabled={deleting}
                className="text-xs px-3 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
                style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
              >
                Cancel
              </button>
              <button
                onClick={handleDeleteProject}
                disabled={deleting}
                className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
                style={{
                  background: 'hsl(0 70% 50%)',
                  borderColor: 'hsl(0 70% 50%)',
                  color: 'hsl(0 0% 100%)',
                }}
              >
                {deleting && <Loader2 size={12} className="animate-spin" />}
                Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
