import { createFileRoute, Navigate } from '@tanstack/react-router'

export const Route = createFileRoute('/')({
  component: LandingRedirect,
})

function LandingRedirect() {
  // The local dashboard has no marketing surface — bounce straight to
  // /dashboard. Redirecting in a route component (rather than the router
  // config) keeps dev server and embed-served build behaving identically.
  return <Navigate to="/dashboard" replace />
}
