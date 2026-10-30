export const WORK_STAGES = ['queued', 'implementing', 'pr_open', 'ci_pending', 'ci_failed', 'acceptance_pending', 'acceptance_running', 'accepted', 'rejected', 'infra_retry', 'merged', 'failed', 'blocked'] as const
export type WorkSearch = { q?: string; workRepo?: string; project?: string; stage?: string; role?: string; sort?: 'updated' | 'age' }
const text = (value: unknown) => typeof value === 'string' && value.length <= 256 && value.trim() ? value : undefined

// No saved preferences: even an empty or invalid URL has the same safe defaults.
export function workSearch(s: Record<string, unknown>): WorkSearch {
  return {
    q: text(s.q) ?? (typeof s.q === 'number' && Number.isSafeInteger(s.q) && s.q > 0 ? String(s.q) : undefined), workRepo: text(s.workRepo), project: text(s.project),
    stage: WORK_STAGES.includes(s.stage as typeof WORK_STAGES[number]) ? s.stage as string : undefined,
    role: text(s.role), sort: s.sort === 'updated' || s.sort === 'age' ? s.sort : undefined,
  }
}
export const CLEAR_WORK_SEARCH = { q: undefined, workRepo: undefined, project: undefined, stage: undefined, role: undefined, sort: undefined, work: undefined, tab: undefined } as const
