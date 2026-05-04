import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { FolderKanban, ChevronRight, Plus, Loader2, X } from 'lucide-react'
import { cn } from '../lib/utils'

interface Project {
  name: string
  repos: string[]
  created_at?: string
  context_md?: string
}

export const Route = createFileRoute('/_app/projects/')({
  component: ProjectList,
})

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

function ProjectList() {
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [createName, setCreateName] = useState('')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  function fetchProjects() {
    fetch('/data/projects.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then(data => {
        setProjects(Array.isArray(data) ? data : [])
        setLoading(false)
      })
  }

  useEffect(() => {
    fetchProjects()
  }, [])

  async function handleCreate() {
    const name = createName.trim()
    if (!name) return
    setCreating(true)
    setCreateError(null)
    try {
      const r = await fetch('/api/project/create', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      })
      const d = await r.json()
      if (!r.ok || d.error) {
        throw new Error(d.error || `HTTP ${r.status}`)
      }
      setShowCreate(false)
      setCreateName('')
      fetchProjects()
    } catch (e) {
      setCreateError(e instanceof Error ? e.message : 'Unknown error')
    } finally {
      setCreating(false)
    }
  }

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <div className="flex items-center justify-between mb-5">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Projects</h1>
          <p className="text-xs text-muted-foreground mt-1">
            Cross-repo project groups with shared context
          </p>
        </div>
        <button
          type="button"
          onClick={() => { setShowCreate(true); setCreateError(null) }}
          className={cn(
            'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors',
            'bg-primary text-primary-foreground hover:bg-primary/90',
          )}
        >
          <Plus className="w-3.5 h-3.5" />
          New project
        </button>
      </div>

      {/* Create dialog */}
      {showCreate && (
        <div className="bg-card border border-border rounded-xl p-4 mb-5">
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-sm font-semibold text-foreground">Create project</h2>
            <button
              type="button"
              onClick={() => { setShowCreate(false); setCreateName(''); setCreateError(null) }}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
          <div className="flex gap-2">
            <input
              type="text"
              placeholder="Project name"
              value={createName}
              onChange={e => setCreateName(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleCreate() }}
              className="flex-1 px-3 py-1.5 bg-background border border-border rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-primary font-mono"
              autoFocus
            />
            <button
              type="button"
              onClick={handleCreate}
              disabled={creating || !createName.trim()}
              className={cn(
                'px-4 py-1.5 rounded-lg text-sm font-medium transition-colors',
                creating || !createName.trim()
                  ? 'bg-secondary text-muted-foreground cursor-not-allowed'
                  : 'bg-primary text-primary-foreground hover:bg-primary/90',
              )}
            >
              {creating ? <Loader2 className="w-4 h-4 animate-spin" /> : 'Create'}
            </button>
          </div>
          {createError && (
            <p className="text-xs text-red-600 mt-2">{createError}</p>
          )}
        </div>
      )}

      {loading ? (
        <p className="text-sm text-muted-foreground text-center py-8">Loading…</p>
      ) : projects.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <FolderKanban className="w-8 h-8 text-muted-foreground/40 mx-auto mb-3" />
          <p className="text-sm text-muted-foreground">
            No projects yet. Click <strong>New project</strong> to get started.
          </p>
        </div>
      ) : (
        <div className="bg-card border border-border rounded-xl overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-secondary/30 text-xs uppercase text-muted-foreground">
              <tr>
                <th className="text-left px-4 py-2 font-semibold">Project</th>
                <th className="text-left px-4 py-2 font-semibold">Repos</th>
                <th className="text-left px-4 py-2 font-semibold">Created</th>
                <th className="w-8" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {projects.map(p => (
                <tr key={p.name} className="hover:bg-secondary/20">
                  <td className="px-4 py-2">
                    <Link
                      to="/projects/$name"
                      params={{ name: p.name }}
                      className="inline-flex items-center gap-2 font-mono text-foreground hover:underline"
                    >
                      <FolderKanban className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
                      {p.name}
                    </Link>
                  </td>
                  <td className="px-4 py-2 text-muted-foreground tabular-nums">
                    {p.repos?.length ?? 0} {(p.repos?.length ?? 0) === 1 ? 'repo' : 'repos'}
                  </td>
                  <td className="px-4 py-2 text-muted-foreground text-xs tabular-nums">
                    {p.created_at ? timeAgo(p.created_at) : '—'}
                  </td>
                  <td className="px-4 py-2">
                    <Link
                      to="/projects/$name"
                      params={{ name: p.name }}
                      className="text-muted-foreground hover:text-foreground"
                    >
                      <ChevronRight className="w-4 h-4" />
                    </Link>
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
