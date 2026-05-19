// cloudApi.ts — typed fetch helpers for the ClawFlow cloud proxy.
// The browser calls /api/cloud/* which the Go web server proxies to the
// configured cloud URL using the stored access token — the token is never
// exposed to JavaScript.

// ---- Domain types (mirror internal/cloud/types.go) ----

export interface Machine {
  id: string
  hostname: string
  display_name?: string
  version?: string
  capabilities?: string[]
  last_seen_at: string // ISO 8601
}

export interface Worker {
  id: string
  machine_id: string
  status: string
  capacity: number
  active_runs?: string[]
  last_seen_at: string
}

export interface Binding {
  id: string
  machine_id: string
  repo_id?: string
  project_id?: string
  created_at: string
  updated_at: string
}

export interface Project {
  id: string
  name: string
  description?: string
  created_at: string
  updated_at: string
}

export interface Repo {
  id: string
  name: string
  platform: string
  project_id?: string
  base_branch?: string
  created_at: string
  updated_at: string
}

export interface JobSpec {
  job_id: string
  run_id?: string
  repo: string
  platform: string
  operator: string
  target: string
  number: number
  title?: string
  labels?: string[]
}

export interface JobRecord {
  spec: JobSpec
  status: 'pending' | 'leased' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'expired'
  bound_machine_id?: string
  lease_worker_id?: string
  lease_expires_at?: string
  attempt_count: number
  created_at: string
  updated_at: string
}

export interface RunRecord {
  id: string
  job_id: string
  status: 'running' | 'succeeded' | 'failed' | 'cancelled'
  outcome?: string
  summary?: string
  error?: string
  started_at: string
  ended_at?: string
}

export interface CloudStatus {
  configured: boolean
  url: string
}

/** Shape of /api/v1/auth/me. Returned when the user has a valid session
 *  cookie (browser) or a valid Bearer token (CLI). */
export interface AuthMe {
  id: string
  github_id: number
  login: string
  name?: string
  avatar_url?: string
}

export interface CloudConfigSummary {
  projects: Project[]
  repos: Repo[]
  machines: Machine[]
  bindings: Binding[]
  counts: {
    projects: number
    repos: number
    machines: number
    bindings: number
    jobs: number
    runs: number
  }
}

// ---- Update request types ----

export interface UpdateBindingRequest {
  machine_id?: string
  repo_id?: string
  project_id?: string
}

// ---- Fetch helpers ----

async function cloudFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(`${res.status} ${res.statusText}: ${text}`)
  }
  return res.json() as Promise<T>
}

export function fetchCloudStatus(): Promise<CloudStatus> {
  return cloudFetch<CloudStatus>('/api/cloud/status')
}

/** AuthMeResult distinguishes the three states the header bar cares about:
 *  - 'authed':       we're on the cloud and have a valid session.
 *  - 'anon':         we're on the cloud but signed out (401).
 *  - 'no-cloud':     /api/v1/auth/me doesn't exist on this origin (404 or
 *                    network error) — i.e. this bundle was loaded from the
 *                    legacy local `clawflow web` server. */
export type AuthMeResult =
  | { kind: 'authed'; user: AuthMe }
  | { kind: 'anon' }
  | { kind: 'no-cloud' }

export async function fetchAuthMe(): Promise<AuthMeResult> {
  let res: Response
  try {
    res = await fetch('/api/v1/auth/me', { credentials: 'include' })
  } catch {
    return { kind: 'no-cloud' }
  }
  if (res.status === 401) return { kind: 'anon' }
  if (res.status === 404) return { kind: 'no-cloud' }
  if (!res.ok) throw new Error(`auth/me: ${res.status}`)
  const user = (await res.json()) as AuthMe
  return { kind: 'authed', user }
}

/** signOut deletes the session cookie. The server clears the cookie
 *  server-side and DB-side; the caller should reload after. */
export async function signOut(): Promise<void> {
  await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'include' })
}

export function fetchCloudConfig(): Promise<CloudConfigSummary> {
  return cloudFetch<CloudConfigSummary>('/api/cloud/config')
}

export function fetchMachines(): Promise<{ machines: Machine[] }> {
  return cloudFetch<{ machines: Machine[] }>('/api/cloud/machines')
}

export function fetchBindings(): Promise<{ bindings: Binding[] }> {
  return cloudFetch<{ bindings: Binding[] }>('/api/cloud/bindings')
}

