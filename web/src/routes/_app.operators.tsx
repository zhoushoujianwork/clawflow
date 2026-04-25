import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { cn } from '../lib/utils'

interface Operator {
  name: string
  description: string
  target: string
  labels_required: string[]
  labels_excluded: string[]
  lock_label: string
  source: string
}

export const Route = createFileRoute('/_app/operators')({
  component: OperatorsList,
})

function OperatorsList() {
  const [ops, setOps] = useState<Operator[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('/data/operators.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .then(d => setOps(Array.isArray(d) ? d : []))
      .catch(() => setOps([]))
      .finally(() => setLoading(false))
  }, [])

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
        <div className="grid gap-3">
          {ops.map(op => (
            <div key={op.name} className="bg-card border border-border rounded-xl p-4">
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
              <div className="mt-2 text-[10px] text-muted-foreground font-mono truncate">
                source: {op.source}
              </div>
            </div>
          ))}
        </div>
      )}
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
