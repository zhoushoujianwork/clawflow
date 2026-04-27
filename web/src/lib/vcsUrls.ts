import { useEffect, useState } from 'react'

/**
 * Per-repo deep-link helpers.
 *
 * Each repo in /data/repos.json carries its own platform + base_url, so the
 * map below is keyed by full_name (e.g. "owner/repo") rather than by
 * platform alone — different repos can live on different hosts (public
 * github.com, gitlab.com, or a self-hosted GitLab instance).
 *
 * On any cache miss we fall back to public github.com / gitlab.com defaults
 * so a route still renders something sensible while repos.json is loading
 * or for runs that reference a repo no longer in config.
 */

export type Platform = 'github' | 'gitlab' | string

export interface RepoInfo {
  /** Defaults to "github" when the upstream config omits it. */
  platform: Platform
  /** Host root, no trailing slash, e.g. "https://github.com" or "http://git.patsnap.com". */
  host: string
}

export type RepoInfoMap = Record<string, RepoInfo>

interface RawRepo {
  full_name: string
  platform?: string
  base_url?: string
}

function defaultHostFor(platform: Platform): string {
  return platform === 'gitlab' ? 'https://gitlab.com' : 'https://github.com'
}

function normalizeHost(raw: string | undefined, platform: Platform): string {
  if (!raw) return defaultHostFor(platform)
  return raw.replace(/\/$/, '')
}

function infoFor(fullName: string, map: RepoInfoMap): RepoInfo {
  const hit = map[fullName]
  if (hit) return hit
  // Unknown repo (run was logged before the repo was removed from config,
  // or repos.json hasn't loaded yet). Default to github.com.
  return { platform: 'github', host: 'https://github.com' }
}

export function repoUrl(fullName: string, map: RepoInfoMap): string {
  const { host } = infoFor(fullName, map)
  return `${host}/${fullName}`
}

export function issueUrl(fullName: string, number: number, map: RepoInfoMap): string {
  const { platform, host } = infoFor(fullName, map)
  // GitLab 11.11+ accepts the canonical /-/issues/ form; older paths still
  // 301 to it but we don't support those anyway. GitHub uses /issues/.
  const segment = platform === 'gitlab' ? '/-/issues/' : '/issues/'
  return `${host}/${fullName}${segment}${number}`
}

/**
 * Build a per-repo info map from /data/repos.json. Returns an empty map
 * while loading; callers should still render — the helpers fall back to
 * public github.com defaults so links don't break during the first paint.
 */
export function useRepoInfoMap(): RepoInfoMap {
  const [map, setMap] = useState<RepoInfoMap>({})

  useEffect(() => {
    let cancelled = false
    fetch('/data/repos.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : []))
      .then((repos: unknown) => {
        if (cancelled) return
        const next: RepoInfoMap = {}
        if (Array.isArray(repos)) {
          for (const r of repos as RawRepo[]) {
            if (!r || typeof r.full_name !== 'string') continue
            const platform: Platform = r.platform || 'github'
            next[r.full_name] = {
              platform,
              host: normalizeHost(r.base_url, platform),
            }
          }
        }
        setMap(next)
      })
      .catch(() => {
        // Leave map empty; helpers default to github.com so links still work
        // for the common case.
      })
    return () => {
      cancelled = true
    }
  }, [])

  return map
}
