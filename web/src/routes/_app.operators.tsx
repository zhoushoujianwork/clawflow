import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { cn } from '../lib/utils'
import { fetchAuthMe } from '../lib/cloudApi'

interface Operator {
  name: string
  description: string
  target: string
  labels_required: string[]
  labels_excluded: string[]
  lock_label: string
  source: string
}

// Maps built-in operator names to their pipeline stage.
// TODO: when SKILL.md frontmatter gains a `stage:` field, use that as the
// primary source and fall back to this map for operators that don't declare one.
type Stage = 'triage' | 'evaluation' | 'execution' | 'interaction' | 'custom'

const STAGE_MAP: Record<string, Stage> = {
  classify: 'triage',
  'evaluate-bug': 'evaluation',
  'evaluate-feat': 'evaluation',
  decompose: 'execution',
  implement: 'execution',
  'track-progress': 'execution',
  'reply-comment': 'interaction',
  'reply-question': 'interaction',
}

// Explicit ordering within each stage so the natural flow is preserved
// (e.g. decompose → implement → track-progress for Execution).
const STAGE_ORDER: Record<string, number> = {
  classify: 0,
  'evaluate-bug': 0,
  'evaluate-feat': 1,
  decompose: 0,
  implement: 1,
  'track-progress': 2,
  'reply-comment': 0,
  'reply-question': 1,
}

const STAGES: { key: Stage; title: string; blurb: string }[] = [
  { key: 'triage',      title: '1. Triage',      blurb: 'Route incoming issues to the right evaluator.' },
  { key: 'evaluation',  title: '2. Evaluation',  blurb: 'Score bugs and features and decide if they are ready for an agent.' },
  { key: 'execution',   title: '3. Execution',   blurb: 'Break down, implement, and track the work.' },
  { key: 'interaction', title: '4. Interaction', blurb: 'Reply to human comments and questions on issues and PRs.' },
  { key: 'custom',      title: 'Custom / Other', blurb: 'Operators not mapped to a built-in pipeline stage.' },
]

export const Route = createFileRoute('/_app/operators')({
  component: OperatorsList,
})

function OperatorsList() {
  const [ops, setOps] = useState<Operator[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    // Cloud bundles serve /api/cloud/operators (the registry the cloud
    // server itself was constructed with). Local `clawflow web` writes
    // /data/operators.json. Probe auth to decide which endpoint to hit;
    // fall back to the local snapshot when fetchAuthMe says no-cloud.
    let cancelled = false
    fetchAuthMe()
      .then(async auth => {
        if (auth.kind === 'authed' || auth.kind === 'anon') {
          const r = await fetch('/api/cloud/operators', { credentials: 'include' })
          if (!r.ok) return [] as Operator[]
          const d = await r.json().catch(() => ({ operators: [] }))
          return Array.isArray(d.operators) ? (d.operators as Operator[]) : []
        }
        const r = await fetch('/data/operators.json', { cache: 'no-store' })
        if (!r.ok) return [] as Operator[]
        const d = await r.json().catch(() => [])
        return Array.isArray(d) ? (d as Operator[]) : []
      })
      .then(d => { if (!cancelled) setOps(d) })
      .catch(() => { if (!cancelled) setOps([]) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  // Group operators by stage, preserving explicit intra-stage order.
  const grouped = STAGES.map(stage => ({
    ...stage,
    items: ops
      .filter(op => (STAGE_MAP[op.name] ?? 'custom') === stage.key)
      .sort((a, b) => (STAGE_ORDER[a.name] ?? 99) - (STAGE_ORDER[b.name] ?? 99)),
  })).filter(group => group.items.length > 0)

  return (
    <div className="max-w-5xl mx-auto px-4 py-6">
      <div className="mb-5">
        <h1 className="text-2xl font-bold text-foreground">Registered operators</h1>
        <p className="text-xs text-muted-foreground mt-1">
          Built-in operators ship inside the binary via embed.FS. Drop a SKILL.md into{' '}
          <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">~/.clawflow/skills/&lt;name&gt;/</code> to override by name.
        </p>
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground text-center py-8">Loading…</p>
      ) : ops.length === 0 ? (
        <div className="bg-card border border-border rounded-xl p-8 text-center">
          <p className="text-sm text-muted-foreground">No operators registered.</p>
        </div>
      ) : (
        <div className="space-y-8">
          {grouped.map(group => (
            <section key={group.key}>
              <div className="mb-3">
                <h2 className="text-base font-semibold text-foreground">{group.title}</h2>
                <p className="text-xs text-muted-foreground mt-0.5">{group.blurb}</p>
              </div>
              <div className="grid gap-3">
                {group.items.map(op => (
                  <OperatorCard key={op.name} op={op} />
                ))}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  )
}

function OperatorCard({ op }: { op: Operator }) {
  return (
    <div className="bg-card border border-border rounded-xl p-4">
      <div className="flex items-start justify-between gap-3 mb-2">
        <div>
          <h3 className="text-sm font-semibold text-foreground font-mono">{op.name}</h3>
          <p className="text-xs text-muted-foreground mt-0.5">{op.description}</p>
        </div>
        <span className={cn(
          'shrink-0 inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-semibold border',
          op.target === 'issue' ? 'bg-blue-100 text-blue-700 border-blue-200' : 'bg-purple-100 text-purple-700 border-purple-200',
        )}>
          {op.target}
        </span>
      </div>
      <div className="flex flex-wrap gap-3 text-[11px] items-baseline">
        <LabelList title="requires" labels={op.labels_required} tone="green" />
        <LabelList title="excludes" labels={op.labels_excluded} tone="red" />
        <LabelList title="lock" labels={[op.lock_label]} tone="amber" />
      </div>
      <div className="mt-2 text-[10px] text-muted-foreground font-mono truncate" title={op.source}>
        source: {op.source}
      </div>
    </div>
  )
}

function LabelList({ title, labels, tone }: { title: string; labels: string[]; tone: 'green' | 'red' | 'amber' }) {
  if (labels.length === 0) return null
  const toneCls = {
    green: 'bg-green-50 text-green-700 border-green-200',
    red: 'bg-red-50 text-red-700 border-red-200',
    amber: 'bg-amber-50 text-amber-700 border-amber-200',
  }[tone]
  return (
    <div className="flex items-baseline gap-1 flex-wrap">
      <span className="text-muted-foreground uppercase">{title}:</span>
      {labels.map(l => (
        <code key={l} className={cn('px-1.5 py-0.5 rounded border font-mono', toneCls)}>
          {l}
        </code>
      ))}
    </div>
  )
}
