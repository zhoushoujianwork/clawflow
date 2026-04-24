/**
 * Stub for the local dashboard. No auth, no tokens, no logout.
 *
 * The ported SaaS routes import useAuth() and authHeaders() in various
 * places; this stub lets those imports compile without changing every
 * route. All operations are no-ops: fetch() sends no extra headers,
 * saveToken() discards, useAuth() reports "logged in" so auth-gated
 * layouts don't redirect.
 */

export function authHeaders(): Record<string, string> {
  return {}
}

export function saveToken(_token: string): void {
  // no-op
}

export function useAuth() {
  return {
    user: { username: 'local' } as { username: string } | null,
    loading: false,
    isLoggedIn: true,
    token: null as string | null,
    logout: () => {
      // no-op
    },
  }
}
