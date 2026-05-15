import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { Link2, RefreshCw, Check, X } from 'lucide-react'
import {
  fetchBindings,
  fetchMachines,
  updateBinding,
  timeAgo,
  type Binding,
  type Machine,
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
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [edit, setEdit] = useState<EditState | null>(null)
  const selectRef = useRef<HTMLSelectElement>(null)

  const load = () => {
    setLoading(true)
    setError(null)
    Promise.all([fetchBindings(), fetchMachines()])
      .then(([b, m]) => {
        setBindings(b.bindings ?? [])
        setMachines(m.machines ?? [])
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

  return (
    <div className="px-6 py-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-base font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
          Bindings
        </h1>
        <button
          onClick={load}
          disabled={loading}
          className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
          style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
        >
          <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
          Refresh
        </button>
      </div>

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
                      {b.repo_id || b.project_id || '—'}
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
                        <button
                          onClick={() => startEdit(b)}
                          className="text-xs px-2 py-1 rounded border transition-colors"
                          style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
                        >
                          Rebind
                        </button>
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
      <p className="text-xs mb-4" style={{ color: 'hsl(var(--text-low))' }}>
        Bind a repo or project to a machine to route jobs to it.
      </p>
      <div
        className="inline-block text-left rounded-md px-3 py-2 font-mono text-xs border"
        style={{ background: 'hsl(var(--bg-primary))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
      >
        clawflow cloud bind --repo owner/repo --machine &lt;id&gt;
      </div>
    </div>
  )
}
