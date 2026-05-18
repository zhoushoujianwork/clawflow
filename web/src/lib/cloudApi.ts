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

export function fetchCloudConfig(): Promise<CloudConfigSummary> {
  return cloudFetch<CloudConfigSummary>('/api/cloud/config')
}

export function fetchMachines(): Promise<{ machines: Machine[] }> {
  return cloudFetch<{ machines: Machine[] }>('/api/cloud/machines')
}

export function fetchBindings(): Promise<{ bindings: Binding[] }> {
  return cloudFetch<{ bindings: Binding[] }>('/api/cloud/bindings')
}

export function updateBinding(id: string, body: UpdateBindingRequest): Promise<Binding> {
  return cloudFetch<Binding>(`/api/cloud/bindings/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
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
