/**
 * Reverse-index utility: given a repo full_name and the project list,
 * return the names of all projects that contain it.
 */
export interface ProjectEntry {
  name: string
  repos: string[]
}

export function findProjectsForRepo(
  repoFullName: string,
  projects: ProjectEntry[],
): string[] {
  return projects
    .filter(p => Array.isArray(p.repos) && p.repos.includes(repoFullName))
    .map(p => p.name)
}
