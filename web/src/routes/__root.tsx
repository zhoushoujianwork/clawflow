import { createRootRoute, Outlet } from '@tanstack/react-router'
import { useTheme } from '../lib/useTheme'

export const Route = createRootRoute({
  component: Root,
})

function Root() {
  useTheme()
  return (
    <div className="min-h-screen" style={{ background: 'hsl(var(--bg-primary))' }}>
      <Outlet />
    </div>
  )
}
