import { createFileRoute, Outlet } from '@tanstack/react-router'

// Layout for /jobs/*: the list lives in _app.jobs.index and a run in _app.jobs.$id.
export const Route = createFileRoute('/_app/jobs')({
  component: () => <Outlet />,
})
