// Headless render check for the stopped-repository history and read-only run details.
// Driven by scripts/check-readonly-history.sh, which owns the isolated fixture:
// a non-loopback address (so the server treats the browser as a read-only LAN
// client), a disabled GitLab repository with one failed attempt, and a mock
// provider that records every request. No provider, desktop or real data is used.
//   BASE_URL           server address on a non-loopback interface
//   CHROME             Chrome/Chromium executable
//   PLAYWRIGHT_MODULE  module name or path of playwright(-core)
//   OUT                directory for screenshots
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const base = process.env.BASE_URL, OUT = process.env.OUT || '.'
const browser = await chromium.launch({ executablePath: process.env.CHROME, headless: true })
const out = []
const note = (ok, what) => { out.push(`${ok ? 'PASS' : 'FAIL'} ${what}`); if (!ok) process.exitCode = 1 }
async function open(width = 1280) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 } })
  const page = await ctx.newPage()
  const calls = []
  page.on('request', r => { const u = new URL(r.url()); calls.push(`${r.method()} ${u.pathname}`) })
  return { ctx, page, calls }
}
// 1. Work page: the stopped repository, its issue and its failure are visible.
{
  const { ctx, page, calls } = await open()
  await page.goto(base + '/pipeline', { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-work-key], text=No repository', { timeout: 15000 }).catch(() => {})
  await page.screenshot({ path: `${OUT}/work-active.png`, fullPage: true })
  const body = await page.innerText('body')
  note(/Read-only/.test(body), 'Work: session shown as Read-only')
  note(/1 repository has automation off/.test(body) && /nothing is being processed/.test(body), 'Work: stopped notice explains automation is off')
  note(/grp\/smoke/.test(body), 'Work: registered repository is listed')
  note(!/No repository is (tracked|registered) yet/.test(body), 'Work: does not claim there is no repository')
  note(await page.locator('[data-work-key]').count() === 0, 'Work: Active tab does not present stopped items as live work')
  await page.getByRole('link', { name: 'View history' }).click()
  await page.waitForSelector('[data-work-key]', { timeout: 15000 })
  await page.screenshot({ path: `${OUT}/work-stopped.png`, fullPage: true })
  const row = await page.locator('[data-work-key]').first().innerText()
  note(/Greeting: add a name argument/.test(row), 'Stopped view: issue title visible')
  note(/Automation off/.test(row), 'Stopped view: row marked "Automation off"')
  note(/Failed/.test(row), 'Stopped view: failed stage visible')
  note(/Automation is off for this repository/.test(row), 'Stopped view: reason says automation is off')
  note(await page.locator('[data-work-key] a[href$="/grp/smoke/issues/1"]').count() === 1, 'Stopped view: issue links to the provider /issues/ path')
  // open details dialog -> attempt -> run detail
  await page.getByRole('button', { name: /Details/ }).click()
  await page.waitForSelector('dialog[open] a:has-text("Open existing run details")', { timeout: 15000 })
  await page.screenshot({ path: `${OUT}/work-detail.png`, fullPage: true })
  const dlg = await page.locator('dialog[open]').innerText()
  note(/error: execution/.test(dlg) && /Exit: 1/.test(dlg), 'Work details: failed attempt shows exit code and error kind')
  await Promise.all([page.waitForURL(/\/runs\//), page.locator('dialog[open] a:has-text("Open existing run details")').click()])
  await page.waitForSelector('text=Conclusion', { timeout: 15000 })
  await page.screenshot({ path: `${OUT}/run-detail.png`, fullPage: true })
  const run = await page.innerText('body')
  note(!/requires local access/.test(run), 'Run detail: not blocked by "requires local access"')
  note(/usage policy/.test(run), 'Run detail: failure reason is shown')
  note(!/glpat-|LEAKME|\/Users\/alice|PRIVATE/.test(run), 'Run detail: no token, local path, body, summary or transcript text')
  note(/Read-only view/.test(run) && await page.getByRole('button', { name: /Load execution events/ }).count() === 0, 'Run detail: transcript loader not offered read-only')
  note(!calls.some(c => c.startsWith('GET /data/runs/')), 'Run detail: browser never requested the raw run files')
  note(!calls.some(c => !c.startsWith('GET ')), 'Pages: browser sent no write request')
  await ctx.close()
}
// 2. Usage -> "运行详情" is the original dead end.
{
  const { ctx, page } = await open()
  await page.goto(base + '/usage?repo=grp%2Fsmoke&period=all', { waitUntil: 'networkidle' })
  await page.waitForSelector('#run-history li', { timeout: 15000 })
  await page.screenshot({ path: `${OUT}/usage.png`, fullPage: true })
  await Promise.all([page.waitForURL(/\/runs\//), page.getByRole('link', { name: '运行详情' }).first().click()])
  await page.waitForSelector('text=Conclusion', { timeout: 15000 })
  const t = await page.innerText('body')
  note(!/requires local access/.test(t) && /usage policy/.test(t), 'Usage -> 运行详情 reaches the failure reason (was: requires local access)')
  // hard reload of the deep link, as a shared URL would be opened
  await page.reload({ waitUntil: 'networkidle' })
  await page.waitForSelector('text=Conclusion', { timeout: 15000 })
  note(/usage policy/.test(await page.innerText('body')), 'Run detail: deep link works after a hard reload')
  await ctx.close()
}
// 3. Sensitive pages and writes are still refused for the LAN client.
{
  const { ctx, page } = await open()
  const api = async (method, path, body) => page.evaluate(async ([m, p, b]) => { const r = await fetch(p, { method: m, headers: { 'content-type': 'application/json' }, body: b }); return r.status }, [method, path, body])
  await page.goto(base + '/pipeline', { waitUntil: 'networkidle' })
  for (const [m, p] of [['POST', '/api/run'], ['POST', '/api/labels/add'], ['POST', '/api/repo/config'], ['POST', '/api/project/bind'], ['GET', '/api/settings'], ['GET', '/api/settings/reveal'], ['GET', '/api/providers'], ['GET', '/api/browse-directory'], ['POST', '/api/chat/spawn'], ['GET', '/data/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z/events.jsonl'], ['GET', '/data/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z/meta.json']]) {
    const s = await api(m, p, m === 'POST' ? '{}' : undefined)
    note(s === 403, `${m} ${p} -> ${s} (want 403)`)
  }
  for (const path of ['/settings', '/repos', '/projects']) {
    const resp = await page.goto(base + path, { waitUntil: 'networkidle' })
    note(resp.status() === 403 && /not allowed/.test(await page.innerText('body')), `direct navigation to ${path} -> ${resp.status()} (server refuses, no SPA shell)`)
  }
  // In-app route change (no server request) is still stopped by the SPA guard.
  await page.goto(base + '/pipeline', { waitUntil: 'networkidle' })
  await page.evaluate(() => { window.history.pushState({}, '', '/settings'); window.dispatchEvent(new PopStateEvent('popstate')) })
  await page.waitForSelector('text=requires local access', { timeout: 5000 }).then(() => note(true, 'in-app route to /settings -> "requires local access"'), () => note(false, 'in-app route to /settings -> "requires local access"'))
  await ctx.close()
}
// 4. Narrow viewport: no horizontal overflow on the stopped view.
{
  const { ctx, page } = await open(390)
  await page.goto(base + '/pipeline?work=stopped', { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-work-key]', { timeout: 15000 })
  await page.screenshot({ path: `${OUT}/work-390.png`, fullPage: true })
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  note(over <= 1, `390px: no horizontal overflow (${over}px)`)
  await ctx.close()
}
console.log(out.join('\n'))
await browser.close()
