import { useState } from 'react'
import { ArrowUp, ArrowDown, RefreshCw, AlertCircle, Check, Loader2, GitBranch } from 'lucide-react'
import { cn } from '../lib/utils'

// GitStatus mirrors gitsync.Status from the backend (data/git-status.json and
// the /api/repo/git-status* endpoints).
export interface GitStatus {
  repo: string
  branch: string
  ahead: number
  behind: number
  dirty: boolean
  has_upstream: boolean
  has_clone: boolean
  current?: string
  last_fetch?: string
  error?: string
}

interface GitActionResponse {
  status: string
  output: string
  error?: string
  git_status: GitStatus
}

/**
 * GitSyncCell renders one repo's main-branch sync state plus one-click
 * pull/push. It is surface-agnostic (repos table, repo detail, dashboard,
 * project page) — drop it anywhere a repo row is shown and feed it the cached
 * status. On pull/push it calls the backend, then reports the refreshed status
 * back up via onUpdate so the parent's map stays in sync. Git failures are
 * shown inline; the user resolves them locally (no auto-retry/conflict-fix).
 */
export function GitSyncCell({
  repo,
  status,
  onUpdate,
}: {
  repo: string
  status?: GitStatus
  onUpdate?: (s: GitStatus) => void
}) {
  const [busy, setBusy] = useState<'pull' | 'push' | 'refresh' | null>(null)
  const [error, setError] = useState<string>('')

  async function action(kind: 'pull' | 'push') {
    if (busy) return
    setBusy(kind)
    setError('')
    try {
      const resp = await fetch(`/api/repo/git-${kind}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repo }),
      })
      const data: GitActionResponse = await resp.json()
      if (data.git_status) onUpdate?.(data.git_status)
      if (data.status === 'error') {
        setError(data.error || data.output || `git ${kind} failed`)
      }
    } catch {
      setError('网络错误，请重试')
    } finally {
      setBusy(null)
    }
  }

  async function refresh() {
    if (busy) return
    setBusy('refresh')
    setError('')
    try {
      const resp = await fetch('/api/repo/git-status/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repo }),
      })
      const data: GitStatus[] = await resp.json()
      if (Array.isArray(data) && data[0]) onUpdate?.(data[0])
    } catch {
      setError('网络错误，请重试')
    } finally {
      setBusy(null)
    }
  }

  if (!status || !status.has_clone) {
    return <span className="text-xs text-muted-foreground/60">not cloned</span>
  }

  const { ahead, behind, dirty, has_upstream } = status
  const synced = has_upstream && ahead === 0 && behind === 0
  const refreshSpin = busy === 'refresh'

  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-1.5">
        {/* branch chip */}
        <span
          className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono text-muted-foreground bg-muted border border-border"
          title={`base branch: ${status.branch}${status.current && status.current !== status.branch ? ` (checked out: ${status.current})` : ''}`}
        >
          <GitBranch className="w-3 h-3" />
          {status.branch}
        </span>

        {!has_upstream ? (
          <span className="text-[10px] text-muted-foreground" title="origin 上没有对应分支">no upstream</span>
        ) : synced ? (
          <span className="inline-flex items-center gap-0.5 text-[11px] text-green-600" title="已与远端同步">
            <Check className="w-3 h-3" /> up to date
          </span>
        ) : (
          <>
            {behind > 0 && (
              <button
                type="button"
                onClick={() => action('pull')}
                disabled={!!busy || dirty}
                title={dirty ? '工作区有未提交改动，请先提交或暂存后再 pull' : `落后远端 ${behind} 个提交，点击 git pull`}
                className={cn(
                  'inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-[11px] font-semibold border transition-colors',
                  'bg-amber-50 text-amber-700 border-amber-200 hover:bg-amber-100',
                  (!!busy || dirty) && 'opacity-50 cursor-not-allowed',
                )}
              >
                {busy === 'pull' ? <Loader2 className="w-3 h-3 animate-spin" /> : <ArrowDown className="w-3 h-3" />}
                {behind}
              </button>
            )}
            {ahead > 0 && (
              <button
                type="button"
                onClick={() => action('push')}
                disabled={!!busy}
                title={`领先远端 ${ahead} 个提交，点击 git push`}
                className={cn(
                  'inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-[11px] font-semibold border transition-colors',
                  'bg-blue-50 text-blue-700 border-blue-200 hover:bg-blue-100',
                  !!busy && 'opacity-50 cursor-not-allowed',
                )}
              >
                {busy === 'push' ? <Loader2 className="w-3 h-3 animate-spin" /> : <ArrowUp className="w-3 h-3" />}
                {ahead}
              </button>
            )}
          </>
        )}

        {dirty && (
          <span className="text-[10px] text-amber-600" title="工作区有未提交改动">dirty</span>
        )}

        <button
          type="button"
          onClick={refresh}
          disabled={!!busy}
          title="重新检测同步状态（git fetch）"
          className="inline-flex items-center text-muted-foreground/60 hover:text-foreground disabled:opacity-50"
        >
          <RefreshCw className={cn('w-3 h-3', refreshSpin && 'animate-spin')} />
        </button>
      </div>

      {(error || status.error) && (
        <div
          className="flex items-start gap-1 text-[10px] text-destructive max-w-[280px]"
          title={error || status.error}
        >
          <AlertCircle className="w-3 h-3 shrink-0 mt-0.5" />
          <span className="break-words line-clamp-3">{error || status.error}</span>
        </div>
      )}
    </div>
  )
}
