import { useState } from 'react'
import { Sparkles, Loader2, X, Info } from 'lucide-react'
import { cn } from '../lib/utils'

// useDocUpdater is the "Update with AI" affordance shared by the three
// project-level docs (context.md / testing.md / deployment.md) on the
// project detail page. It returns two ReactNodes so the caller can
// render them in different DOM positions:
//
//   - `trigger`: a small inline button meant to sit on the right of the
//     card header row (next to the title/size text).
//   - `form`:    the expanded form (textarea + submit/cancel),
//     conditionally rendered below the header when the user clicks the
//     trigger. Null when closed.
//
// State (open/closed, instructions, submitting, error) lives inside the
// hook. On a successful update the hook calls onUpdated() so the parent
// can refetch the project view.
//
// The backend (/api/project/update-doc) is synchronous — it blocks
// until claude returns or the 5-minute timeout fires. The UI mirrors
// that with a simple "Updating…" spinner; no SSE / streaming. Easier
// to reason about for a one-shot rewrite task.

export type DocFile = 'context.md' | 'testing.md' | 'deployment.md'

export interface UseDocUpdaterProps {
  project: string
  file: DocFile
  onUpdated: () => void
}

export interface DocUpdaterSlots {
  trigger: JSX.Element
  form: JSX.Element | null
}

export function useDocUpdater({ project, file, onUpdated }: UseDocUpdaterProps): DocUpdaterSlots {
  const [open, setOpen] = useState(false)
  const [instructions, setInstructions] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // noChangeReason is set when claude explicitly responds with the
  // NO-CHANGE marker (audit path) — the doc wasn't written and we
  // surface the model's one-line explanation instead of treating it
  // as an error. Cleared on next open/submit.
  const [noChangeReason, setNoChangeReason] = useState<string | null>(null)

  function close() {
    setOpen(false)
    setInstructions('')
    setError(null)
    setNoChangeReason(null)
  }

  async function submit() {
    const text = instructions.trim()
    if (!text || submitting) return
    setSubmitting(true)
    setError(null)
    setNoChangeReason(null)
    try {
      const r = await fetch('/api/project/update-doc', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project, file, instructions: text }),
      })
      const d = (await r.json().catch(() => ({}))) as {
        ok?: boolean
        action?: 'updated' | 'no_change'
        no_change_reason?: string
        error?: string
      }
      if (!r.ok || d.error) throw new Error(d.error || `HTTP ${r.status}`)
      if (d.action === 'no_change') {
        // Audit path: doc unchanged, show claude's reason inline. Form
        // stays open so the user can refine instructions and try again
        // ("no, definitely update X anyway").
        setNoChangeReason(d.no_change_reason || 'Claude reviewed and found no changes warranted.')
        setInstructions('')
      } else {
        // Rewrite path: doc was overwritten on the server. Close the
        // form and trigger a refetch so the new content shows up in
        // the markdown panel above.
        close()
        onUpdated()
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'update failed')
    } finally {
      setSubmitting(false)
    }
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault()
      void submit()
    }
  }

  // Trigger lives inside the card header's outer <button>, so it must
  // be a <span role="button"> (nested <button> is invalid HTML) and must
  // stopPropagation so clicking it doesn't also toggle the header's
  // open/closed state.
  const trigger = (
    <span
      role="button"
      tabIndex={0}
      onClick={e => {
        e.stopPropagation()
        setOpen(o => !o)
      }}
      onKeyDown={e => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          e.stopPropagation()
          setOpen(o => !o)
        }
      }}
      className={cn(
        'ml-auto text-xs inline-flex items-center gap-1 shrink-0 px-2 py-0.5 rounded cursor-pointer transition-colors',
        open
          ? 'text-foreground bg-secondary/60'
          : 'text-muted-foreground hover:text-foreground hover:bg-secondary/50',
      )}
    >
      <Sparkles className="w-3 h-3" />
      Update with AI
    </span>
  )

  const form = open ? (
    <div className="px-4 py-3 border-t border-border bg-secondary/10 space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-foreground">
          Update <code className="font-mono">{file}</code>
        </span>
        <button
          type="button"
          onClick={close}
          disabled={submitting}
          aria-label="Close"
          className="text-muted-foreground hover:text-foreground disabled:opacity-40 transition-colors"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      </div>
      <textarea
        value={instructions}
        onChange={e => setInstructions(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={`What should change in ${file}? e.g. "add a section about deployment to staging", "remove outdated Foo references", "看下要不要更新"`}
        rows={3}
        disabled={submitting}
        className="w-full resize-none text-xs font-sans px-3 py-2 rounded-lg bg-card border border-border focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
      />
      {noChangeReason && (
        <div className="text-xs text-blue-800 dark:text-blue-300 bg-blue-50 dark:bg-blue-950/30 border border-blue-200 dark:border-blue-900 rounded-lg px-3 py-2 flex items-start gap-2">
          <Info className="w-3.5 h-3.5 shrink-0 mt-px" />
          <div className="flex-1 min-w-0">
            <div className="font-medium mb-0.5">No change needed</div>
            <div className="text-blue-700 dark:text-blue-400 break-words">{noChangeReason}</div>
          </div>
        </div>
      )}
      {error && <p className="text-xs text-red-600 dark:text-red-400 break-all">{error}</p>}
      <div className="flex items-center justify-between gap-2">
        <span className="text-[11px] text-muted-foreground">
          {submitting
            ? 'Claude is rewriting the doc — this can take 10-60s.'
            : 'Cmd+⏎ to submit. The file is overwritten on save.'}
        </span>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={close}
            disabled={submitting}
            className="px-3 py-1 rounded-md text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-secondary/40 disabled:opacity-40 transition-colors"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={!instructions.trim() || submitting}
            className={cn(
              'px-3 py-1 rounded-md text-xs font-semibold inline-flex items-center gap-1 transition-all',
              !instructions.trim() || submitting
                ? 'bg-secondary text-muted-foreground/60 cursor-not-allowed'
                : 'bg-primary text-primary-foreground hover:opacity-90',
            )}
          >
            {submitting ? <Loader2 className="w-3 h-3 animate-spin" /> : <Sparkles className="w-3 h-3" />}
            {submitting ? 'Updating…' : 'Submit'}
          </button>
        </div>
      </div>
    </div>
  ) : null

  return { trigger, form }
}
