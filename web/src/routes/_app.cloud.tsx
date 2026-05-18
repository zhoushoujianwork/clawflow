import { createFileRoute, Outlet, Link } from '@tanstack/react-router'
import { Cloud } from 'lucide-react'
import { useEffect, useState } from 'react'
import { fetchAuthMe, type AuthMeResult } from '../lib/cloudApi'

export const Route = createFileRoute('/_app/cloud')({
  component: CloudLayout,
})

function CloudLayout() {
  // Detect host mode the same way _app.tsx does: /api/v1/auth/me being
  // reachable tells us we're served by a cloud server. The legacy
  // `fetchCloudStatus()` probe hit a local endpoint that doesn't exist
  // on the cloud server, so visiting /cloud/* from clawflow.daboluo.cc
  // used to render "Cloud not configured" — exactly when cloud was
  // configured, which is backwards.
  const [auth, setAuth] = useState<AuthMeResult | undefined>(undefined)
  useEffect(() => {
    fetchAuthMe().then(setAuth).catch(() => setAuth({ kind: 'no-cloud' }))
  }, [])

  if (auth?.kind === 'no-cloud') {
    return (
      <div className="max-w-2xl mx-auto px-6 py-20 text-center">
        <Cloud className="mx-auto mb-4 opacity-30" style={{ color: 'hsl(var(--text-low))' }} size={40} />
        <h1 className="text-xl font-semibold mb-2" style={{ color: 'hsl(var(--text-high))' }}>
          No cloud connected
        </h1>
        <p className="text-sm mb-6" style={{ color: 'hsl(var(--text-low))' }}>
          This bundle is served by local <code>clawflow web</code>. Cloud features need a cloud server.
        </p>
        <div
          className="text-left rounded-lg px-4 py-3 font-mono text-xs border"
          style={{ background: 'hsl(var(--bg-panel))', borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-mid, var(--text-low)))' }}
        >
          <p className="mb-1" style={{ color: 'hsl(var(--text-low))' }}># Point this machine at a cloud instance</p>
          <p>clawflow cloud login --url https://your-cloud.example.com</p>
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
