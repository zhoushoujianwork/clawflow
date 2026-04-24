import { useMemo } from 'react'

/**
 * Host-map resolution for repo / issue deep-links.
 *
 * On the SaaS side this hit `/api/v1/orgs/current/integrations` to pull the
 * user's custom GitLab instance URLs. Locally we don't have that lookup —
 * self-hosted GitLab URLs come straight from config.yaml via repos.json's
 * base_url field, so callers with a repo in hand should use that directly.
 * These helpers remain for ported pages that expect the old interface, and
 * they default to the public instances.
 */

export type Platform = 'github' | 'gitlab' | string
export type HostMap = Record<string, string>

export function repoHost(platform: Platform | undefined, hostMap: HostMap): string {
  if (platform && hostMap[platform]) return hostMap[platform]
  return platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com'
}

export function repoUrl(platform: Platform | undefined, fullName: string, hostMap: HostMap): string {
  return `${repoHost(platform, hostMap)}/${fullName}`
}

export function issueUrl(
  platform: Platform | undefined,
  fullName: string,
  number: number,
  hostMap: HostMap,
): string {
  return `${repoHost(platform, hostMap)}/${fullName}/issues/${number}`
}

/** No-op in local mode. Returns an empty map so public-instance defaults apply. */
export function useHostMap(): HostMap {
  return useMemo(() => ({}), [])
}
