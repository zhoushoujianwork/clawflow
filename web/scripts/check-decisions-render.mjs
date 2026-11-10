// Isolated synthetic API fixture only. Build web first; never connects to providers.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0 } })
const now = '2026-11-10T00:00:00Z'
const base = { reason: 'Because the fixture says so.', action: 'Do the documented thing.' }
const full = { generated_at: now, decisions: [
  { id: 'a', kind: 'execution_config', title: 'Execution setup: mac-1', ...base, url: 'https://example.com/guide' },
  { id: 'b', kind: 'needs_confirmation', repo: 'o/r', issue: 11, title: 'Ambiguous scope', ...base, url: 'https://github.com/o/r/issues/11', issue_url: 'https://github.com/o/r/issues/11' },
  { id: 'c', kind: 'owner_skipped', repo: 'o/r', issue: 12, title: 'Parked by owner', ...base, url: 'https://github.com/o/r/issues/12', issue_url: 'https://github.com/o/r/issues/12' },
  { id: 'd', kind: 'acceptance_blocked', repo: 'o/r', issue: 13, pr: 130, title: 'Review blocked', ...base, url: 'https://github.com/o/r/pull/130', issue_url: 'https://github.com/o/r/issues/13' },
  { id: 'e', kind: 'repair_stopped', repo: 'o/r', issue: 14, pr: 140, title: 'Still failing', ...base, url: 'https://github.com/o/r/pull/140', issue_url: 'https://github.com/o/r/issues/14' },
] }
let decisions = full
let status = 200
const board = { repos: [], generated_at: now, stale: false, refreshing: false, events: [], runtime: { version: 'fixture', scheduler: { status: 'idle' }, execution: { ready: true } } }
let browser
try {
  browser = await chromium.launch({ headless: true })
  const origin = new URL(server.resolvedUrls.local[0])
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } })
    const page = await context.newPage()
    await page.route('**/*', route => {
      const url = new URL(route.request().url())
      if (url.origin !== origin.origin) return route.abort()
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
      if (url.pathname === '/api/decisions') return route.fulfill({ status, json: status === 200 ? decisions : { error: 'decisions source is not available' } })
      const data = { '/api/pipeline': board, '/api/session': { readonly: true }, '/data/repos.json': [] }[url.pathname]
      return route.fulfill({ status: data ? 200 : 404, json: data || {} })
    })
    // The root is the home page: it must land on the page that lists decisions.
    await page.goto(origin.href)
    const region = page.getByRole('region', { name: 'Needs your decision', exact: true })
    await region.getByRole('heading', { name: 'Needs your decision (5)', exact: true }).waitFor()
    assert.equal(await region.locator('[data-decision-id]').count(), 5)
    for (const [kind, href] of [['needs_confirmation', '/o/r/issues/11'], ['owner_skipped', '/o/r/issues/12'], ['acceptance_blocked', '/o/r/pull/130'], ['repair_stopped', '/o/r/pull/140']]) {
      const card = region.locator(`[data-decision-kind="${kind}"]`)
      assert.equal(await card.count(), 1, kind)
      assert.ok((await card.getByRole('link').first().getAttribute('href')).endsWith(href), kind)
      assert.equal(await card.getByText('Do the documented thing.').count(), 1, kind)
    }
    assert.ok(await region.evaluate(el => el.scrollWidth <= el.clientWidth))
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
    await context.close()
  }
  // Nothing to decide: explicit empty state.
  decisions = { generated_at: now, decisions: [] }
  let page = await browser.newPage()
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin !== origin.origin) return route.abort()
    if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
    const data = url.pathname === '/api/decisions' ? decisions : { '/api/pipeline': board, '/api/session': { readonly: true }, '/data/repos.json': [] }[url.pathname]
    return route.fulfill({ status: data ? 200 : 404, json: data || {} })
  })
  await page.goto(new URL('/pipeline', origin).href)
  await page.getByText('Nothing needs your decision', { exact: true }).waitFor()
  assert.equal(await page.locator('[data-decision-id]').count(), 0)
  await page.close()
  // Endpoint failure: shown as an error, never as an empty list.
  status = 503
  page = await browser.newPage()
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin !== origin.origin) return route.abort()
    if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
    if (url.pathname === '/api/decisions') return route.fulfill({ status, json: { error: 'decisions source is not available' } })
    const data = { '/api/pipeline': board, '/api/session': { readonly: true }, '/data/repos.json': [] }[url.pathname]
    return route.fulfill({ status: data ? 200 : 404, json: data || {} })
  })
  await page.goto(new URL('/pipeline', origin).href)
  await page.getByText('Could not refresh decisions: decisions source is not available', { exact: false }).waitFor()
  assert.equal(await page.getByText('Nothing needs your decision').count(), 0)
  console.log('PASS: decisions list on the home page: all kinds, links, empty state and failure at 1440/390px')
} finally {
  await browser?.close()
  await server.httpServer.close()
}
