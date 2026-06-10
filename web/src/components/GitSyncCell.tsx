import { useCallback, useEffect, useRef, useState } from 'react'
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
  Star,
} from 'lucide-react'
import { cn } from '../lib/utils'
import { emitConfigChanged } from '../lib/configEvents'

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

interface BranchEntry {
  name: string
  remote: boolean
  is_base: boolean
  is_current: boolean
}

interface BranchesResponse {
  branches: BranchEntry[]
  base: string
  current: string
  fetch_error?: string
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
  const [error, setError] = useState<string>('')
  const [errorKind, setErrorKind] = useState<'pull' | 'push' | 'fetch' | ''>('')
  const [modalOpen, setModalOpen] = useState(false)

  // Branch switcher popover state
  const [branchOpen, setBranchOpen] = useState(false)
  const [branches, setBranches] = useState<BranchEntry[]>([])
  const [branchLoading, setBranchLoading] = useState(false)
  const [branchSwitching, setBranchSwitching] = useState(false)
  const [switchToast, setSwitchToast] = useState<string>('')
  const branchRef = useRef<HTMLDivElement>(null)

  const fetchBranches = useCallback(async () => {
    setBranchLoading(true)
    try {
      const r = await fetch(`/api/repo/branches?repo=${encodeURIComponent(repo)}`)
      if (r.ok) {
        const d: BranchesResponse = await r.json()
        setBranches(d.branches || [])
      }
    } catch {
      // silently degrade
    } finally {
      setBranchLoading(false)
    }
  }, [repo])

  useEffect(() => {
    if (branchOpen) void fetchBranches()
  }, [branchOpen, fetchBranches])

  // Close branch popover on outside click / Escape
  useEffect(() => {
    if (!branchOpen) return
    function handleClick(e: MouseEvent) {
      if (branchRef.current && !branchRef.current.contains(e.target as Node)) {
        setBranchOpen(false)
      }
    }
    function handleEsc(e: KeyboardEvent) {
      if (e.key === 'Escape') setBranchOpen(false)
    }
    document.addEventListener('mousedown', handleClick)
    document.addEventListener('keydown', handleEsc)
    return () => {
      document.removeEventListener('mousedown', handleClick)
      document.removeEventListener('keydown', handleEsc)
    }
  }, [branchOpen])

