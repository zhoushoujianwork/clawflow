export type WorkFilter = 'running' | 'waiting' | 'attention' | 'delivered'

export interface WorkItem {
  stage: string
  state?: string
  attention?: string[]
  run?: { live?: boolean }
  acceptance?: { running?: boolean }
  delivery?: { merged_at?: string }
  executions?: {
    status: string
    ended_at?: string
    provenance: { source: string; stale: boolean }
  }[]
}

export interface SourceHealth {
  source: string
  repo?: string
  last_success: string | null
  stale: boolean
  refreshing: boolean
  error_category?: string
}

const active = (item: WorkItem) => item.stage !== 'merged' && item.state !== 'closed'
const live = (item: WorkItem, status: string) => item.executions?.some(e =>
  e.status === status && e.provenance.source === 'offload_live' && !e.provenance.stale) || false
const attention = (item: WorkItem) => !!item.attention?.length || live(item, 'stalled')
export const unknownExecution = (item: WorkItem) => active(item) && !live(item, 'running') && !live(item, 'stalled') && (
  !!item.run?.live || !!item.acceptance?.running || ['implementing', 'acceptance_running'].includes(item.stage) || !!item.executions?.some(e => !e.ended_at && (e.status === 'unknown' || e.provenance.stale)))

// Both the summary and repository rows use this selector. Counts are items, not attempts.
export function selectWorkItems<T extends WorkItem>(items: T[], filter: WorkFilter | 'active' | 'all', now = Date.now()): T[] {
  return items.filter(item => {
    switch (filter) {
      case 'active': return active(item)
      case 'running': return active(item) && live(item, 'running')
      case 'waiting': return active(item) && !live(item, 'running') && !attention(item) && !unknownExecution(item)
      case 'attention': return attention(item)
      case 'delivered': {
        const merged = Date.parse(item.delivery?.merged_at || '')
        return item.stage === 'merged' && merged <= now && now - merged <= 7 * 86400000
      }
      default: return true
    }
  })
}

export function WorkSummary({ repos, sources, generatedAt, stale, refreshing, failed, filter, onFilter }: {
  repos?: { items: WorkItem[] }[]
  sources?: SourceHealth[]
  generatedAt?: string
  stale?: boolean
  refreshing?: boolean
  failed?: boolean
  filter?: WorkFilter
  onFilter: (filter: WorkFilter) => void
}) {
  const items = repos?.flatMap(repo => repo.items) || []
  const ready = !!repos && !!generatedAt
  const unknown = items.filter(unknownExecution).length
  const freshness = [...new Set(sources?.map(s => s.source))].map(source => {
    const rows = sources!.filter(s => s.source === source)
    const unknown = rows.filter(s => !s.last_success).length
    const stale = rows.filter(s => s.last_success && (s.stale || s.error_category)).length
    return `${source}: ${rows.length - unknown - stale} fresh, ${stale} stale, ${unknown} unknown sources`
  }).join(' · ')
  const partial = failed || stale || sources?.some(s => s.stale || !s.last_success || s.error_category)
  return <section aria-label="Work summary" aria-busy={!ready || !!refreshing} className="mb-4 border rounded-xl p-4 min-h-[220px]">
    <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
      {(['running', 'waiting', 'attention', 'delivered'] as const).map(key => <button key={key}
        type="button" disabled={!ready} aria-pressed={filter === key} onClick={() => onFilter(key)}
        className={`rounded-lg border p-3 text-left hover:bg-secondary ${filter === key ? 'bg-secondary border-brand' : ''}`}>
        <span className="block text-sm capitalize">{key}</span>
        <span className="block text-2xl font-semibold tabular-nums">{ready ? selectWorkItems(items, key).length : '—'}</span>
      </button>)}
    </div>
    <p className="text-xs text-muted-foreground mt-3">All tracked repositories · items, not attempts. Running: observed Offload activity. Waiting: active items without running, attention or unknown execution. Attention counts items, not incidents, and may overlap running. Delivered: merges in the last 7 days. Select a count to show every matching row.</p>
    <p role="status" className="text-xs text-muted-foreground mt-2 h-24 sm:h-16 overflow-y-auto">
      {!ready ? failed ? 'Work unavailable. Retry Refresh to load a snapshot.' : 'Loading work… Counts will appear with the first snapshot.' : <>
        {sources?.length ? freshness : 'Source freshness unknown'}
        {partial ? ' · Partial or stale snapshot; counts reflect available records.' : ''}
        {refreshing ? ' · Refreshing; keeping the current snapshot.' : ''}
        {unknown ? ` · ${unknown} active items have unknown execution activity; excluded from waiting.` : ''}
        {!items.length ? ' · No work in the available snapshot.' : ''}
      </>}
    </p>
  </section>
}
