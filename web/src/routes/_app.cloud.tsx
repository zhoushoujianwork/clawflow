import { createFileRoute, Outlet } from '@tanstack/react-router'
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

  // Sub-nav was removed when Machines / Bindings / Jobs got promoted to
  // the top-level header — see _app.tsx. This layout is now just an Outlet
  // wrapper that exists so the existing /cloud/* URLs keep resolving.
  return <Outlet />
}