  async function switchBranch(newBranch: string) {
    if (branchSwitching) return
    setBranchSwitching(true)
    try {
      const r = await fetch('/api/repo/set-base-branch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repo, branch: newBranch }),
      })
      const d = await r.json()
      if (d.git_status) onUpdate?.(d.git_status)
      const toast = d.head_action === 'switched'
        ? `已切换 base 并 checkout 到 ${newBranch}`
        : `已更新 base 为 ${newBranch}，HEAD 保持不变。已开 PR 不会自动 retarget`
      setSwitchToast(toast)
      setTimeout(() => setSwitchToast(''), 4000)
      emitConfigChanged()
      setBranchOpen(false)
    } catch {
      setSwitchToast('切换失败，请重试')
      setTimeout(() => setSwitchToast(''), 3000)
    } finally {
      setBranchSwitching(false)
    }
  }

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
  const activeError = error || status.error || ''
  const activeErrorKind = error ? errorKind : status.error ? 'fetch' : ''
  // HEAD is off-base: pull/push would fail, so disable them proactively.
  const headOffBase = !!(status.current && status.current !== status.branch)
  const headOffBaseTitle = headOffBase
    ? `HEAD 当前在 ${status.current}，请先切回 base 再同步`
    : undefined

  // Separate local vs remote branches for the popover
  const localBranches = branches.filter(b => !b.remote)
  const remoteBranches = branches.filter(b => b.remote)

  return (
    <>
      <div className="flex items-center gap-1.5">
        {/* branch chip — clickable button that opens popover */}
        <div ref={branchRef} className="relative">
          <button
            type="button"
            onClick={() => setBranchOpen(v => !v)}
            aria-expanded={branchOpen}
            aria-haspopup="listbox"
            className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono text-muted-foreground bg-muted border border-border hover:bg-accent hover:text-foreground transition-colors"
            title={`base: ${status.branch}${headOffBase ? ` (HEAD: ${status.current})` : ''} — 点击切换 base 分支`}
          >
            <GitBranch className="w-3 h-3" />
            base: {status.branch}
          </button>

          {/* branch switcher popover */}
          {branchOpen && (
            <div
              className="absolute left-0 top-full mt-1 w-56 rounded-lg border shadow-lg z-50 p-2 flex flex-col gap-1"
              style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))' }}
              role="listbox"
              aria-label="切换 base 分支"
            >
              {branchLoading ? (
                <span className="text-xs text-muted-foreground flex items-center gap-1 px-2 py-1">
                  <Loader2 className="w-3 h-3 animate-spin" /> 加载分支…
                </span>
              ) : branches.length === 0 ? (
                <span className="text-xs text-muted-foreground px-2 py-1">暂无分支</span>
              ) : (
                <>
                  {localBranches.length > 0 && (
                    <p className="text-[10px] uppercase tracking-wide text-muted-foreground/60 px-1 pt-0.5">本地</p>
                  )}
                  {localBranches.map(b => (
                    <BranchItem key={b.name} b={b} onSelect={switchBranch} switching={branchSwitching} />
                  ))}
                  {remoteBranches.length > 0 && (
                    <p className="text-[10px] uppercase tracking-wide text-muted-foreground/60 px-1 pt-1">远端</p>
                  )}
                  {remoteBranches.map(b => (
                    <BranchItem key={'r/' + b.name} b={b} onSelect={switchBranch} switching={branchSwitching} />
                  ))}
                </>
              )}
            </div>
          )}
        </div>

        {/* HEAD label when HEAD != base */}
        {headOffBase && (
          <span
            className="inline-flex items-center gap-0.5 px-1 py-0.5 rounded text-[10px] font-mono text-muted-foreground/70 bg-muted/50 border border-border/50"
            title={`当前 checkout 分支：${status.current}`}
          >
            HEAD: {status.current}
          </span>
        )}

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
                disabled={!!busy || dirty || headOffBase}
                title={headOffBase ? headOffBaseTitle : dirty ? '工作区有未提交改动，请先提交或暂存后再 pull' : `落后远端 ${behind} 个提交，点击 git pull`}
                className={cn(
                  'inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-[11px] font-semibold border transition-colors',
                  'bg-amber-50 text-amber-700 border-amber-200 hover:bg-amber-100',
                  (!!busy || dirty || headOffBase) && 'opacity-50 cursor-not-allowed',
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
                disabled={!!busy || headOffBase}
                title={headOffBase ? headOffBaseTitle : `领先远端 ${ahead} 个提交，点击 git push`}
                className={cn(
                  'inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-[11px] font-semibold border transition-colors',
                  'bg-blue-50 text-blue-700 border-blue-200 hover:bg-blue-100',
                  (!!busy || headOffBase) && 'opacity-50 cursor-not-allowed',
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

      {/* switch toast */}
      {switchToast && (
        <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 px-3 py-2 rounded-lg shadow-lg text-xs bg-foreground text-background max-w-sm text-center">
          {switchToast}
        </div>
      )}
    </>
  )
}

// BranchItem renders one row inside the branch switcher popover.
function BranchItem({
  b,
  onSelect,
  switching,
}: {
  b: BranchEntry
  onSelect: (name: string) => void
  switching: boolean
}) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={b.is_base}
      disabled={switching || b.is_base}
      onClick={() => onSelect(b.name)}
      className={cn(
        'flex items-center gap-1.5 w-full text-left px-2 py-1 rounded text-xs font-mono transition-colors',
        b.is_base
          ? 'bg-accent/50 text-foreground cursor-default'
          : 'hover:bg-accent text-muted-foreground hover:text-foreground',
        switching && 'opacity-50 cursor-wait',
      )}
      title={b.is_base ? '当前 base 分支' : `切换 base 到 ${b.name}`}
    >
      {b.is_base
        ? <Star className="w-3 h-3 shrink-0 text-amber-500" />
        : b.is_current
          ? <Check className="w-3 h-3 shrink-0 text-green-500" />
          : <span className="w-3 h-3 shrink-0" />
      }
      <span className="truncate">{b.name}</span>
      {b.remote && (
        <span className="ml-auto text-[9px] text-muted-foreground/60 shrink-0">remote</span>
      )}
    </button>
  )
}
