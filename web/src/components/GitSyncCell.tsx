import { useState } from 'react'
import {
  ArrowUp,
  ArrowDown,
  RefreshCw,
  AlertCircle,
  Check,
  Loader2,
  GitBranch,
  Pencil,
  Unlink,
  CloudOff,
  X,
} from 'lucide-react'
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
 * back up via onUpdate so the parent's map stays in sync.
 *
 * Status is conveyed entirely through icons (no inline text) so the cell stays
 * compact across every surface. Git failures are surfaced as a single error
 * icon that opens a modal with the full output — the row layout never grows or
 * wraps. The user resolves errors locally (no auto-retry/conflict-fix).
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
  // Live error from the last pull/push/refresh, plus which op produced it,
  // so the modal can title itself. Falls back to status.error (cached fetch
  // failure from the background hook) when there is no live error.
  const [error, setError] = useState<string>('')
  const [errorKind, setErrorKind] = useState<'pull' | 'push' | 'fetch' | ''>('')
  const [modalOpen, setModalOpen] = useState(false)

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
        setErrorKind(kind)
        setModalOpen(true)
      }
    } catch {
      setError('网络错误，请重试')
      setErrorKind(kind)
      setModalOpen(true)
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
      setErrorKind('fetch')
      setModalOpen(true)
    } finally {
      setBusy(null)
    }
  }

  // Not configured locally — single muted icon, no text.
  if (!status || !status.has_clone) {
    return (
      <span className="inline-flex items-center text-muted-foreground/50" title="本地未 clone">
        <CloudOff className="w-3.5 h-3.5" />
      </span>
    )
  }

  const { ahead, behind, dirty, has_upstream } = status
  const synced = has_upstream && ahead === 0 && behind === 0
  const refreshSpin = busy === 'refresh'
  // The error icon surfaces either a live action error or a cached fetch error.
  const activeError = error || status.error || ''
  const activeErrorKind = error ? errorKind : status.error ? 'fetch' : ''

  return (
    <>
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
          <span className="inline-flex items-center text-muted-foreground/70" title="origin 上没有对应分支 (no upstream)">
            <Unlink className="w-3.5 h-3.5" />
          </span>
        ) : synced ? (
          <span className="inline-flex items-center text-green-600" title="已与远端同步 (up to date)">
            <Check className="w-3.5 h-3.5" />
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
          <span className="inline-flex items-center text-amber-600" title="工作区有未提交改动 (dirty)">
            <Pencil className="w-3 h-3" />
          </span>
        )}

        {/* error icon — opens the modal instead of growing the row */}
        {activeError && (
          <button
            type="button"
            onClick={() => setModalOpen(true)}
            title="git 操作出错，点击查看详情"
            className="inline-flex items-center text-destructive hover:opacity-80"
          >
            <AlertCircle className="w-3.5 h-3.5" />
          </button>
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

      {modalOpen && activeError && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
          onClick={() => setModalOpen(false)}
        >
          <div
            className="bg-card border border-border rounded-lg shadow-xl max-w-lg w-full p-4 flex flex-col gap-3"
            onClick={e => e.stopPropagation()}
            role="dialog"
            aria-label="Git error"
          >
            <div className="flex items-center gap-2">
              <AlertCircle className="w-4 h-4 text-destructive shrink-0" />
              <span className="text-sm font-semibold text-foreground">
                git {activeErrorKind || 'operation'} 失败
              </span>
              <span className="text-xs font-mono text-muted-foreground truncate" title={repo}>· {repo}</span>
              <button
                type="button"
                onClick={() => setModalOpen(false)}
                className="ml-auto text-muted-foreground hover:text-foreground"
                aria-label="Close"
              >
                <X className="w-4 h-4" />
              </button>
            </div>
            <pre className="text-xs text-destructive whitespace-pre-wrap break-words bg-muted/50 rounded p-2 max-h-72 overflow-auto font-mono">
              {activeError}
            </pre>
            <p className="text-[11px] text-muted-foreground">
              ClawFlow 不会自动重试或解决冲突，请回到本地仓库手动处理后再操作。
            </p>
          </div>
        </div>
      )}
    </>
  )
}
