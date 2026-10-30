import { createFileRoute, redirect } from '@tanstack/react-router'

export const Route = createFileRoute('/_app/dashboard')({
  beforeLoad: ({ location }) => {
    throw redirect({ to: '/pipeline', search: location.search, hash: location.hash, replace: true })
  },
})
