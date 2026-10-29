// Build first. PLAYWRIGHT_MODULE may point to an external Playwright installation.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0 } })
let browser
const now = new Date().toISOString()
const execution = profile => ({ profile, status: 'running', provenance: { source: 'offload_live', stale: false } })
const item = (issue, fields = {}) => ({ issue, title: `Fixture ${issue}`, state: 'open', stage: 'queued', why: 'Fixture', evidence: [], ...fields })
const board = {
  generated_at: now, stale: false, refreshing: false, events: [],
  runtime: { version: 'fixture-binary', scheduler: { status: 'idle' }, execution: { ready: true, profile: 'last-profile' } },
  sources: [{ source: 'delivery', last_success: now, stale: false }, { source: 'execution', last_success: now, stale: false }],
  repos: [{ repo: 'fixture/repo', enabled: true, max_in_flight: 2, items: [
    item(1, { executions: [execution('alpha')] }), item(2, { executions: [execution('beta')] }),
    item(3), item(4, { attention: ['Review needed'] }),
    item(5, { stage: 'merged', state: 'closed', delivery: { pr: 5, merged_at: now } }),
  ] }],
}
try {
  browser = await chromium.launch({ headless: true })
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } })
    const page = await context.newPage()
    const origin = new URL(server.resolvedUrls.local[0])
    let release
    let arrived
    let arrival = new Promise(resolve => { arrived = resolve })
    let snapshot = board
    let hold = true
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.origin !== origin.origin) return route.abort()
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
      if (url.pathname === '/api/pipeline') {
        if (hold) await new Promise(resolve => { release = resolve; arrived() })
        return route.fulfill({ json: snapshot })
      }
      const data = { '/api/session': { readonly: true }, '/data/repos.json': [] }[url.pathname]
      return route.fulfill({ status: data ? 200 : 404, json: data || {} })
    })
    await page.goto(new URL('/pipeline', origin).href)
    const summary = page.getByRole('region', { name: 'Work summary' })
    await summary.getByText('Loading work…', { exact: false }).waitFor()
    assert.equal(await summary.getByRole('button').first().isDisabled(), true)
    assert.ok(!(await summary.textContent()).includes('Running0'))
    await arrival
    hold = false
    release()
    await summary.getByRole('button', { name: 'running 2', exact: true }).waitFor()
    const status = page.locator('details').filter({ has: page.locator('summary', { hasText: 'System status' }) })
    assert.equal(await status.getAttribute('open'), null)
    assert.equal(await page.getByText('fixture-binary', { exact: true }).isVisible(), false)
    for (const [filter, count] of [['running', 2], ['waiting', 1], ['attention', 1], ['delivered', 1]]) {
      await summary.getByRole('button', { name: `${filter} ${count}`, exact: true }).click()
      await page.waitForURL(url => url.searchParams.get('work') === filter)
      await page.waitForFunction(n => document.querySelectorAll('[data-stage]').length === n, count)
      assert.equal(await page.locator('[data-stage]').count(), count)
    }
    await status.locator('summary').click()
    assert.ok((await status.textContent()).includes('one execution only'))
    await status.locator('summary').click()
    const before = await summary.boundingBox()
    hold = true
    arrival = new Promise(resolve => { arrived = resolve })
    await page.getByRole('button', { name: 'Refresh pipeline' }).click()
    await summary.getByText('Refreshing; keeping the current snapshot.', { exact: false }).waitFor()
    assert.ok((await summary.textContent()).includes('running2'))
    assert.deepEqual(await summary.boundingBox(), before)
    await arrival
    snapshot = { ...board, stale: true, sources: [{ source: 'execution', last_success: null, stale: true }] }
    hold = false
    release()
    await summary.getByText('Partial or stale snapshot', { exact: false }).waitFor()
    assert.deepEqual(await summary.boundingBox(), before)
    snapshot = { ...board, repos: [] }
    await page.getByRole('button', { name: 'Refresh pipeline' }).click()
    await summary.getByText('No work in the available snapshot.', { exact: false }).waitFor()
    assert.equal(await summary.getByRole('button', { name: 'running 0', exact: true }).count(), 1)
    assert.deepEqual(await summary.boundingBox(), before)
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
    await context.close()
  }
  console.log('PASS: Work summary, filters, loading, mixed profiles, stale/empty refresh and stable layout at 1440/390px')
} finally {
  await browser?.close()
  await server.httpServer.close()
}
