import { createFileRoute, Navigate } from '@tanstack/react-router'

// The Jobs UI now lives at /dashboard (it IS the cloud Dashboard). This
// route is kept only so old bookmarks and links to /cloud/jobs still
// resolve cleanly — it redirects rather than re-renders the JobsBoard.
export const Route = createFileRoute('/_app/cloud/jobs')({
  component: () => <Navigate to="/dashboard" replace />,
})
