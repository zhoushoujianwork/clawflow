// Headless logic tests for the run observation pages. No browser, provider or
// desktop is used; the stream is a scripted fake fetch.
import { createRequire } from 'node:module'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
const require = createRequire(import.meta.url)
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dir = await mkdtemp(join(tmpdir(), 'jobs-test-'))
try {
  const { build } = createRequire(require.resolve('vite'))('esbuild')
  await build({ stdin: { resolveDir: root, loader: 'tsx', contents: `
import assert from 'node:assert/strict'
import { jobElapsed, formatDuration, heartbeatState, ago, logToTraceEvents, mergeEntries, reconnectDelay, availabilityNote, detailLabel, isTerminal, parseTime } from './src/lib/jobs'
import { buildSteps } from './src/components/TraceView'
import { followLog } from './src/lib/followLog'
import { groupJobs } from './src/routes/_app.jobs.index'

// ---- durations (AC-01)
const T0 = Date.parse('2026-11-10T10:00:00Z')
assert.equal(formatDuration(0), '<1s'); assert.equal(formatDuration(59_000), '59s'); assert.equal(formatDuration(61_000), '1m 1s')
assert.equal(formatDuration(3_660_000), '1h 1m'); assert.equal(formatDuration(undefined), '—'); assert.equal(formatDuration(-5), '—'); assert.equal(formatDuration(NaN), '—')
const running = { status: 'running', started_at: '2026-11-10T09:59:00Z' }
assert.equal(jobElapsed(running, T0, true), 60_000)
// A stale snapshot stops the clock at the snapshot time instead of counting on.
assert.equal(jobElapsed(running, T0 + 600_000, false, '2026-11-10T09:59:30Z'), 30_000)
assert.equal(jobElapsed({ status: 'running' }, T0, true), undefined, 'unknown start is unavailable')
assert.equal(jobElapsed({ status: 'running', started_at: '0001-01-01T00:00:00Z' }, T0, true), undefined, 'zero time is unavailable')
assert.equal(jobElapsed({ status: 'queued', started_at: '2026-11-10T09:59:00Z' }, T0, true), undefined, 'queued has no duration')
assert.equal(jobElapsed({ status: 'success', elapsed_ms: 4000, started_at: '2026-11-10T09:00:00Z' }, T0, true), 4000, 'finished uses recorded elapsed, not now')
assert.equal(jobElapsed({ status: 'failed', started_at: '2026-11-10T09:00:00Z', ended_at: '2026-11-10T09:01:30Z' }, T0 + 9e9, true), 90_000, 'finished stops at its end time')
assert.equal(jobElapsed({ status: 'failed', started_at: '2026-11-10T09:00:00Z' }, T0, true), undefined, 'no end time is unavailable, not now')
assert.equal(parseTime('garbage'), undefined)
assert.ok(isTerminal('success') && isTerminal('failed') && isTerminal('cancelled') && !isTerminal('running') && !isTerminal('queued'))
assert.equal(detailLabel('lease_expired'), 'Lease expired'); assert.equal(detailLabel(''), undefined)

// ---- runners (AC-05)
assert.equal(heartbeatState('2026-11-10T09:59:30Z', T0), 'recent')
assert.equal(heartbeatState('2026-11-10T09:50:00Z', T0), 'lost')
assert.equal(heartbeatState(undefined, T0), 'unknown')
assert.equal(heartbeatState('2026-11-10T11:00:00Z', T0), 'unknown', 'future heartbeat is unknown')
assert.equal(ago(undefined, T0), 'never'); assert.equal(ago('2026-11-10T09:59:00Z', T0), '1m 0s ago')

// ---- grouping by repository and issue (AC-01)
const row = (id, repo, issue, pr = 0) => ({ id, repo, issue, pr })
const groups = groupJobs([row('a', 'o/r', 1), row('b', 'o/r', 1, 9), row('c', 'o/r', 1), row('d', 'o/x', 1)])
assert.deepEqual(groups.map(([, rows]) => rows.map(r => r.id)), [['a', 'c'], ['b'], ['d']])

// ---- log events reuse TraceView steps (AC-02, AC-09)
const e = (o) => ({ event: { seq: 1, ...o } })
const events = logToTraceEvents([
  e({ type: 'message', role: 'assistant', text: 'hello' }),
  e({ type: 'message', role: 'reasoning', text: 'thinking...' }),
  e({ type: 'tool_call', tool_call_id: 't1', tool_name: 'Bash', arguments: '{"command":"ls"}' }),
  e({ type: 'tool_result', tool_call_id: 't1', output: 'a.txt', is_error: false }),
  e({ type: 'tool_call', tool_call_id: 't2', tool_name: 'Edit', arguments: 'not json' }),
  e({ type: 'tool_result', tool_call_id: 't2', output: 'boom', is_error: true }),
  e({ type: 'gap', reason: 'dropped' }), e({ type: 'backend_error', error_kind: 'rate_limit', text: 'slow down' }),
  e({ type: 'attempt_start' }), e({ type: 'execution_end', status: 'error', error_kind: 'timeout' }),
  e({ type: 'message', role: 'assistant', text: '' }), { event: null }, { event: { type: 5 } },
])
const steps = buildSteps(events)
assert.ok(steps.some(s => s.kind === 'reply' && s.text === 'hello'))
assert.ok(steps.some(s => s.kind === 'thinking' && s.text === 'thinking...'))
const bash = steps.find(s => s.kind === 'tool' && s.name === 'Bash')
assert.deepEqual(bash.input, { command: 'ls' }); assert.equal(bash.result.body, 'a.txt'); assert.equal(bash.result.isError, false)
const edit = steps.find(s => s.kind === 'tool' && s.name === 'Edit')
assert.equal(edit.input, 'not json'); assert.equal(edit.result.isError, true)
assert.ok(steps.some(s => s.text?.includes('Output gap')))
assert.ok(steps.some(s => s.text?.includes('rate_limit')))
assert.ok(steps.some(s => s.kind === 'final' && s.isError))
assert.equal(steps.filter(s => s.kind === 'tool').length, 2, 'a result pairs with its call instead of adding a row')

// ---- merging never duplicates (AC-02)
const mk = (...ids) => ids.map(id => ({ id, invocation: 'i', event: { seq: id, type: 'message' } }))
assert.deepEqual(mergeEntries(mk(1, 2), mk(2, 3)).map(x => x.id), [1, 2, 3])
assert.deepEqual(mergeEntries(mk(1, 2), mk(1, 2)).map(x => x.id), [1, 2])
const same = mk(1); assert.equal(mergeEntries(same, []), same)
assert.deepEqual([0, 1, 2, 3, 4, 5, 9].map(reconnectDelay), [1000, 2000, 4000, 8000, 15000, 15000, 15000])
assert.ok(availabilityNote('not_captured').length > 0 && availabilityNote('expired').includes('expired') && availabilityNote('available') === '')

// ---- followLog resumes without duplicates or gaps (AC-02) ----
function sse(parts) { // parts: [{id, frame}] or '[DONE]'
  return parts.map(p => p === '[DONE]' ? 'data: [DONE]\\n\\n' : (p.id ? 'id: ' + p.id + '\\n' : '') + 'data: ' + JSON.stringify(p.frame) + '\\n\\n').join('')
}
const frame = (ids, extra = {}) => ({ task_id: 't', epoch: 7, epochs: [], active: true, availability: 'available', streams: [], entries: mk(...ids), next_after: ids.at(-1) ?? 0, ...extra })
function fakeFetch(script, urls) {
  return async (url, init) => {
    urls.push(url)
    const step = script.shift()
    if (!step) return new Promise(() => {})
    if (step.status) return { ok: false, status: step.status, statusText: 'x' }
    // Deliver the chunk on the first read and fail or close on the next one:
    // controller.error() would discard a chunk still sitting in the queue.
    let sent = false
    const body = new ReadableStream({ pull(c) {
      if (!sent) { sent = true; c.enqueue(new TextEncoder().encode(step.text)); return }
      if (step.drop) c.error(new TypeError('network drop')); else c.close()
    } })
    init.signal?.addEventListener('abort', () => {})
    return { ok: true, status: 200, body }
  }
}
async function run(script, opts = {}) {
  const urls = [], frames = [], phases = []
  globalThis.fetch = fakeFetch([...script], urls)
  let stop
  await new Promise(resolve => {
    stop = followLog({ jobId: 'job1', delay: () => 1, ...opts }, {
      onFrame: f => frames.push(f),
      onPhase: p => { phases.push(p); if (p === 'ended' || p === 'failed') resolve() },
    })
    setTimeout(resolve, 400)
  })
  stop()
  return { urls, frames, phases }
}
// 1. Drops mid-stream after entries 1-3, resumes after 3, finishes with 4-5.
let r = await run([
  { text: sse([{ id: 3, frame: frame([1, 2, 3]) }]), drop: true },
  { text: sse([{ id: 5, frame: frame([4, 5], { active: false }) }, '[DONE]']) },
])
assert.equal(r.urls.length, 2, 'one reconnect')
assert.match(r.urls[0], /\\/api\\/jobs\\/job1\\/logs\\?epoch=0&after=0$/)
assert.match(r.urls[1], /epoch=7&after=3$/, 'resumes after the last id and keeps the pinned attempt')
assert.deepEqual(r.frames.flatMap(f => f.entries.map(x => x.id)), [1, 2, 3, 4, 5])
assert.ok(r.phases.includes('reconnecting') && r.phases.at(-1) === 'ended')
// 2. A clean close without [DONE] also resumes.
r = await run([{ text: sse([{ id: 2, frame: frame([1, 2]) }]) }, { text: sse([{ id: 3, frame: frame([3]) }, '[DONE]']) }])
assert.equal(r.urls.length, 2); assert.match(r.urls[1], /after=2$/)
// 3. Server errors retry; 404 stops for good.
r = await run([{ status: 503 }, { text: sse([{ id: 1, frame: frame([1]) }, '[DONE]']) }])
assert.equal(r.urls.length, 2); assert.equal(r.phases.at(-1), 'ended')
r = await run([{ status: 404 }, { text: 'unreachable' }])
assert.equal(r.urls.length, 1, '404 is not retried'); assert.equal(r.phases.at(-1), 'failed')
// 4. After [DONE] nothing reconnects.
r = await run([{ text: sse(['[DONE]']) }, { text: 'unreachable' }])
assert.equal(r.urls.length, 1)
// 5. Resuming from a held cursor.
r = await run([{ text: sse(['[DONE]']) }], { after: 12, epoch: 4 })
assert.match(r.urls[0], /epoch=4&after=12$/)
// 6. Malformed frames are skipped, not fatal.
r = await run([{ text: 'data: {not json\\n\\n' + sse([{ id: 1, frame: frame([1]) }, '[DONE]']) }])
assert.equal(r.frames.length, 1)
// 7. Backoff doubles per consecutive failure.
const waits = []
r = await run([{ status: 500 }, { status: 500 }, { status: 500 }], { delay: n => { waits.push(n); return 1 } })
assert.deepEqual(waits, [0, 1, 2], 'consecutive failures count up so the delay grows')
// A frame received in between resets the count: the next failure starts over.
const waits2 = []
r = await run([{ status: 500 }, { text: sse([{ id: 1, frame: frame([1]) }]), drop: true }, { status: 500 }], { delay: n => { waits2.push(n); return 1 } })
assert.deepEqual(waits2, [0, 0, 1], 'a successful frame resets the failure count')
console.log('PASS jobs: durations, staleness, runners, grouping, trace reuse, merge, resume without duplicates or gaps, 404/503, backoff')
` }, bundle: true, platform: 'node', format: 'esm', outfile: join(dir, 'test.mjs'), banner: { js: "import { createRequire as __cr } from 'node:module'; const require = __cr(import.meta.url);" }, logLevel: 'error', plugins: [{ name: 'router-stub', setup(b) {
    b.onResolve({ filter: /^@tanstack\/react-router$/ }, () => ({ path: 'router', namespace: 'stub' }))
    b.onLoad({ filter: /.*/, namespace: 'stub' }, () => ({ contents: 'export const createFileRoute = () => options => options; export const Link = () => null' }))
  } }] })
  process.stdout.write(execFileSync(process.execPath, [join(dir, 'test.mjs')], { encoding: 'utf8' }))
} finally { await rm(dir, { recursive: true, force: true }) }
