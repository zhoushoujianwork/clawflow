import type { RepoInfoMap, Platform } from '../lib/vcsUrls'

/**
 * Inline brand glyph for the VCS host of a given repo. lucide-react no longer
 * ships Github/Gitlab brand icons, so we hand-roll two small SVGs (paths from
 * simple-icons, MIT-licensed). Defaults to github when the platform is unknown
 * — same fallback the URL helpers use.
 */

const GITHUB_PATH =
  'M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12'

const GITLAB_PATH =
  'm23.6004 9.5927-.0337-.0862L20.3.9181a.851.851 0 00-.3392-.408.8753.8753 0 00-.5039-.1431.8806.8806 0 00-.5018.1495.8898.8898 0 00-.3252.4178l-2.2073 6.7479H7.5739L5.3667.9341a.8709.8709 0 00-.3236-.4194.8807.8807 0 00-.5024-.1494.8859.8859 0 00-.5034.143.8709.8709 0 00-.3393.408L.4441 9.5015l-.033.0825a6.0026 6.0026 0 001.9897 6.9335l.0107.0082.0287.0222 4.92 3.6856 2.4358 1.8436 1.4847 1.1227a1.0282 1.0282 0 001.2563 0l1.4842-1.1227 2.4365-1.8443 4.9504-3.7077.0125-.01a6.0023 6.0023 0 001.9883-6.9242z'

interface VcsIconProps {
  /** Pass the repo full_name and the map; we look up the platform. */
  repo: string
  map: RepoInfoMap
  className?: string
}

/**
 * Resolve a repo's platform from the info map, defaulting to "github" — the
 * same fallback the URL helpers in vcsUrls.ts use.
 */
function platformOf(repo: string, map: RepoInfoMap): Platform {
  return map[repo]?.platform === 'gitlab' ? 'gitlab' : 'github'
}

export function VcsIcon({ repo, map, className = 'w-3.5 h-3.5 shrink-0' }: VcsIconProps) {
  const platform = platformOf(repo, map)
  const path = platform === 'gitlab' ? GITLAB_PATH : GITHUB_PATH
  return (
    <svg
      viewBox="0 0 24 24"
      className={className}
      fill="currentColor"
      role="img"
      aria-label={platform}
    >
      <title>{platform}</title>
      <path d={path} />
    </svg>
  )
}
