import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { ArrowLeft, Loader2, RefreshCw, Save, Trash2 } from 'lucide-react'
import {
  deleteRepo,
  fetchBindings,
  fetchMachines,
  fetchProjects,
  fetchRepos,
  timeAgo,
  updateRepo,
  type Binding,
  type Machine,
  type Project,
  type Repo,
} from '../lib/cloudApi'
import { VcsIcon } from '../components/VcsIcon'
import type { RepoInfoMap, Platform } from '../lib/vcsUrls'

export const Route = createFileRoute('/_app/repos/$repoName')({
  component: RepoDetailPage,
})

function RepoDetailPage() {
  const { repoName } = Route.useParams()
  const fullName = decodeURIComponent(repoName)
  const navigate = useNavigate()

  const [repo, setRepo] = useState<Repo | null>(null)
  const [projects, setProjects] = useState<Project[]>([])
  const [bindings, setBindings] = useState<Binding[]>([])
  const [machines, setMachines] = useState<Machine[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [notFound, setNotFound] = useState(false)

  // Editable fields (local form state, synced from `repo` after load)
  const [baseBranch, setBaseBranch] = useState('')
  const [projectId, setProjectId] = useState('')
  const [savingBase, setSavingBase] = useState(false)
  const [savingProject, setSavingProject] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    setNotFound(false)
    Promise.all([fetchRepos(), fetchProjects(), fetchBindings(), fetchMachines()])
      .then(([r, p, b, m]) => {
        const found = (r.repos ?? []).find(x => x.name === fullName) ?? null
        setRepo(found)
        setProjects(p.projects ?? [])
        setBindings(b.bindings ?? [])
        setMachines(m.machines ?? [])
        if (found) {
          setBaseBranch(found.base_branch ?? '')
          setProjectId(found.project_id ?? '')
        } else {
          setNotFound(true)
        }
      })
      .catch(e => setError(String(e)))
      .finally(() => setLoading(false))
  }, [fullName])

  useEffect(() => {
    load()
  }, [load])

  const repoMap = useMemo<RepoInfoMap>(() => {
    if (!repo) return {}
    const platform: Platform = repo.platform === 'gitlab' ? 'gitlab' : 'github'
    return {
      [repo.name]: {
        platform,
        host: platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com',
      },
    }
  }, [repo])

  const projectName = useCallback(
    (id?: string) => {
      if (!id) return '—'
      const p = projects.find(p => p.id === id)
      return p ? p.name : id
    },
    [projects],
  )

  // Resolve bindings for this repo and pair each with its machine name.
  const repoBindings = useMemo(() => {
    if (!repo) return []
    return bindings
      .filter(b => b.repo_id === repo.id)
      .map(b => {
        const m = machines.find(m => m.id === b.machine_id)
        return {
          binding: b,
          machineName: m ? (m.display_name || m.hostname) : b.machine_id,
        }
      })
      .sort((a, b) =>
        (b.binding.updated_at || '').localeCompare(a.binding.updated_at || ''),
      )
  }, [repo, bindings, machines])

  const baseDirty = repo ? baseBranch !== (repo.base_branch ?? '') : false
  const projectDirty = repo ? projectId !== (repo.project_id ?? '') : false

  async function saveBase() {
    if (!repo || !baseDirty) return
    setSavingBase(true)
    setError(null)
    try {
      const updated = await updateRepo(repo.id, { base_branch: baseBranch.trim() })
      setRepo(updated)
      setBaseBranch(updated.base_branch ?? '')
    } catch (e) {
      setError(String(e))
    } finally {
      setSavingBase(false)
    }
  }

  async function saveProject() {
    if (!repo || !projectDirty) return
    setSavingProject(true)
    setError(null)
    try {
      // Empty string sent as "clear project" — but the cloud API's PATCH
      // body uses an optional pointer, so an empty string here is the
      // closest we get to "unset" from the browser. Backends that want
      // explicit clearing can interpret "" as null.
      const updated = await updateRepo(repo.id, { project_id: projectId })
      setRepo(updated)
      setProjectId(updated.project_id ?? '')
    } catch (e) {
      setError(String(e))
    } finally {
      setSavingProject(false)
    }
  }

  async function handleDelete() {
    if (!repo) return
    const confirmed = window.confirm(
      `Delete repo "${repo.name}" from the cloud?\n\nThis removes its bindings too. Local clones on worker machines are not touched.`,
    )
    if (!confirmed) return
    setDeleting(true)
    setError(null)
    try {
      await deleteRepo(repo.id)
      navigate({ to: '/repos' })
    } catch (e) {
      setError(String(e))
      setDeleting(false)
    }
  }

  return (
    <div className="px-6 py-6 max-w-3xl mx-auto">
      <Link
        to="/repos"
        className="inline-flex items-center gap-1 text-xs mb-4"
        style={{ color: 'hsl(var(--text-low))' }}
      >
        <ArrowLeft size={12} />
        Back to repos
      </Link>

      {loading ? (
        <p className="text-sm" style={{ color: 'hsl(var(--text-low))' }}>
          Loading…
        </p>
      ) : notFound ? (
        <div
          className="rounded-lg border px-6 py-12 text-center"
          style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
        >
          <p className="text-sm font-medium mb-1" style={{ color: 'hsl(var(--text-high))' }}>
            Repo not found
          </p>
          <p className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
            <span className="font-mono">{fullName}</span> is not registered in
            the cloud. Add it from the{' '}
            <Link to="/repos" className="underline">
              repos list
            </Link>
            .
          </p>
        </div>
      ) : repo ? (
        <>
          <div className="flex items-center justify-between mb-5">
            <div className="flex items-center gap-2 min-w-0">
              <VcsIcon repo={repo.name} map={repoMap} className="w-4 h-4 shrink-0" />
              <h1
                className="text-base font-mono font-semibold truncate"
                title={repo.name}
                style={{ color: 'hsl(var(--text-high))' }}
              >
                {repo.name}
              </h1>
            </div>
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
                onClick={handleDelete}
                disabled={deleting}
                className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
                style={{
                  borderColor: 'hsl(0 70% 50% / 0.4)',
                  color: 'hsl(0 70% 50%)',
                }}
              >
                {deleting ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
                Delete
              </button>
            </div>
          </div>

          {error && (
            <div
              className="mb-4 px-4 py-3 rounded-md text-sm border"
              style={{
                background: 'hsl(var(--bg-panel))',
                borderColor: 'hsl(var(--border))',
                color: 'hsl(var(--text-high))',
              }}
            >
              {error}
            </div>
          )}

          {/* Summary card */}
          <div
            className="rounded-lg border overflow-hidden mb-6"
            style={{ borderColor: 'hsl(var(--border))' }}
          >
            <table className="w-full text-sm">
              <tbody>
                <SummaryRow label="Platform" value={repo.platform || 'github'} />
                <SummaryRow
                  label="Project"
                  value={projectName(repo.project_id)}
                />
                <SummaryRow
                  label="Base branch"
                  value={repo.base_branch || '—'}
                  mono
                />
                <SummaryRow label="Created" value={timeAgo(repo.created_at)} />
                <SummaryRow label="Updated" value={timeAgo(repo.updated_at)} />
              </tbody>
            </table>
          </div>

          {/* Edit fields */}
          <h2 className="text-xs font-semibold uppercase tracking-wide mb-2" style={{ color: 'hsl(var(--text-low))' }}>
            Edit
          </h2>
          <div
            className="rounded-lg border p-4 mb-6 space-y-4"
            style={{ borderColor: 'hsl(var(--border))' }}
          >
            {/* Base branch */}
            <div className="flex items-end gap-2">
              <div className="flex-1">
                <label
                  htmlFor="edit-base"
                  className="block text-xs font-medium mb-1.5"
                  style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}
                >
                  Base branch
                </label>
                <input
                  id="edit-base"
                  type="text"
                  value={baseBranch}
                  onChange={e => setBaseBranch(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  className="w-full text-sm font-mono px-3 py-2 rounded border"
                  style={{
                    background: 'hsl(var(--bg-primary))',
                    borderColor: 'hsl(var(--border))',
                    color: 'hsl(var(--text-high))',
                  }}
                />
              </div>
              <button
                onClick={saveBase}
                disabled={!baseDirty || savingBase}
                className="inline-flex items-center gap-1.5 text-xs px-3 py-2 rounded-sm border transition-colors disabled:opacity-50"
                style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
              >
                {savingBase ? <Loader2 size={12} className="animate-spin" /> : <Save size={12} />}
                Save
              </button>
            </div>

            {/* Project */}
            <div className="flex items-end gap-2">
              <div className="flex-1">
                <label
                  htmlFor="edit-project"
                  className="block text-xs font-medium mb-1.5"
                  style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}
                >
                  Project
                </label>
                <select
                  id="edit-project"
                  value={projectId}
                  onChange={e => setProjectId(e.target.value)}
                  className="w-full text-sm px-3 py-2 rounded border"
                  style={{
                    background: 'hsl(var(--bg-primary))',
                    borderColor: 'hsl(var(--border))',
                    color: 'hsl(var(--text-high))',
                  }}
                >
                  <option value="">— No project —</option>
                  {projects.map(p => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </div>
              <button
                onClick={saveProject}
                disabled={!projectDirty || savingProject}
                className="inline-flex items-center gap-1.5 text-xs px-3 py-2 rounded-sm border transition-colors disabled:opacity-50"
                style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
              >
                {savingProject ? <Loader2 size={12} className="animate-spin" /> : <Save size={12} />}
                Save
              </button>
            </div>
          </div>

          {/* Bindings */}
          <h2 className="text-xs font-semibold uppercase tracking-wide mb-2" style={{ color: 'hsl(var(--text-low))' }}>
            Bindings ({repoBindings.length})
          </h2>
          {repoBindings.length === 0 ? (
            <div
              className="rounded-lg border px-4 py-6 text-center text-xs"
              style={{
                borderColor: 'hsl(var(--border))',
                background: 'hsl(var(--bg-panel))',
                color: 'hsl(var(--text-low))',
              }}
            >
              No machines bound to this repo yet. Bind one from the{' '}
              <Link to="/cloud/bindings" className="underline">
                Bindings page
              </Link>
              .
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
                    style={{
                      background: 'hsl(var(--bg-panel))',
                      borderColor: 'hsl(var(--border))',
                      color: 'hsl(var(--text-low))',
                    }}
                  >
                    <th className="text-left px-4 py-2">Machine</th>
                    <th className="text-left px-4 py-2">Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {repoBindings.map(({ binding, machineName }, i) => (
                    <tr
                      key={binding.id}
                      className="border-b last:border-b-0"
                      style={{
                        borderColor: 'hsl(var(--border))',
                        background: i % 2 === 0 ? 'transparent' : 'hsl(var(--bg-panel) / 0.4)',
                      }}
                    >
                      <td
                        className="px-4 py-2.5 font-mono text-xs"
                        style={{ color: 'hsl(var(--text-high))' }}
                      >
                        {machineName}
                      </td>
                      <td className="px-4 py-2.5 text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                        {timeAgo(binding.updated_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      ) : null}
    </div>
  )
}

function SummaryRow({
  label,
  value,
  mono,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <tr className="border-b last:border-b-0" style={{ borderColor: 'hsl(var(--border))' }}>
      <td
        className="px-4 py-2 text-xs font-medium w-40"
        style={{ background: 'hsl(var(--bg-panel) / 0.4)', color: 'hsl(var(--text-low))' }}
      >
        {label}
      </td>
      <td
        className={`px-4 py-2 text-xs ${mono ? 'font-mono' : ''}`}
        style={{ color: 'hsl(var(--text-high))' }}
      >
        {value}
      </td>
    </tr>
  )
}
