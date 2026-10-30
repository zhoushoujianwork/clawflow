// Build first. Uses only intercepted synthetic API responses and headless Chromium.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
import { workListFixture } from './work-list-fixture.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0 } })
const { repos, queue, now } = workListFixture()
const board = { repos, global_queue: queue, generated_at: now, stale: false, refreshing: false, events: [],
  runtime: { version: 'fixture', scheduler: { status: 'idle' }, execution: { ready: true } },
  sources: [{ source: 'execution', last_success: now, stale: false }, { source: 'delivery', last_success: now, stale: false }] }
let browser
try {
  browser = await chromium.launch({ headless: true })
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } })
    const page = await context.newPage()
    const origin = new URL(server.resolvedUrls.local[0])
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.origin !== origin.origin) return route.abort()
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
      const data = { '/api/pipeline': board, '/api/session': { readonly: true }, '/data/repos.json': [] }[url.pathname]
      return route.fulfill({ status: data ? 200 : 404, json: data || {} })
    })
    await page.goto(new URL('/pipeline', origin).href)
    const list = page.getByRole('region', { name: 'Work list', exact: true })
    await list.getByText('1–50 of 400 items', { exact: true }).waitFor()
    assert.equal(await list.locator('[data-work-key]').count(), 50)
    assert.equal(await list.locator('h2').count(), 0)
    assert.equal(await list.getByRole('link', { name: /Task 49-0:/ }).isVisible(), true)
    // All active tasks appear without expanding repository cards.
    assert.equal(await list.locator('[data-stage="implementing"]').count(), 50)
    const firstKeys = await list.locator('[data-work-key]').evaluateAll(nodes => nodes.map(n => n.dataset.workKey))
    assert.equal(new Set(firstKeys).size, 50)
    await list.getByRole('button', { name: 'Next', exact: true }).click()
    await list.getByText('51–100 of 400 items', { exact: true }).waitFor()
    const nextKeys = await list.locator('[data-work-key]').evaluateAll(nodes => nodes.map(n => n.dataset.workKey))
    assert.ok(nextKeys.every(key => !firstKeys.includes(key)))
    await page.getByRole('tab', { name: 'Waiting', exact: true }).click()
    await list.getByText('1–50 of 300 items', { exact: true }).waitFor()
    await list.getByRole('checkbox', { name: 'Group by repository' }).check()
    assert.equal(await list.locator('h2').count(), 50)
    assert.equal(await list.locator('[data-work-key]').count(), 50)
    await list.getByRole('checkbox', { name: 'Group by repository' }).uncheck()
    for (const reason of ['Waiting for CI', 'Waiting for independent acceptance', 'Waiting for dependencies', 'Waiting for repository capacity']) {
      await list.getByRole('button', { name: 'Next', exact: true }).click()
      await list.locator('[data-work-reason]').filter({ hasText: reason }).first().waitFor()
    }
    assert.ok((await list.textContent()).includes('Automation disabled'))
    await page.getByRole('tab', { name: 'Delivered', exact: true }).click()
    await list.getByText('1–50 of 50 items', { exact: true }).waitFor()
    assert.equal(await list.locator('[data-stage="merged"]').count(), 50)
    assert.equal(await list.getByText('Execution ended; delivery is not confirmed', { exact: false }).count(), 0)
    await page.getByRole('tab', { name: 'Running', exact: true }).click()
    await list.getByText('1–50 of 50 items', { exact: true }).waitFor()
    const open = list.getByRole('button', { name: 'Details · 2 execution attempts', exact: true }).first()
    await open.click()
    const panel = page.getByRole('dialog')
    await panel.waitFor()
    assert.equal(await panel.locator('[data-attempt-id]').count(), 2)
    // Both recorded attempts remain visible in the new detail panel.
    // Which repository's row is first depends on list ordering, which this check does not assert.
    const ids = await panel.locator('[data-attempt-id]').evaluateAll(nodes => nodes.map(n => n.dataset.attemptId))
    assert.match(ids[0], /^\d+-a$/)
    assert.deepEqual(ids, [ids[0], ids[0].replace(/-a$/, '-b')])
    await panel.getByRole('button', { name: 'Close details', exact: true }).click()
    await panel.waitFor({ state: 'hidden' })
    assert.equal(await open.evaluate(el => el === document.activeElement), true)
    const reason = list.locator('[data-work-reason]').first()
    assert.equal(await reason.isVisible(), true)
    assert.equal(await reason.evaluate(el => getComputedStyle(el).whiteSpace), 'normal')
    if (width === 390) {
      assert.ok(await reason.evaluate(el => el.clientHeight > parseFloat(getComputedStyle(el).lineHeight)))
    }
    assert.ok(await list.evaluate(el => el.scrollWidth <= el.clientWidth))
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
    await context.close()
  }
  console.log('PASS: 50 repositories / 500 items, tabs, grouping, pagination, waiting reasons, attempt details and wrapping at 1440/390px')
} finally {
  await browser?.close()
  await server.httpServer.close()
}