export function createBinding(body: {
  machine_id: string
  repo_id?: string
  project_id?: string
}): Promise<Binding> {
  return cloudFetch<Binding>('/api/cloud/bindings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export function updateBinding(id: string, body: UpdateBindingRequest): Promise<Binding> {
  return cloudFetch<Binding>(`/api/cloud/bindings/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export async function deleteBinding(id: string): Promise<void> {
  const res = await fetch(`/api/cloud/bindings/${id}`, { method: 'DELETE', credentials: 'include' })
  if (!res.ok && res.status !== 204) {
    throw new Error(`delete binding: ${res.status}`)
  }
}

// ---- Usage ----

/** UsageAggregate mirrors the JSON shape produced by the cloud
 * /api/cloud/usage/summary endpoint and by the local `clawflow web`
 * data/usage.json. Both surfaces use the same shape so the Usage
 * page can render either without translation. */
export interface UsageAggregate {
  runs: number
  total_cost_usd: number
  input_tokens: number
  output_tokens: number
  cache_read_input_tokens: number
  cache_creation_input_tokens: number
  duration_ms: number
}

export interface ModelAggregate {
  cost_usd: number
  input_tokens: number
  output_tokens: number
  cache_read_input_tokens: number
  cache_creation_input_tokens: number
}

export interface DailyPoint {
  date: string
  runs: number
  total_cost_usd: number
  input_tokens: number
  output_tokens: number
  by_operator?: Record<string, UsageAggregate>
  by_repo?: Record<string, UsageAggregate>
  by_model?: Record<string, ModelAggregate>
}

export interface PeriodSummary {
  period_start: string
  period_end: string
  totals: UsageAggregate
  by_operator: Record<string, UsageAggregate>
  by_repo: Record<string, UsageAggregate>
  by_model: Record<string, ModelAggregate>
  daily_trend: DailyPoint[]
}

export interface UsageSummary {
  generated_at: string
  totals: UsageAggregate
  by_operator: Record<string, UsageAggregate>
  by_repo: Record<string, UsageAggregate>
  by_model: Record<string, ModelAggregate>
  periods?: PeriodSummary[]
}

export function fetchUsageSummary(): Promise<UsageSummary> {
  return cloudFetch<UsageSummary>('/api/cloud/usage/summary')
}

// ---- Repos ----

export function fetchRepos(): Promise<{ repos: Repo[] }> {
  return cloudFetch<{ repos: Repo[] }>('/api/cloud/repos')
}

export function createRepo(body: {
  name: string
  platform?: string
  project_id?: string
  base_branch?: string
}): Promise<Repo> {
  return cloudFetch<Repo>('/api/cloud/repos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export function updateRepo(id: string, body: {
  project_id?: string
  base_branch?: string
}): Promise<Repo> {
  return cloudFetch<Repo>(`/api/cloud/repos/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export async function deleteRepo(id: string): Promise<void> {
  const res = await fetch(`/api/cloud/repos/${id}`, { method: 'DELETE', credentials: 'include' })
  if (!res.ok && res.status !== 204) {
    throw new Error(`delete repo: ${res.status}`)
  }
}

// ---- Projects ----

export function fetchProjects(): Promise<{ projects: Project[] }> {
  return cloudFetch<{ projects: Project[] }>('/api/cloud/projects')
}

export function createProject(body: {
  name: string
  description?: string
}): Promise<Project> {
  return cloudFetch<Project>('/api/cloud/projects', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export async function deleteProject(id: string): Promise<void> {
  const res = await fetch(`/api/cloud/projects/${id}`, { method: 'DELETE', credentials: 'include' })
  if (!res.ok && res.status !== 204) {
    throw new Error(`delete project: ${res.status}`)
  }
}

export function fetchJobs(): Promise<{ jobs: JobRecord[] }> {
  return cloudFetch<{ jobs: JobRecord[] }>('/api/cloud/jobs')
}

export function fetchRuns(): Promise<{ runs: RunRecord[] }> {
  return cloudFetch<{ runs: RunRecord[] }>('/api/cloud/runs')
}

// ---- Helpers ----

export function timeAgo(iso: string | undefined): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (!isFinite(t)) return '—'
  const diff = Math.floor((Date.now() - t) / 1000)
  if (diff < 0) return 'just now'
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}min ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

/** Returns true when the machine has sent a heartbeat in the last 5 minutes. */
export function isMachineOnline(m: Machine): boolean {
  if (!m.last_seen_at) return false
  const age = Date.now() - new Date(m.last_seen_at).getTime()
  return age < 5 * 60 * 1000
}
