import { createFileRoute, Link } from '@tanstack/react-router'
import { useMemo } from 'react'
import { ChevronLeft, MessageSquare } from 'lucide-react'
import { XTerminal } from '../components/Terminal'

interface ChatSearch {
  repo?: string
  issue?: string
  model?: string
}

export const Route = createFileRoute('/_app/chat')({
  validateSearch: (search: Record<string, unknown>): ChatSearch => ({
    repo: (search.repo as string) || undefined,
    issue: (search.issue as string) || undefined,
    model: (search.model as string) || undefined,
  }),
  component: ChatPage,
})

function ChatPage() {
  const { repo, issue, model } = Route.useSearch()

  const wsUrl = useMemo(() => {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const params = new URLSearchParams()
    if (repo) params.set('repo', repo)
    if (issue) params.set('issue', issue)
    if (model) params.set('model', model)
    return `${proto}//${window.location.host}/ws/pty?${params.toString()}`
  }, [repo, issue, model])

  if (!repo) {
    return (
      <div className="max-w-3xl mx-auto px-4 py-6">
        <p className="text-sm text-muted-foreground">
          Missing <code>repo</code> parameter. Use{' '}
          <code className="px-1 py-0.5 bg-secondary rounded font-mono">/chat?repo=owner/repo</code>
          {' '}or start from a{' '}
          <Link to="/repos" className="underline hover:text-foreground">repo page</Link>.
        </p>
      </div>
    )
  }

  return (
    <div className="flex flex-col" style={{ height: 'calc(100vh - 48px)' }}>
      <div className="flex items-center gap-3 px-4 py-2 border-b" style={{ borderColor: 'hsl(var(--border))' }}>
        <Link to="/repos" className="text-muted-foreground hover:text-foreground">
          <ChevronLeft className="w-4 h-4" />
        </Link>
        <MessageSquare className="w-4 h-4" style={{ color: 'hsl(var(--brand))' }} />
        <span className="text-sm font-mono font-medium">{repo}</span>
        {issue && (
          <span className="text-xs font-mono text-muted-foreground">#{issue}</span>
        )}
        {model && model !== 'haiku' && (
          <span className="text-[11px] px-1.5 py-0.5 rounded border" style={{
            borderColor: 'hsl(var(--border))',
            color: 'hsl(var(--text-low))',
          }}>
            {model}
          </span>
        )}
      </div>
      <div className="flex-1 min-h-0" style={{ background: '#1a1a2e' }}>
        <XTerminal wsUrl={wsUrl} />
      </div>
    </div>
  )
}
