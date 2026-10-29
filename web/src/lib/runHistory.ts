/** GET /api/runs keeps its array response and unpaged default. Supply limit
 * (1..10000) for bounded reads; continue with the X-Next-Cursor response header.
 * from is inclusive and until exclusive (RFC3339 or YYYY-MM-DD).
 * path and attempt_id select the existing full detail payload, not a list.
 * Role is stored execution attribution; absent legacy roles match "unknown".
 */
export interface RunHistoryQuery {
  kind?: 'run' | 'pilot'
  repo?: string
  project?: string
  from?: string
  until?: string
  execution_status?: string
  role?: string
  profile?: string
  limit?: number
  cursor?: string
  attempt_id?: string
  path?: string
}

export interface RunAttribution {
  source: 'clawflow'
  kind: 'run' | 'pilot'
  repo: string
  project: string
  operator: string
  role: string
  profile: string
  machine: string
}

export interface RunAvailability {
  attempt: boolean
  execution_status: boolean
  exit_code: boolean
  elapsed: boolean
  cost: boolean
  tokens: boolean
}

export interface RunHistoryEntry {
  attempt_id: string
  attribution: RunAttribution
  availability: RunAvailability
  path: string
  repo: string
  project?: string
  operator: string
  issue_number?: number
  issue_title?: string
  started_at: string
  ended_at?: string
  status: string
  execution_status?: string
  profile: string
  backend: string
  model: string
  machine: string
  offload_run_id?: string
  caller_run_id?: string
  session_id?: string
  exit_code: number | null
  elapsed_ms: number | null
  usage?: {
    total_cost_usd: number | null
    input_tokens: number | null
    output_tokens: number | null
    cache_read_input_tokens: number | null
    cache_creation_input_tokens: number | null
  }
}
