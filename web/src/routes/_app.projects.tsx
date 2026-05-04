import { createFileRoute, Outlet } from '@tanstack/react-router'

// Pure pass-through layout. The actual list view lives in
// _app.projects.index.tsx so that nested detail routes (e.g.
// /projects/$name) get a chance to mount via this Outlet.
// Without it, child routes match but never render.
export const Route = createFileRoute('/_app/projects')({
  component: () => <Outlet />,
})
