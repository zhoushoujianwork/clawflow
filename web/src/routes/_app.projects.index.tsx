import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { FolderKanban, ChevronRight, Plus, Loader2, X, RefreshCw, Trash2 } from 'lucide-react'
import {
  fetchProjects,
  fetchRepos,
  createProject,
  deleteProject,
  timeAgo,
  type Project,
  type Repo,
} from '../lib/cloudApi'

export const Route = createFileRoute('/_app/projects/')({
  component: ProjectList,
})

function ProjectList() {
  const [projects, setProjects] = useState<Project[]>([])
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Create form state
  const [showCreate, setShowCreate] = useState(false)
  const [createName, setCreateName] = useState('')
  const [createDescription, setCreateDescription] = useState('')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  // Delete state — tracks which project is currently being confirmed/deleted
  const [confirmDelete, setConfirmDelete] = useState<Project | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

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

  const repoCount = (projectId: string) =>
    repos.filter(r => r.project_id === projectId).length

  async function handleCreate() {
    const name = createName.trim()
    if (!name) return
    setCreating(true)
    setCreateError(null)
    try {
      await createProject({
        name,
        description: createDescription.trim() || undefined,
      })
      setShowCreate(false)
      setCreateName('')
      setCreateDescription('')
      load()
    } catch (e) {
      setCreateError(e instanceof Error ? e.message : String(e))
    } finally {
      setCreating(false)
    }
  }

  async function handleDelete(p: Project) {
    setDeleting(true)
    setDeleteError(null)
    try {
      await deleteProject(p.id)
      setConfirmDelete(null)
      load()
    } catch (e) {
      setDeleteError(e instanceof Error ? e.message : String(e))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="px-6 py-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-base font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
          Projects
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
            onClick={() => { setShowCreate(true); setCreateError(null) }}
            className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors"
            style={{
              background: 'hsl(var(--brand))',
              borderColor: 'hsl(var(--brand))',
              color: 'hsl(var(--brand-foreground, 0 0% 100%))',
            }}
          >
            <Plus size={12} />
            New project
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

      {/* Create form */}
      {showCreate && (
        <div
          className="mb-4 rounded-lg border p-4"
          style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
        >
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-sm font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
              Create project
            </h2>
            <button
              onClick={() => { setShowCreate(false); setCreateName(''); setCreateDescription(''); setCreateError(null) }}
              className="p-1 rounded transition-colors"
              style={{ color: 'hsl(var(--text-low))' }}
              aria-label="Close"
            >
              <X size={14} />
            </button>
          </div>
          <div className="space-y-2">
            <input
              type="text"
              placeholder="Project name"
              value={createName}
              onChange={e => setCreateName(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && createName.trim()) handleCreate() }}
              className="w-full px-3 py-1.5 rounded-sm border text-sm font-mono focus:outline-none"
              style={{
                background: 'hsl(var(--bg-primary))',
                borderColor: 'hsl(var(--border))',
                color: 'hsl(var(--text-high))',
              }}
              autoFocus
            />
            <input
              type="text"
              placeholder="Description (optional)"
              value={createDescription}
              onChange={e => setCreateDescription(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && createName.trim()) handleCreate() }}
              className="w-full px-3 py-1.5 rounded-sm border text-sm focus:outline-none"
              style={{
                background: 'hsl(var(--bg-primary))',
                borderColor: 'hsl(var(--border))',
                color: 'hsl(var(--text-high))',
              }}
            />
            <div className="flex items-center gap-2">
              <button
                onClick={handleCreate}
                disabled={creating || !createName.trim()}
                className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
                style={{
                  background: 'hsl(var(--brand))',
                  borderColor: 'hsl(var(--brand))',
                  color: 'hsl(var(--brand-foreground, 0 0% 100%))',
                }}
              >
                {creating && <Loader2 size={12} className="animate-spin" />}
                Create
              </button>
              {createError && (
                <span className="text-xs" style={{ color: 'hsl(0 70% 60%)' }}>
                  {createError}
                </span>
              )}
            </div>
          </div>
        </div>
      )}

      {!loading && projects.length === 0 && !error && (
        <EmptyProjects />
      )}

      {projects.length > 0 && (
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
                <th className="text-left px-4 py-2">Name</th>
                <th className="text-left px-4 py-2">Description</th>
                <th className="text-left px-4 py-2">Repos</th>
                <th className="text-left px-4 py-2">Created</th>
                <th className="text-right px-4 py-2">Actions</th>
              </tr>
            </thead>
            <tbody>
              {projects.map((p, i) => (
                <tr
                  key={p.id}
                  className="border-b last:border-b-0"
                  style={{ borderColor: 'hsl(var(--border))', background: i % 2 === 0 ? 'transparent' : 'hsl(var(--bg-panel) / 0.4)' }}
                >
                  <td className="px-4 py-2.5">
                    <Link
                      to="/projects/$name"
                      params={{ name: p.name }}
                      className="inline-flex items-center gap-2 font-mono text-sm hover:underline"
                      style={{ color: 'hsl(var(--text-high))' }}
                    >
                      <FolderKanban size={14} style={{ color: 'hsl(var(--text-low))' }} />
                      {p.name}
                    </Link>
                  </td>
                  <td className="px-4 py-2.5 text-sm" style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
                    {p.description || '—'}
                  </td>
                  <td className="px-4 py-2.5 text-xs tabular-nums" style={{ color: 'hsl(var(--text-low))' }}>
                    {repoCount(p.id)} {repoCount(p.id) === 1 ? 'repo' : 'repos'}
                  </td>
                  <td className="px-4 py-2.5 text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                    {timeAgo(p.created_at)}
                  </td>
                  <td className="px-4 py-2.5">
                    <div className="flex items-center justify-end gap-1">
                      <button
                        onClick={() => { setConfirmDelete(p); setDeleteError(null) }}
                        className="p-1 rounded transition-colors"
                        style={{ color: 'hsl(var(--text-low))' }}
                        title="Delete"
                      >
                        <Trash2 size={14} />
                      </button>
                      <Link
                        to="/projects/$name"
                        params={{ name: p.name }}
                        className="p-1 rounded transition-colors"
                        style={{ color: 'hsl(var(--text-low))' }}
                        aria-label="View"
                      >
                        <ChevronRight size={14} />
                      </Link>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Delete confirmation modal */}
      {confirmDelete && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center px-4"
          style={{ background: 'rgba(0,0,0,0.4)' }}
          onClick={() => !deleting && setConfirmDelete(null)}
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
              Are you sure you want to delete <span className="font-mono">{confirmDelete.name}</span>?
              Repos belonging to it will be detached, but not deleted.
            </p>
            {deleteError && (
              <p className="text-xs mb-3" style={{ color: 'hsl(0 70% 60%)' }}>{deleteError}</p>
            )}
            <div className="flex items-center justify-end gap-2">
              <button
                onClick={() => setConfirmDelete(null)}
                disabled={deleting}
                className="text-xs px-3 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
                style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
              >
                Cancel
              </button>
              <button
                onClick={() => handleDelete(confirmDelete)}
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

function EmptyProjects() {
  return (
    <div
      className="rounded-lg border px-6 py-12 text-center"
      style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
    >
      <FolderKanban size={32} className="mx-auto mb-3 opacity-30" style={{ color: 'hsl(var(--text-low))' }} />
      <p className="text-sm font-medium mb-1" style={{ color: 'hsl(var(--text-high))' }}>
        No projects yet
      </p>
      <p className="text-xs mb-4" style={{ color: 'hsl(var(--text-low))' }}>
        Click <strong>New project</strong> to create one, or import an existing
        local config from a registered machine.
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
