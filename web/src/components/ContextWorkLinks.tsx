import { Link } from '@tanstack/react-router'
import { CLEAR_WORK_SEARCH } from '../lib/workSearch'

type Props = ({ repo: string; project?: never } | { project: string; repo?: never }) & { compact?: boolean }

// Usage reads its initial filters on mount, so use a document navigation for it.
export function ContextWorkLinks({ repo, project, compact }: Props) {
  const usage = new URLSearchParams(repo ? { repo } : { project: project! })
  return <nav aria-label="工作与用量" className={compact ? 'flex flex-wrap gap-3' : 'border rounded-xl p-4 mb-4 flex flex-wrap gap-4 text-sm'}>
    <Link to="/pipeline" search={{ ...CLEAR_WORK_SEARCH, workRepo: repo, project }} className="underline">Work</Link>
    <a href={`/usage?${usage}`} className="underline">Usage</a>
  </nav>
}
