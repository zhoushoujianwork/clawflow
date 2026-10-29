// Build first: pnpm --dir web build
// Run with Playwright installed externally if needed:
// PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node web/scripts/check-shell-render.mjs
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0 } })
let browser
const origin = server.resolvedUrls.local[0]
const screenshots = process.env.SHELL_SCREENSHOTS
if (screenshots) await mkdir(screenshots, { recursive: true })
const repo = 'example/' + 'long-repository-name-'.repeat(5)
const title = 'Keep current work readable with wrapping labels and a long issue title '.repeat(3)
const board = {
  generated_at: new Date().toISOString(),
  runtime: { version: 'fixture', scheduler: { status: 'idle', agent_slots_used: 0, agent_slots_total: 2 }, execution: { ready: true, profile: 'fixture' } },
  repos: [{
    repo, enabled: true, auto_merge: false, ci_required: true, acceptance_required: true, max_in_flight: 2,
    items: [{ issue: 537, title, stage: 'queued', state: 'open', why: 'Waiting for the next pass', evidence: [], attention: ['Awaiting review'] }],
  }],
  events: [],
}
try {
  browser = await chromium.launch({ headless: true, channel: process.env.PLAYWRIGHT_CHANNEL })
  for (const width of [1440, 390]) {
    for (const readonly of [false, true]) {
      const context = await browser.newContext({ viewport: { width, height: 1000 } })
      const page = await context.newPage()
      const errors = []
      const requests = []
      page.on('pageerror', error => errors.push(error.message))
      // Every API/data response is synthetic, and all external traffic is blocked.
      await page.route('**/*', route => {
        const url = new URL(route.request().url())
        if (url.origin !== new URL(origin).origin) return route.abort()
        if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
        requests.push({ path: url.pathname, method: route.request().method() })
        const data = {
          '/api/session': { readonly },
          '/api/version': {
            current: 'v0.75.0-' + 'very-long-source-branch-'.repeat(8),
            latest: 'v0.76.0', channel: 'source', warning: '3 commits behind main',
            update_available: true, can_upgrade: true,
          },
          '/api/pipeline': board,
          '/api/usage': { totals: { runs: 0 }, periods: [] },
          '/data/repos.json': [],
        }[url.pathname]
        return route.fulfill({ status: data === undefined ? 404 : 200, json: data ?? {} })
      })
      const noOverflow = async () => {
        const dimensions = await page.evaluate(() => ({ content: document.documentElement.scrollWidth, viewport: innerWidth }))
        assert.ok(dimensions.content <= dimensions.viewport, JSON.stringify({ width, readonly, ...dimensions }))
      }
      await page.goto(new URL('/pipeline', origin).href)
      await page.getByRole('heading', { name: 'Work' }).waitFor()
      await page.getByRole('region', { name: 'Work list', exact: true }).getByRole('link', { name: title }).waitFor()
      if (!readonly) await page.getByRole('button', { name: '与平台 steward 对话' }).waitFor()
      const nav = page.getByRole('navigation', { name: 'Primary' })
      const work = nav.getByRole('link', { name: 'Work', exact: true })
      assert.equal(await work.getAttribute('aria-current'), 'page')
      assert.deepEqual(await nav.locator(':scope > a').allTextContents(), ['Work', 'Usage'])
      assert.equal(await nav.getByText('Management', { exact: true }).count(), readonly ? 0 : 1)
      assert.equal(await page.getByRole('button', { name: '与平台 steward 对话' }).count(), readonly ? 0 : 1)
      if (readonly) {
        assert.equal(requests.filter(r => r.path === '/api/version').length, 0)
        assert.equal(await page.locator('header a[href="/settings"], header a[href="/repos"], header a[href="/projects"], header a[href="/operators"]').count(), 0)
      }
      await noOverflow()
      const metrics = await page.locator('main > div').evaluate(el => ({
        width: el.getBoundingClientRect().width,
        title: parseFloat(getComputedStyle(el.querySelector('h1')).fontSize),
        body: parseFloat(getComputedStyle(el.querySelector('p')).fontSize),
      }))
      assert.ok(metrics.width >= width - 2, JSON.stringify(metrics))
      assert.ok(metrics.title >= 24 && metrics.body >= 13, JSON.stringify(metrics))
      const label = page.getByRole('region', { name: 'Work list', exact: true }).getByRole('link', { name: title })
      assert.equal(await label.evaluate(el => getComputedStyle(el).whiteSpace), 'normal')
      assert.ok(await label.evaluate(el => el.getBoundingClientRect().height > parseFloat(getComputedStyle(el).lineHeight)))
      await noOverflow()
      // Tab from the beginning: skip link, then content target, then navigation.
      await page.goto(new URL('/pipeline', origin).href)
      await page.getByRole('heading', { name: 'Work' }).waitFor()
      await page.keyboard.press('Tab')
      assert.equal(await page.locator(':focus').textContent(), 'Skip to content')
      assert.equal(await page.locator(':focus').evaluate(el => getComputedStyle(el).outlineStyle), 'solid')
      await page.keyboard.press('Enter')
      assert.equal(await page.locator(':focus').getAttribute('id'), 'main-content')
      const administration = nav.locator('summary', { hasText: 'Administration' })
      await administration.focus()
      await page.keyboard.press('Enter')
      await nav.getByRole('link', { name: 'Dashboard', exact: true }).waitFor({ state: 'visible' })
      await noOverflow()
      await page.keyboard.press('Escape')
      assert.equal(await page.locator(':focus').textContent(), 'Administration')
      assert.equal(await administration.evaluate(el => el.parentElement.open), false)
      if (!readonly) {
        const management = nav.locator('summary', { hasText: 'Management' })
        await management.focus()
        await page.keyboard.press('Enter')
        await nav.getByRole('link', { name: 'Repositories', exact: true }).waitFor({ state: 'visible' })
        await noOverflow()
        await page.keyboard.press('Escape')
      }
      await nav.getByRole('link', { name: 'Usage', exact: true }).click()
      await page.getByRole('heading', { name: 'Usage & billing' }).waitFor()
      assert.equal(await nav.getByRole('link', { name: 'Usage', exact: true }).getAttribute('aria-current'), 'page')
      await noOverflow()
      if (readonly) {
        for (const path of ['/settings', '/repos', '/projects', '/operators', '/runs/test/1/now']) {
          await page.goto(new URL(path, origin).href)
          await page.getByText('This page requires local access.', { exact: false }).waitFor()
          await page.getByRole('link', { name: 'Open Work', exact: true }).click()
          await page.getByRole('heading', { name: 'Work' }).waitFor()
        }
      }
      await page.goto(new URL('/pipeline', origin).href)
      if (!readonly) await page.getByRole('button', { name: '与平台 steward 对话' }).waitFor()
      await noOverflow()
      if (screenshots) await page.screenshot({ path: join(screenshots, `shell-${width}-${readonly ? 'lan' : 'local'}.png`), fullPage: true })
      assert.deepEqual(errors, [])
      assert.equal(requests.filter(r => r.method !== 'GET').length, 0)
      console.log(`PASS ${width}px ${readonly ? 'LAN' : 'local'}: width, wrapping, type, navigation, focus, access`)
      await context.close()
    }
  }
} finally {
  await browser?.close()
  await new Promise(resolve => server.httpServer.close(resolve))
}
