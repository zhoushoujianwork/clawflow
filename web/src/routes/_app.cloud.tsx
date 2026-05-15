import { createFileRoute, Outlet, Link } from '@tanstack/react-router'
import { Cloud } from 'lucide-react'
import { useEffect, useState } from 'react'
import { fetchCloudStatus, type CloudStatus } from '../lib/cloudApi'

export const Route = createFileRoute('/_app/cloud')({
  component: CloudLayout,
})

function CloudLayout() {
  const [status, setStatus] = useState<CloudStatus | null>(null)

  useEffect(() => {
    fetchCloudStatus()
      .then(setStatus)
      .catch(() => setStatus({ configured: false, url: '' }))
  }, [])

  if (status && !status.configured) {
    return (
      <div className="max-w-2xl mx-auto px-6 py-20 text-center">
        <Cloud className="mx-auto mb-4 opacity-30" style={{ color: 'hsl(var(--text-low))' }} size={40} />
        <h1 className="text-xl font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
          Cloud not configured
        </h1>
        <p className="text-sm mb-6" style={{ color: 'hsl(var(--text-low))' }}>
          Connect to a ClawFlow cloud instance to manage machines, bindings, and jobs.
        </p>
        <div
          className="text-left rounded-lg px-4 py-3 font-mono text-xs border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-mid, var(--text-low)))' }}
        >
          <p className="mb-1" style={{ color: 'hsl(var(--text-low))' }}># Register this machine with a cloud instance</p>
          <p>clawflow worker register --url https://your-cloud.example.com</p>
        </div>
      </div>
    )
  }

  return (
    <div>
      {/* Cloud sub-nav */}
      <div
        className="border-b px-6 flex gap-1 h-10 items-center"
        style={{ background: 'hsl(var(--bg-primary))', borderColor: 'hsl(var(--border))' }}
      >
        <Link
          to="/cloud/machines"
          className="text-sm font-medium px-2.5 py-1 rounded-sm transition-colors"
          style={{ color: 'hsl(var(--text-low))' }}
          activeProps={{ style: { color: 'hsl(var(--brand))', background: 'hsl(var(--brand) / 0.08)' } }}
        >
          Machines
        </Link>
        <Link
          to="/cloud/bindings"
          className="text-sm font-medium px-2.5 py-1 rounded-sm transition-colors"
          style={{ color: 'hsl(var(--text-low))' }}
          activeProps={{ style: { color: 'hsl(var(--brand))', background: 'hsl(var(--brand) / 0.08)' } }}
        >
          Bindings
        </Link>
        <Link
          to="/cloud/jobs"
          className="text-sm font-medium px-2.5 py-1 rounded-sm transition-colors"
          style={{ color: 'hsl(var(--text-low))' }}
          activeProps={{ style: { color: 'hsl(var(--brand))', background: 'hsl(var(--brand) / 0.08)' } }}
        >
          Jobs
        </Link>
      </div>
      <Outlet />
    </div>
  )
}
