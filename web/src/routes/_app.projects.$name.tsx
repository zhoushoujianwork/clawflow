import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { ChevronLeft, FolderKanban, MessageSquare, Sparkles, X } from 'lucide-react'
import { cn } from '../lib/utils'
import { useChatDrawer } from '../lib/chatContext'
import { Markdown } from '../components/Markdown'

interface Project {
  name: string
  repos: string[]
  created_at?: string
  context_md?: string
}

export const Route = createFileRoute('/_app/projects/$name')({
  component: ProjectDetail,
})

function ProjectDetail() {
  const { name } = Route.useParams()
  const chatDrawer = useChatDrawer()

  const [project, setProject] = useState<Project | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('/data/projects.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .catch(() => [])
      .then(data => {
        const list: Project[] = Array.isArray(data) ? data : []
        const match = list.find(p => p.name === name) || null
        setProject(match)
        setLoading(false)
      })
  }, [name])

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
          Project <code className="font-mono text-foreground">{name}</code> not found. Run{' '}
          <code className="px-1 py-0.5 bg-secondary rounded font-mono">
            clawflow project create {name}
          </code>{' '}
          to create it.
        </div>
      ) : (
        <>
          {/* Header */}
          <div className="mb-6">
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
            </div>
          </div>

          {/* Action buttons */}
          <div className="flex gap-2 mb-6">
            <button
              type="button"
              onClick={() => chatDrawer.open({ project: project.name, action: 'chat' })}
              className={cn(
                'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border transition-colors',
                'border-border text-foreground hover:bg-secondary/50',
              )}
            >
              <MessageSquare className="w-3.5 h-3.5" />
              Chat
            </button>
            <button
              type="button"
              onClick={() => chatDrawer.open({ project: project.name, action: 'generate' })}
              className={cn(
                'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border transition-colors',
                'border-border text-foreground hover:bg-secondary/50',
              )}
            >
              <Sparkles className="w-3.5 h-3.5" />
              AI Generate
            </button>
          </div>

          {/* Context.md */}
          <section className="mb-6">
            <h2 className="text-sm font-semibold text-foreground mb-2">Context</h2>
            {project.context_md ? (
              <div className="bg-card border border-border rounded-xl p-4">
                <Markdown>{project.context_md}</Markdown>
              </div>
            ) : (
              <div className="bg-card border border-border rounded-xl p-6 text-center">
                <p className="text-sm text-muted-foreground">
                  No <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">context.md</code> yet.
                  Click <strong>AI Generate</strong> to create one, or add it manually.
                </p>
              </div>
            )}
          </section>

          {/* Member repos */}
          <section>
            <h2 className="text-sm font-semibold text-foreground mb-2">
              Member repos{' '}
              <span className="font-normal text-muted-foreground">
                ({project.repos?.length ?? 0})
              </span>
            </h2>
            {!project.repos || project.repos.length === 0 ? (
              <div className="bg-card border border-border rounded-xl p-6 text-center">
                <p className="text-sm text-muted-foreground">
                  No repos in this project. Use{' '}
                  <code className="px-1 py-0.5 bg-secondary rounded text-xs font-mono">
                    clawflow project add-repo {project.name} &lt;owner/repo&gt;
                  </code>{' '}
                  to add one.
                </p>
              </div>
            ) : (
              <div className="bg-card border border-border rounded-xl overflow-hidden divide-y divide-border">
                {project.repos.map(repo => (
                  <div
                    key={repo}
                    className="flex items-center justify-between px-4 py-2 hover:bg-secondary/20"
                  >
                    <Link
                      to="/repos/$repoName"
                      params={{ repoName: encodeURIComponent(repo) }}
                      className="font-mono text-sm text-foreground hover:underline"
                    >
                      {repo}
                    </Link>
                    <span className="text-muted-foreground">
                      <X className="w-3.5 h-3.5 opacity-0" aria-hidden="true" />
                    </span>
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
