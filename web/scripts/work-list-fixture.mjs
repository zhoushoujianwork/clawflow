// Shared, synthetic data only: 50 repositories / 500 issues, plus duplicate input.
export function workListFixture() {
  const now = new Date().toISOString()
  const execution = (id, status = 'running') => ({ attempt_id: id, operator: 'implement', role: 'implement',
    status, started_at: now, elapsed_ms: 123000, last_progress_at: now,
    ...(status === 'success' ? { ended_at: now } : {}),
    provenance: { source: status === 'success' ? 'offload_final' : 'offload_live', stale: false } })
  const repos = Array.from({ length: 50 }, (_, r) => ({ repo: `fixture/repository-${r}`, enabled: r % 10 !== 9,
    max_in_flight: 2, items: Array.from({ length: 10 }, (_, n) => ({
      issue: n + 1, title: `Task ${r}-${n}: ${'LongTitleWithoutSpaces'.repeat(8)} supports cross-repository work`,
      state: n === 7 || n === 8 ? 'closed' : 'open',
      stage: ['implementing', 'queued', 'ci_pending', 'acceptance_pending', 'queued', 'queued', 'queued', 'merged', 'merged', 'blocked'][n],
      why: ['Implementation in progress', 'Waiting in the queue', 'CI is still running on the current head', 'Waiting for independent acceptance', 'Waiting in the queue', 'Waiting in the queue', 'Execution ended; delivery is not confirmed', 'Pull request merged', 'Old merge', 'Review needs attention'][n] + ' · ' + 'DetailedReasonWithoutSpaces'.repeat(6),
      evidence: [], updated_at: now,
      attention: n === 9 ? ['Review needs attention'] : [],
      executions: n === 0 ? [execution(`${r}-a`), execution(`${r}-b`)] : n === 6 ? [execution(`${r}-success`, 'success')] : [],
      ...(n === 7 || n === 8 ? { delivery: { pr: 100 + n, merge_commit_sha: 'a'.repeat(40), merged_at: n === 7 ? now : new Date(Date.now() - 8 * 86400000).toISOString() } } : {}),
    })) }))
  // Deliberately interleave repositories in the authoritative queue, independent of title/priority.
  const queue = Array.from({ length: 7 }, (_, n) => repos.map((repo, r) => ({ repo: repo.repo,
    number: n + 1, position: n * 50 + r + 1, tier: r % 2 ? 'P0' : 'P3',
    state: n === 4 ? 'waiting:dependency' : n === 5 ? 'eligible:max_in_flight' : n === 0 ? 'in_flight' : 'eligible',
  }))).flat()
  return { repos, queue, now }
}
