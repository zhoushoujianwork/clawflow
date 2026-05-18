import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { ArrowLeft, Loader2 } from 'lucide-react'
import { createRepo, fetchProjects, type Project } from '../lib/cloudApi'

export const Route = createFileRoute('/_app/repos/add')({
  component: AddRepoPage,
})

type PlatformChoice = 'github' | 'gitlab'

// owner/repo or group/subgroup/repo — at least one slash, no leading/trailing
// slash, no empty segments. We deliberately keep this loose; the cloud API
// will reject anything truly malformed.
const NAME_RE = /^[^/\s]+(?:\/[^/\s]+)+$/

function AddRepoPage() {
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [platform, setPlatform] = useState<PlatformChoice>('github')
  const [projectId, setProjectId] = useState<string>('')
  const [baseBranch, setBaseBranch] = useState('main')
  const [projects, setProjects] = useState<Project[]>([])
  const [projectsLoading, setProjectsLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    fetchProjects()
      .then(r => setProjects(r.projects ?? []))
      .catch(e => setError(String(e)))
      .finally(() => setProjectsLoading(false))
  }, [])

  const nameValid = NAME_RE.test(name.trim())
  const canSubmit = nameValid && !submitting

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!canSubmit) return
    setSubmitting(true)
    setError(null)
    try {
      await createRepo({
        name: name.trim(),
        platform,
        project_id: projectId || undefined,
        base_branch: baseBranch.trim() || undefined,
      })
      navigate({ to: '/repos' })
    } catch (e) {
      setError(String(e))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="px-6 py-6 max-w-2xl mx-auto">
      <Link
        to="/repos"
        className="inline-flex items-center gap-1 text-xs mb-4"
        style={{ color: 'hsl(var(--text-low))' }}
      >
        <ArrowLeft size={12} />
        Back to repos
      </Link>

      <h1 className="text-base font-semibold mb-1" style={{ color: 'hsl(var(--text-high))' }}>
        Add repo
      </h1>
      <p className="text-xs mb-6" style={{ color: 'hsl(var(--text-low))' }}>
        Register a repository with the cloud. Worker machines bound to it will
        start receiving its issue and PR jobs.
      </p>

      {error && (
        <div
          className="mb-4 px-4 py-3 rounded-md text-sm border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
        >
          {error}
        </div>
      )}

      <form onSubmit={handleSubmit} className="space-y-5">
        <div>
          <label
            htmlFor="repo-name"
            className="block text-xs font-medium mb-1.5"
            style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}
          >
            Repository name
          </label>
          <input
            id="repo-name"
            type="text"
            value={name}
            onChange={e => setName(e.target.value)}
            placeholder="owner/repo"
            autoComplete="off"
            spellCheck={false}
            className="w-full text-sm font-mono px-3 py-2 rounded border"
            style={{
              background: 'hsl(var(--bg-primary))',
              borderColor:
                name && !nameValid ? 'hsl(0 70% 50%)' : 'hsl(var(--border))',
              color: 'hsl(var(--text-high))',
            }}
          />
          <p className="text-[11px] mt-1" style={{ color: 'hsl(var(--text-low))' }}>
            Format: <code className="font-mono">owner/repo</code> (GitLab subgroups
            allowed: <code className="font-mono">group/subgroup/repo</code>).
          </p>
        </div>

        <div>
          <span
            className="block text-xs font-medium mb-1.5"
            style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}
          >
            Platform
          </span>
          <div className="flex items-center gap-4">
            <label className="inline-flex items-center gap-2 cursor-pointer">
              <input
                type="radio"
                name="platform"
                value="github"
                checked={platform === 'github'}
                onChange={() => setPlatform('github')}
              />
              <span className="text-sm" style={{ color: 'hsl(var(--text-high))' }}>
                GitHub
              </span>
            </label>
            <label className="inline-flex items-center gap-2 cursor-pointer">
              <input
                type="radio"
                name="platform"
                value="gitlab"
                checked={platform === 'gitlab'}
                onChange={() => setPlatform('gitlab')}
              />
              <span className="text-sm" style={{ color: 'hsl(var(--text-high))' }}>
                GitLab
              </span>
            </label>
          </div>
        </div>

        <div>
          <label
            htmlFor="repo-project"
            className="block text-xs font-medium mb-1.5"
            style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}
          >
            Project (optional)
          </label>
          <select
            id="repo-project"
            value={projectId}
            onChange={e => setProjectId(e.target.value)}
            disabled={projectsLoading}
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

        <div>
          <label
            htmlFor="repo-base"
            className="block text-xs font-medium mb-1.5"
            style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}
          >
            Base branch
          </label>
          <input
            id="repo-base"
            type="text"
            value={baseBranch}
            onChange={e => setBaseBranch(e.target.value)}
            placeholder="main"
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

        <div className="flex items-center gap-2 pt-2">
          <button
            type="submit"
            disabled={!canSubmit}
            className="inline-flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
            style={{
              borderColor: 'hsl(var(--brand))',
              color: 'hsl(var(--brand))',
              background: 'hsl(var(--brand) / 0.08)',
            }}
          >
            {submitting && <Loader2 size={12} className="animate-spin" />}
            {submitting ? 'Adding…' : 'Add repo'}
          </button>
          <Link
            to="/repos"
            className="text-xs px-3 py-1.5 rounded-sm border transition-colors"
            style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
          >
            Cancel
          </Link>
        </div>
      </form>
    </div>
  )
}
