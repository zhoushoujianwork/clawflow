import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { Link2, RefreshCw, Check, X, Plus, Trash2, Loader2 } from 'lucide-react'
import {
  createBinding,
  deleteBinding,
  fetchBindings,
  fetchMachines,
  fetchProjects,
  fetchRepos,
  updateBinding,
  timeAgo,
  type Binding,
  type Machine,
  type Project,
  type Repo,
} from '../lib/cloudApi'

export const Route = createFileRoute('/_app/cloud/bindings')({
  component: BindingsPage,
})

interface EditState {
  id: string
  machine_id: string
  saving: boolean
}

function BindingsPage() {
  const [bindings, setBindings] = useState<Binding[]>([])
  const [machines, setMachines] = useState<Machine[]>([])
  const [repos, setRepos] = useState<Repo[]>([])
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [edit, setEdit] = useState<EditState | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  // Create-form state. null = collapsed; non-null = expanded inline form.
  const [creating, setCreating] = useState<null | {
    target: 'repo' | 'project'
    target_id: string
    machine_id: string
    saving: boolean
  }>(null)
  const selectRef = useRef<HTMLSelectElement>(null)

  const load = () => {
    setLoading(true)
    setError(null)
    Promise.all([fetchBindings(), fetchMachines(), fetchRepos(), fetchProjects()])
      .then(([b, m, r, p]) => {
        setBindings(b.bindings ?? [])
        setMachines(m.machines ?? [])
        setRepos(r.repos ?? [])
        setProjects(p.projects ?? [])
      })
      .catch(e => setError(String(e)))
      .finally(() => setLoading(false))
  }

  useEffect(load, [])

  const machineLabel = (id: string) => {
    const m = machines.find(m => m.id === id)
    return m ? (m.display_name || m.hostname) : id
  }

  const startEdit = (b: Binding) => {
    setEdit({ id: b.id, machine_id: b.machine_id, saving: false })
    // Focus the select on next frame
    requestAnimationFrame(() => selectRef.current?.focus())
  }

  const cancelEdit = () => setEdit(null)

  const saveEdit = async () => {
    if (!edit) return
    setEdit(e => e ? { ...e, saving: true } : e)
    try {
      const updated = await updateBinding(edit.id, { machine_id: edit.machine_id })
      setBindings(bs => bs.map(b => (b.id === updated.id ? updated : b)))
      setEdit(null)
    } catch (e) {
      setError(String(e))
      setEdit(e => e ? { ...e, saving: false } : e)
    }
  }

  // Look up a repo/project by id for display in the table.
  const repoName = (id?: string) => {
    if (!id) return undefined
    return repos.find(r => r.id === id)?.name
  }
  const projectName = (id?: string) => {
    if (!id) return undefined
    return projects.find(p => p.id === id)?.name
  }

  // Open the create form with sensible defaults — first repo, first machine.
  const startCreate = () => {
    setCreating({
      target: 'repo',
      target_id: repos[0]?.id ?? '',
      machine_id: machines[0]?.id ?? '',
      saving: false,
    })
  }

  const saveCreate = async () => {
    if (!creating) return
    if (!creating.target_id || !creating.machine_id) {
      setError('Pick a target and a machine.')
      return
    }
    setCreating(c => c ? { ...c, saving: true } : c)
    try {
      const created = await createBinding(
        creating.target === 'repo'
          ? { machine_id: creating.machine_id, repo_id: creating.target_id }
          : { machine_id: creating.machine_id, project_id: creating.target_id },
      )
      setBindings(bs => [...bs, created])
      setCreating(null)
    } catch (e) {
      setError(String(e))
      setCreating(c => c ? { ...c, saving: false } : c)
    }
  }

  const handleDelete = async (b: Binding) => {
    const label = repoName(b.repo_id) ?? projectName(b.project_id) ?? b.id
    if (!window.confirm(`Delete binding for ${label}?`)) return
    setDeletingId(b.id)
    try {
      await deleteBinding(b.id)
      setBindings(bs => bs.filter(x => x.id !== b.id))
    } catch (e) {
      setError(String(e))
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <div className="px-6 py-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-base font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
          Bindings
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
            onClick={startCreate}
            disabled={creating !== null || machines.length === 0 || (repos.length === 0 && projects.length === 0)}
            className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
            style={{
              borderColor: 'hsl(var(--brand))',
              color: 'hsl(var(--brand))',
              background: 'hsl(var(--brand) / 0.08)',
            }}
          >
            <Plus size={12} />
            New binding
          </button>
        </div>
      </div>

      {creating && (
        <div
          className="mb-4 px-4 py-3 rounded-lg border"
          style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
        >
          <div className="flex items-center gap-3 flex-wrap">
            <span className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>Bind</span>
            <select
              value={creating.target}
              onChange={e => setCreating(c => c ? { ...c, target: e.target.value as 'repo' | 'project', target_id: '' } : c)}
              className="text-xs px-2 py-1 rounded border bg-transparent"
              style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
              disabled={creating.saving}
            >
              <option value="repo">Repo</option>
              <option value="project">Project</option>
            </select>
            <select
              value={creating.target_id}
              onChange={e => setCreating(c => c ? { ...c, target_id: e.target.value } : c)}
              className="text-xs px-2 py-1 rounded border bg-transparent flex-1 min-w-[200px]"
              style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
              disabled={creating.saving}
            >
              <option value="">Select {creating.target}…</option>
              {creating.target === 'repo'
                ? repos.map(r => <option key={r.id} value={r.id}>{r.name}</option>)
                : projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <span className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>→</span>
            <select
              value={creating.machine_id}
              onChange={e => setCreating(c => c ? { ...c, machine_id: e.target.value } : c)}
              className="text-xs px-2 py-1 rounded border bg-transparent"
              style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
              disabled={creating.saving}
            >
              <option value="">Select machine…</option>
              {machines.map(m => (
                <option key={m.id} value={m.id}>
                  {m.display_name || m.hostname}
                </option>
              ))}
            </select>
            <button
              onClick={saveCreate}
              disabled={creating.saving || !creating.target_id || !creating.machine_id}
              className="text-xs px-3 py-1 rounded-sm transition-colors disabled:opacity-50"
              style={{ background: 'hsl(var(--brand))', color: 'white' }}
            >
              {creating.saving ? <Loader2 size={12} className="animate-spin" /> : 'Create'}
            </button>
            <button
              onClick={() => setCreating(null)}
              disabled={creating.saving}
              className="text-xs px-2 py-1 rounded-sm transition-colors disabled:opacity-50"
              style={{ color: 'hsl(var(--text-low))' }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {error && (
        <div
          className="mb-4 px-4 py-3 rounded-md text-sm border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
        >
          {error}
        </div>
      )}

      {!loading && bindings.length === 0 && !error && (
        <EmptyBindings />
      )}

      {bindings.length > 0 && (
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
                <th className="text-left px-4 py-2">Repo / Project</th>
                <th className="text-left px-4 py-2">Machine</th>
                <th className="text-left px-4 py-2">Updated</th>
                <th className="text-left px-4 py-2">Actions</th>
              </tr>
            </thead>
            <tbody>
              {bindings.map((b, i) => {
                const isEditing = edit?.id === b.id
                return (
                  <tr
                    key={b.id}
                    className="border-b last:border-b-0"
                    style={{ borderColor: 'hsl(var(--border))', background: i % 2 === 0 ? 'transparent' : 'hsl(var(--bg-panel) / 0.4)' }}
                  >
                    <td className="px-4 py-2.5 font-mono text-xs" style={{ color: 'hsl(var(--text-high))' }}>
                      {repoName(b.repo_id) ?? projectName(b.project_id) ?? b.repo_id ?? b.project_id ?? '—'}
                      {repoName(b.repo_id) && (
                        <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded" style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}>repo</span>
                      )}
                      {projectName(b.project_id) && (
                        <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded" style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))' }}>project</span>
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      {isEditing ? (
                        <select
                          ref={selectRef}
                          value={edit.machine_id}
                          onChange={e => setEdit(ev => ev ? { ...ev, machine_id: e.target.value } : ev)}
                          disabled={edit.saving}
                          className="text-xs px-2 py-1 rounded border"
                          style={{
                            background: 'hsl(var(--bg-primary))',
                            borderColor: 'hsl(var(--brand))',
                            color: 'hsl(var(--text-high))',
                          }}
                        >
                          {machines.map(m => (
                            <option key={m.id} value={m.id}>
                              {m.display_name || m.hostname}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="text-sm" style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
                          {machineLabel(b.machine_id)}
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-2.5 text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {timeAgo(b.updated_at)}
                    </td>
                    <td className="px-4 py-2.5">
                      {isEditing ? (
                        <div className="flex items-center gap-1.5">
                          <button
                            onClick={saveEdit}
                            disabled={edit.saving}
                            className="p-1 rounded transition-colors disabled:opacity-50"
                            style={{ color: 'hsl(142 76% 36%)' }}
                            title="Save"
                          >
                            <Check size={14} />
                          </button>
                          <button
                            onClick={cancelEdit}
                            disabled={edit.saving}
                            className="p-1 rounded transition-colors disabled:opacity-50"
                            style={{ color: 'hsl(var(--text-low))' }}
                            title="Cancel"
                          >
                            <X size={14} />
                          </button>
                        </div>
                      ) : (
                        <div className="flex items-center gap-1.5">
                          <button
                            onClick={() => startEdit(b)}
                            className="text-xs px-2 py-1 rounded border transition-colors"
                            style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
                          >
                            Rebind
                          </button>
                          <button
                            onClick={() => handleDelete(b)}
                            disabled={deletingId === b.id}
                            className="p-1 rounded transition-colors disabled:opacity-50"
                            style={{ color: 'hsl(var(--text-low))' }}
                            title="Delete binding"
                          >
                            {deletingId === b.id ? (
                              <Loader2 size={14} className="animate-spin" />
                            ) : (
                              <Trash2 size={14} />
                            )}
                          </button>
                        </div>
                      )}
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

function EmptyBindings() {
  return (
    <div
      className="rounded-lg border px-6 py-12 text-center"
      style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
    >
      <Link2 size={32} className="mx-auto mb-3 opacity-30" style={{ color: 'hsl(var(--text-low))' }} />
      <p className="text-sm font-medium mb-1" style={{ color: 'hsl(var(--text-high))' }}>
        No bindings yet
      </p>
      <p className="text-xs" style={{ color: 'hsl(var(--text-low))' }}>
        Bind a repo or project to a machine so cloud routes its jobs there.
        Use the <strong>New binding</strong> button above.
      </p>
    </div>
  )
}
