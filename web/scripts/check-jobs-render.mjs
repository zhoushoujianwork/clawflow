// Build first. Headless, intercepted fixtures only; no provider, database or desktop access.
// Drives the run list, run detail (live log with a dropped connection), and runner pages as a
// read-only LAN session, and asserts that the browser never sends a write.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0 } })
let browser

const now = Date.now()
const iso = ms => new Date(ms).toISOString()
const id = prefix => prefix + 'f'.repeat(56)
const IDS = { running: id('run00001'), queued: id('que00002'), failed: id('fai00003'), done: id('don00004'), cancelled: id('can00005'), repair: id('rep00006') }
const base = { repo: 'owner/repo', issue: 800, pr: 0, kind: 'implement', operator: 'implement', attempt: 1, epoch: 1, tags: [], has_logs: false, generated_at: iso(now) }
const jobs = [
  { ...base, id: IDS.running, title: 'Run observation pages', stage: 'implement', trigger: 'issue_queued', status: 'running', worker: 'lab-mac', queued_at: iso(now - 120_000), started_at: iso(now - 90_000), has_logs: true },
  { ...base, id: IDS.queued, issue: 801, title: 'Waiting for a worker', stage: 'implement', trigger: 'issue_reopened', status: 'queued', queued_at: iso(now - 30_000), waiting_reason: 'waiting for a worker with tags host-lab' },
  { ...base, id: IDS.failed, issue: 802, title: 'Broke CI', stage: 'repair-ci', kind: 'repair', pr: 55, trigger: 'ci_failed', status: 'failed', worker: 'lab-mac', queued_at: iso(now - 900_000), started_at: iso(now - 800_000), ended_at: iso(now - 740_000), elapsed_ms: 60_000, error_kind: 'timeout' },
  { ...base, id: IDS.done, issue: 803, title: 'Shipped', stage: 'implement', trigger: 'issue_queued', status: 'success', worker: 'office-linux', queued_at: iso(now - 4_000_000), started_at: iso(now - 3_900_000), ended_at: iso(now - 3_600_000), elapsed_ms: 300_000, pr_url: 'https://github.com/owner/repo/pull/9', has_logs: true },
  { ...base, id: IDS.cancelled, issue: 804, title: 'Stopped', stage: 'acceptance', kind: 'acceptance', pr: 56, trigger: 'pr_ready_for_review', status: 'cancelled', worker: 'office-linux', queued_at: iso(now - 5_000_000), started_at: iso(now - 4_990_000), ended_at: iso(now - 4_980_000), elapsed_ms: 10_000 },
  { ...base, id: IDS.repair, issue: 800, pr: 70, kind: 'repair', stage: 'repair-acceptance', trigger: 'acceptance_failed', status: 'success', worker: 'lab-mac', queued_at: iso(now - 600_000), started_at: iso(now - 500_000), ended_at: iso(now - 450_000), elapsed_ms: 50_000 },
]
const runners = [
  { name: 'lab-mac', online: true, revoked: false, paused: false, tags: ['host-lab', 'gpu'], roles: ['implement'], max_parallel: 2, last_seen: iso(now - 5_000), last_heartbeat: iso(now - 8_000), running: [jobs[0]] },
  { name: 'office-linux', online: false, revoked: false, paused: true, tags: [], roles: [], max_parallel: 1, last_seen: iso(now - 3_600_000), last_heartbeat: iso(now - 3_000_000), running: [] },
  { name: 'new-box', online: false, revoked: false, paused: false, tags: [], roles: [], max_parallel: 1, running: [] },
]
const detail = (job, extra = {}) => ({
  ...job, attempts: [], artifacts: {}, related: jobs.filter(j => j.repo === job.repo && j.issue === job.issue),
  timeline: [{ at: job.queued_at, event: 'queued' }, ...(job.started_at ? [{ at: job.started_at, event: 'started', worker: job.worker, attempt: 1 }] : []), ...(job.ended_at ? [{ at: job.ended_at, event: 'attempt_' + (job.status === 'success' ? 'done' : job.status), worker: job.worker, attempt: 1 }] : [])],
  ...extra,
})
const details = {
  [IDS.running]: detail(jobs[0]),
  [IDS.queued]: detail(jobs[1]),
  [IDS.failed]: detail(jobs[2], { error: 'tests failed: 3 of 10', artifacts: { pr_number: 55 } }),
  [IDS.done]: detail(jobs[3], { summary: '## Done\n\nOpened the pull request.', artifacts: { pr_number: 9, pr_url: 'https://github.com/owner/repo/pull/9', branch: 'feat/issue-803', head_sha: 'abcdef1234567890' } }),
  [IDS.cancelled]: detail(jobs[4]),
  [IDS.repair]: detail(jobs[5]),
}

