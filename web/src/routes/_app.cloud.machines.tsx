import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { Monitor, RefreshCw } from 'lucide-react'
import { fetchMachines, isMachineOnline, timeAgo, type Machine } from '../lib/cloudApi'

export const Route = createFileRoute('/_app/cloud/machines')({
  component: MachinesPage,
})

function MachinesPage() {
  const [machines, setMachines] = useState<Machine[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = () => {
    setLoading(true)
    setError(null)
    fetchMachines()
      .then(r => setMachines(r.machines ?? []))
      .catch(e => setError(String(e)))
      .finally(() => setLoading(false))
  }

  useEffect(load, [])

  return (
    <div className="px-6 py-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-base font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
          Machines
        </h1>
        <button
          onClick={load}
          disabled={loading}
          className="flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-sm border transition-colors disabled:opacity-50"
          style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
        >
          <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
          Refresh
        </button>
      </div>

      {error && (
        <div
          className="mb-4 px-4 py-3 rounded-md text-sm border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-high))' }}
        >
          {error}
        </div>
      )}

      {!loading && machines.length === 0 && !error && (
        <EmptyMachines />
      )}

      {machines.length > 0 && (
        <div
          className="rounded-lg border overflow-hidden"
          style={{ borderColor: 'hsl(var(--border))' }}
        >
          <table className="w-full text-sm">
            <thead>
              <tr
                className="text-xs font-medium border-b"
                style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
              >
                <th className="text-left px-4 py-2">Status</th>
                <th className="text-left px-4 py-2">Hostname</th>
                <th className="text-left px-4 py-2">Display name</th>
                <th className="text-left px-4 py-2">Capabilities</th>
                <th className="text-left px-4 py-2">Last seen</th>
                <th className="text-left px-4 py-2">Version</th>
              </tr>
            </thead>
            <tbody>
              {machines.map((m, i) => {
                const online = isMachineOnline(m)
                return (
                  <tr
                    key={m.id}
                    className="border-b last:border-b-0"
                    style={{ borderColor: 'hsl(var(--border))', background: i % 2 === 0 ? 'transparent' : 'hsl(var(--bg-panel) / 0.4)' }}
                  >
                    <td className="px-4 py-2.5">
                      <span
                        className="inline-flex items-center gap-1.5 text-xs px-2 py-0.5 rounded-full font-medium"
                        style={{
                          background: online ? 'hsl(142 76% 36% / 0.15)' : 'hsl(var(--bg-panel))',
                          color: online ? 'hsl(142 76% 36%)' : 'hsl(var(--text-low))',
                          border: `1px solid ${online ? 'hsl(142 76% 36% / 0.3)' : 'hsl(var(--border))'}`,
                        }}
                      >
                        <span
                          className="w-1.5 h-1.5 rounded-full"
                          style={{ background: online ? 'hsl(142 76% 36%)' : 'hsl(var(--text-low))' }}
                        />
                        {online ? 'Online' : 'Offline'}
                      </span>
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs" style={{ color: 'hsl(var(--text-high))' }}>
                      {m.hostname}
                    </td>
                    <td className="px-4 py-2.5 text-sm" style={{ color: 'hsl(var(--text-mid, var(--text-low)))' }}>
                      {m.display_name || '—'}
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex flex-wrap gap-1">
                        {(m.capabilities ?? []).map(cap => (
                          <span
                            key={cap}
                            className="text-xs px-1.5 py-0.5 rounded"
                            style={{ background: 'hsl(var(--bg-panel))', color: 'hsl(var(--text-low))', border: '1px solid hsl(var(--border))' }}
                          >
                            {cap}
                          </span>
                        ))}
                        {(m.capabilities ?? []).length === 0 && (
                          <span style={{ color: 'hsl(var(--text-low))' }}>—</span>
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-2.5 text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {timeAgo(m.last_seen_at)}
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs" style={{ color: 'hsl(var(--text-low))' }}>
                      {m.version || '—'}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function EmptyMachines() {
  return (
    <div
      className="rounded-lg border px-6 py-12 text-center"
      style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-panel))' }}
    >
      <Monitor size={32} className="mx-auto mb-3 opacity-30" style={{ color: 'hsl(var(--text-low))' }} />
      <p className="text-sm font-medium mb-1" style={{ color: 'hsl(var(--text-high))' }}>
        No machines registered
      </p>
      <p className="text-xs mb-4" style={{ color: 'hsl(var(--text-low))' }}>
        Register a machine to start running operators in the cloud.
      </p>
      <div
        className="inline-block text-left rounded-md px-3 py-2 font-mono text-xs border"
        style={{ background: 'hsl(var(--bg-primary))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
      >
        clawflow worker register
      </div>
    </div>
  )
}
