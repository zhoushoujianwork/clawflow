// Isolated read-only API fixtures. Never connects to providers or desktop apps.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
import { scheduledChecksFixture } from './scheduled-checks-fixture.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0 } })
const origin = new URL(server.resolvedUrls.local[0])
const now = new Date().toISOString()
const board = { repos: [], generated_at: now, events: [], runtime: { scheduler: { status: 'idle' }, execution: { ready: true } } }
let browser
try {
  browser = await chromium.launch({ headless: true })
  for (const width of [1440, 390]) {
    for (const kind of ['none', 'healthy', 'overlap', 'disabled', 'stale']) {
      const context = await browser.newContext({ viewport: { width, height: 1000 } })
      const page = await context.newPage()
      const writes = []
      await page.route('**/*', route => {
        const request = route.request()
        const url = new URL(request.url())
        if (request.method() !== 'GET') writes.push(request.method())
        if (url.origin !== origin.origin) return route.abort()
        if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
        const data = { '/api/pipeline': board, '/api/session': { readonly: true }, '/data/repos.json': [], '/api/pilot/schedules': scheduledChecksFixture(kind) }[url.pathname]
        return route.fulfill({ status: data ? 200 : 404, json: data || {} })
      })
      await page.goto(new URL('/pipeline', origin).href)
      await page.getByRole('heading', { name: 'Needs attention (1)', exact: true }).waitFor({ state: kind === 'disabled' ? 'visible' : 'hidden' })
      // Wait for the fixture request and React update even for the hidden panel.
      if (kind === 'none') {
        await page.getByText('Nothing needs you', { exact: true }).waitFor()
        assert.equal(await page.getByRole('region', { name: 'Scheduled observations', exact: true }).count(), 0)
      } else {
        const panel = page.getByRole('region', { name: 'Scheduled observations', exact: true })
        await panel.waitFor()
        assert.equal(await panel.locator('details[open]').count(), 0)
        assert.equal(await panel.getByText('Manage with Steward / administration').count(), 0)
        if (kind === 'disabled') {
          const attention = page.getByRole('region', { name: 'Needs attention', exact: true })
          assert.equal(await attention.locator('[data-incident-key]').count(), 1)
          await attention.getByRole('link', { name: 'Latest result', exact: true }).click()
        } else {
          await panel.getByRole('link', { name: 'Latest result', exact: true }).click()
        }
        assert.equal(await panel.locator('details[open]').count(), 1)
        if (kind === 'overlap') await panel.getByText('previous invocation still running', { exact: true }).waitFor()
        if (kind === 'stale') assert.ok((await panel.innerText()).includes('Stale result'))
        assert.ok(!(await page.locator('body').innerText()).includes('PRIVATE_'))
        assert.ok(await panel.evaluate(el => el.scrollWidth <= el.clientWidth))
      }
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
      assert.deepEqual(writes, [])
      await context.close()
    }
  }
  console.log('PASS: scheduled observation fixtures, result navigation, deduplicated attention, zero writes, read-only access and 1440/390px layout')
} finally {
  await browser?.close()
  await server.httpServer.close()
}