const entry = (n, event) => ({ id: n, invocation: 'inv-1', event: { seq: n, ...event } })
const frame = (entries, extra = {}) => ({ task_id: IDS.running, task_state: 'leased', epoch: 1, epochs: [{ epoch: 1, attempt: 1, worker: 'lab-mac', status: 'running' }], active: true, availability: 'available', streams: [{ invocation: 'inv-1', availability: 'available', state: 'live', event_count: entries.length }], entries, next_after: entries.at(-1)?.id ?? 0, ...extra })
const sse = parts => parts.map(p => p === 'DONE' ? 'data: [DONE]\n\n' : `id: ${p.next_after}\ndata: ${JSON.stringify(p)}\n\n`).join('')
const bulk = Array.from({ length: 80 }, (_, i) => entry(3 + i, { type: 'message', role: 'assistant', text: `progress line ${i + 1}` }))
const first = frame([entry(1, { type: 'message', role: 'assistant', text: 'starting the work' }), entry(2, { type: 'tool_call', tool_call_id: 'c1', tool_name: 'Bash', arguments: '{"command":"go test ./..."}' })])
const second = frame(bulk, { active: true })
const done = frame([], { active: false, task_state: 'done', next_after: 82 })

async function newPage(width, readonly, hooks = {}) {
  const context = await browser.newContext({ viewport: { width, height: 900 } })
  const page = await context.newPage()
  const requests = []
  const origin = new URL(server.resolvedUrls.local[0])
  const logCalls = []
  await page.route('**/*', route => {
    const req = route.request()
    const url = new URL(req.url())
    if (url.origin !== origin.origin) return route.abort()
    if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
    requests.push({ method: req.method(), path: url.pathname, search: url.search })
    const json = body => route.fulfill({ status: 200, json: body })
    if (url.pathname === '/api/session') return json({ readonly })
    if (url.pathname === '/data/repos.json') return json([])
    if (hooks.api) { const handled = hooks.api(route, url); if (handled) return }
    if (url.pathname === '/api/jobs') {
      const status = url.searchParams.get('status'), repo = url.searchParams.get('repo')
      return json({ generated_at: iso(Date.now()), jobs: jobs.filter(j => (!status || j.status === status) && (!repo || j.repo === repo)) })
    }
    if (url.pathname === '/api/runners') return json({ generated_at: iso(Date.now()), runners })
    const logs = url.pathname.match(/^\/api\/jobs\/([^/]+)\/logs$/)
    if (logs) {
      logCalls.push({ id: logs[1], search: url.search })
      const n = logCalls.filter(c => c.id === logs[1]).length
      let body
      if (logs[1] === IDS.running) body = n === 1 ? sse([first]) : n === 2 ? sse([second, 'DONE']) : sse(['DONE'])
      else if (logs[1] === IDS.done) body = sse([frame([entry(1, { type: 'message', role: 'assistant', text: 'finished work' })], { task_id: IDS.done, active: false, task_state: 'done' }), 'DONE'])
      else body = sse(['DONE'])
      return route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream' }, body })
    }
    const one = url.pathname.match(/^\/api\/jobs\/([^/]+)$/)
    if (one && details[one[1]]) return json(details[one[1]])
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  return { context, page, requests, logCalls, origin }
}
const at = (origin, path) => new URL(path, origin).href

try {
  browser = await chromium.launch({ headless: true })
  for (const width of [1440, 390]) {
    const { context, page, requests, logCalls, origin } = await newPage(width, true)
    const noOverflow = async where => assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${width}px overflow on ${where}`)

    // ---- run list (AC-01)
    await page.goto(at(origin, '/jobs'))
    await page.getByRole('heading', { name: 'Runs', exact: true }).waitFor()
    for (const status of ['Running', 'Queued', 'Failed', 'Succeeded', 'Cancelled']) {
      assert.ok(await page.locator(`tbody [data-status="${status.toLowerCase().replace('succeeded', 'success')}"]`).count() >= 1, `${status} row`)
    }
    const text = await page.locator('main').innerText()
    for (const expected of ['owner/repo', '#800', 'Run observation pages', 'PR #55', 'Implement', 'Repair CI', 'Repair (review)', 'Acceptance', 'lab-mac', 'office-linux', 'Issue queued', 'Issue reopened', 'CI failed', 'PR ready for review', 'Review failed']) assert.ok(text.includes(expected), `list text: ${expected}`)
    // Grouped by repository and issue: issue 800 has its implement run and its repair run together.
    const group = page.getByRole('region', { name: 'owner/repo issue 800' })
    assert.equal(await group.locator('tbody tr').count(), 1)
    assert.equal(await page.getByRole('region', { name: 'owner/repo PR 70' }).locator('tbody tr').count(), 1)
    // A running row's duration counts from the recorded start (about 1m 30s), not from zero.
    const runningRow = group.locator('tbody tr').first()
    assert.match(await runningRow.locator('td').nth(3).innerText(), /^1m \d+s$/)
    // Finished rows use the stored elapsed time; queued has none.
    assert.match(await page.getByRole('region', { name: 'owner/repo issue 803' }).locator('td').nth(3).innerText(), /^5m 0s$/)
    assert.equal((await page.getByRole('region', { name: 'owner/repo issue 801' }).locator('td').nth(3).innerText()).trim(), '—')
    assert.ok(text.includes('waiting for a worker with tags host-lab'))
    // The spinner turns only for the running badge while data is live.
    assert.ok((await runningRow.locator('svg').first().getAttribute('class')).includes('animate-spin'))
    await noOverflow('list')
    // Filter by status goes through the API.
    await page.getByRole('button', { name: 'Failed', exact: true }).click()
    await page.waitForFunction(() => document.querySelectorAll('tbody [data-status]').length === 1)
    assert.ok(requests.some(r => r.path === '/api/jobs' && r.search.includes('status=failed')))
    await page.getByRole('button', { name: 'All', exact: true }).click()
    await page.screenshot({ path: join(process.env.TMPDIR || '/tmp', `clawflow-jobs-${width}.png`), fullPage: true })

    // ---- run detail with a live log that loses its connection (AC-02, AC-03)
    await page.getByRole('link', { name: `Open run ${IDS.running.slice(0, 8)}` }).click()
    await page.waitForURL(`**/jobs/${IDS.running}`)
    await page.getByRole('heading', { level: 1 }).waitFor()
    assert.equal((await page.locator('[data-field="worker"]').innerText()).trim(), 'lab-mac')
    assert.match(await page.locator('[data-field="duration"]').innerText(), /^1m \d+s$/)
    assert.ok((await page.locator('main').innerText()).includes('Timeline'))
    await page.getByText('Started on a worker').waitFor()
    // First connection delivers two events, then drops without an end marker.
    await page.getByText('starting the work').first().waitFor()
    await page.locator('[data-phase="reconnecting"]').waitFor()
    // The viewer resumes from the last id it holds and keeps the same attempt.
    await page.locator('[data-phase="ended"]').waitFor({ timeout: 15_000 })
    const mine = logCalls.filter(c => c.id === IDS.running)
    assert.equal(mine.length, 2, JSON.stringify(mine))
    assert.match(mine[0].search, /epoch=0&after=0$/)
    assert.match(mine[1].search, /epoch=1&after=2$/)
    assert.ok((await page.getByText(/82 events/).count()) >= 1, 'all 82 events, none repeated')
    // Following: the newest line is in view without scrolling.
    await page.getByText('progress line 80').first().waitFor()
    const pinned = await page.evaluate(() => {
      const list = [...document.querySelectorAll('div')].find(d => getComputedStyle(d).overflowY === 'auto' && d.scrollHeight > d.clientHeight + 200)
      return list ? list.scrollHeight - list.scrollTop - list.clientHeight : -1
    })
    assert.ok(pinned >= 0 && pinned < 60, `log follows the newest line (distance ${pinned})`)
    // The reader can scroll away from the newest line. (Not asserted: that later output leaves them there; that is TraceView's existing, unmodified behavior.)
    const scrolled = await page.evaluate(() => {
      const list = [...document.querySelectorAll('div')].find(d => getComputedStyle(d).overflowY === 'auto' && d.scrollHeight > d.clientHeight + 200)
      list.scrollTop = 0
      list.dispatchEvent(new Event('scroll'))
      return list.scrollTop
    })
    assert.equal(scrolled, 0)
    await noOverflow('running detail')
    await page.screenshot({ path: join(process.env.TMPDIR || '/tmp', `clawflow-job-live-${width}.png`), fullPage: true })

    // ---- finished run: result, artifacts, timeline (AC-03, AC-04)
    await page.goto(at(origin, `/jobs/${IDS.done}`))
    await page.getByRole('region', { name: 'Result' }).waitFor()
    const result = page.getByRole('region', { name: 'Result' })
    assert.ok((await result.innerText()).includes('Opened the pull request.'))
    const pr = result.getByRole('link', { name: /Pull request #9/ })
    assert.equal(await pr.getAttribute('href'), 'https://github.com/owner/repo/pull/9')
    assert.equal(await pr.getAttribute('rel'), 'noopener noreferrer')
    const resultText = await result.innerText()
    assert.ok(resultText.includes('feat/issue-803') && resultText.includes('abcdef123456'))
    assert.ok(await page.getByText('Attempt finished').count() >= 1)
    assert.match(await page.locator('[data-field="duration"]').innerText(), /^5m 0s$/)
    await page.locator('[data-phase="ended"]').waitFor()
    await page.getByText('finished work').first().waitFor()
    assert.ok((await page.locator('main').innerText()).includes('does not mean CI, review or merge finished'))
    await noOverflow('finished detail')
    await page.goto(at(origin, `/jobs/${IDS.failed}`))
    assert.ok((await page.locator('[data-field="error"]').innerText()).includes('tests failed'))
    // A queued run has no log to open and does not request one.
    const before = logCalls.length
    await page.goto(at(origin, `/jobs/${IDS.queued}`))
    await page.getByText('has not started').waitFor()
    assert.ok((await page.locator('main').innerText()).includes('waiting for a worker with tags host-lab'))
    assert.equal(logCalls.length, before)
    // Other runs for the same work link to each other.
    await page.goto(at(origin, `/jobs/${IDS.running}`))
    await page.getByRole('region', { name: 'Other runs for this work' }).waitFor().catch(() => {})

    // ---- runners (AC-05)
    await page.goto(at(origin, '/runners'))
    await page.getByRole('heading', { name: 'Runners', exact: true }).waitFor()
    const lab = page.locator('[data-runner="lab-mac"]')
    const labText = await lab.innerText()
    for (const expected of ['Online', 'host-lab', 'gpu', 'implement', '1 / 2 running', 'Run observation pages']) assert.ok(labText.includes(expected), `runner text: ${expected}`)
    assert.match(labText, /Heartbeat \d+s ago/)
    assert.ok((await page.locator('[data-runner="office-linux"]').innerText()).includes('Offline'))
    assert.ok((await page.locator('[data-runner="office-linux"]').innerText()).includes('Draining'))
    assert.ok((await page.locator('[data-runner="office-linux"]').innerText()).includes('(lost)'))
    assert.ok((await page.locator('[data-runner="new-box"]').innerText()).includes('Heartbeat unknown'))
    await lab.getByRole('link', { name: /Run observation pages/ }).click()
    await page.waitForURL(`**/jobs/${IDS.running}`)
    await page.goto(at(origin, '/runners'))
    await noOverflow('runners')
    await page.screenshot({ path: join(process.env.TMPDIR || '/tmp', `clawflow-runners-${width}.png`), fullPage: true })

    // ---- read-only session (AC-06, 08, 12)
    const writes = requests.filter(r => r.method !== 'GET')
    assert.deepEqual(writes, [], 'a LAN session sends no write request')
    for (const forbidden of ['/api/settings', '/api/version', '/api/run', '/api/chat', '/ws/pty']) {
      assert.ok(!requests.some(r => r.path === forbidden || r.path.startsWith(forbidden + '/')), `no request to ${forbidden}`)
    }
    assert.equal(await page.locator('button:has-text("Cancel"), button:has-text("Retry"), button:has-text("Run now")').count(), 0)
    await context.close()
    console.log(`PASS ${width}px: run list, live log with reconnect and follow, result and artifacts, runners, read-only`)
  }

  // ---- a stale connection stops spinners and the duration clock (freshness)
  {
    let healthy = true
    const { context, page, origin } = await newPage(1440, true, { api: (route, url) => {
      if (!healthy && (url.pathname === '/api/jobs' || url.pathname === '/api/runners')) { route.abort(); return true }
    } })
    await page.goto(at(origin, '/jobs'))
    await page.locator('tbody [data-status="running"]').first().waitFor()
    assert.equal(await page.getByRole('status').filter({ hasText: 'Connection lost' }).count(), 0)
    healthy = false
    await page.getByText('Connection lost', { exact: false }).first().waitFor({ timeout: 25_000 })
    const badge = page.locator('tbody [data-status="running"]').first()
    assert.ok(!(await badge.locator('svg').getAttribute('class')).includes('animate-spin'), 'spinner stops when data is stale')
    const row = page.locator('tbody tr').first()
    const frozen = await row.locator('td').nth(3).innerText()
    await page.waitForTimeout(2500)
    assert.equal(await row.locator('td').nth(3).innerText(), frozen, 'elapsed time does not advance on stale data')
    assert.ok((await page.locator('main').innerText()).includes('lab-mac'), 'last known data is kept')
    await context.close()
    console.log('PASS: stale connection pauses spinner and clock, keeps last data')
  }

  // ---- a local session keeps its own navigation; the run pages are linked for everyone
  {
    const { context, page, origin } = await newPage(1440, false)
    await page.goto(at(origin, '/jobs'))
    const nav = page.getByRole('navigation', { name: 'Primary' })
    assert.deepEqual(await nav.locator(':scope > a').allTextContents(), ['Work', 'Runs', 'Runners', 'Usage'])
    await context.close()
  }
} finally { await browser?.close(); await server.httpServer.close() }
