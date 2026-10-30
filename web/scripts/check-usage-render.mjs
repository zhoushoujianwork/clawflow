// Headless regression checks for nullable SQL usage payloads. No browser required.
import assert from 'node:assert/strict'
import { createServer } from 'vite'
import react from '@vitejs/plugin-react'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

const unknown = {
  runs: 1, total_cost_usd: null, total_tokens: null, input_tokens: null, output_tokens: null,
  cache_read_input_tokens: null, cache_creation_input_tokens: null,
  coverage: { cost: 0, tokens: 0, input: 0, output: 0, cache_read: 0, cache_write: 0 },
  estimated_cost_runs: 0, reported_cost_runs: 0, cost_provenance_unknown_runs: 0,
}
const totals = {
  ...unknown, runs: 6, total_cost_usd: 3, total_tokens: 387, input_tokens: 137, output_tokens: 17,
  cache_read_input_tokens: 200, cache_creation_input_tokens: 40, subscription_runs: 1,
  coverage: { cost: 3, tokens: 4, input: 5, output: 4, cache_read: 4, cache_write: 4 },
  estimated_cost_runs: 1, reported_cost_runs: 1, cost_provenance_unknown_runs: 1,
}
const long = 'long-attributed-name-'.repeat(8)
const claude = { ...unknown, total_cost_usd: 2, total_tokens: 180, input_tokens: 100, output_tokens: 10, cache_read_input_tokens: 50, cache_creation_input_tokens: 20,
  coverage: { cost: 1, tokens: 1, input: 1, output: 1, cache_read: 1, cache_write: 1 }, cost_provenance_unknown_runs: 1 }
const codex = { ...unknown, total_cost_usd: 1, total_tokens: 105, input_tokens: 20, output_tokens: 5, cache_read_input_tokens: 70, cache_creation_input_tokens: 10,
  coverage: { cost: 1, tokens: 1, input: 1, output: 1, cache_read: 1, cache_write: 1 }, estimated_cost_runs: 1 }
const resumed = { ...unknown, subscription_runs: 1, total_tokens: 102, input_tokens: 10, output_tokens: 2, cache_read_input_tokens: 80, cache_creation_input_tokens: 10,
  coverage: { cost: 0, tokens: 1, input: 1, output: 1, cache_read: 1, cache_write: 1 } }
const legacy = { ...unknown }
const partial = { ...unknown, input_tokens: 7, coverage: { ...unknown.coverage, input: 1 } }
const zero = { ...claude, total_cost_usd: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, cost_provenance_unknown_runs: 0, reported_cost_runs: 1 }
function aggregate(rows) {
  const a = { ...unknown, runs: 0, coverage: { ...unknown.coverage } }
  for (const r of rows) {
    a.runs += r.runs
    for (const key of ['total_cost_usd', 'total_tokens', 'input_tokens', 'output_tokens', 'cache_read_input_tokens', 'cache_creation_input_tokens']) {
      if (r[key] != null) a[key] = (a[key] ?? 0) + r[key]
    }
    for (const key of Object.keys(a.coverage)) a.coverage[key] += r.coverage[key]
    for (const key of ['estimated_cost_runs', 'reported_cost_runs', 'cost_provenance_unknown_runs', 'subscription_runs']) a[key] = (a[key] ?? 0) + (r[key] ?? 0)
  }
  return a
}
assert.deepEqual(aggregate([claude, codex, resumed, legacy, partial, zero]), totals)
const groups = {
  by_project: { one: aggregate([claude, codex, resumed]), unknown: aggregate([legacy, partial, zero]) },
  by_repo: { 'o/r': aggregate([claude, codex, resumed, partial, zero]), [long]: legacy },
  by_role: { implement: claude, acceptance: codex, unknown: aggregate([resumed, legacy, partial, zero]) },
  by_profile: { codex, unknown: aggregate([claude, resumed, legacy, partial, zero]) },
  by_model: { gpt: aggregate([codex, resumed]), claude, unknown: aggregate([legacy, partial, zero]) },
  by_machine: { m: aggregate([claude, codex, resumed, partial, zero]), [long]: legacy }, by_operator: {},
}
const fixture = {
  ...groups, totals, generated_at: '2026-10-16T10:00:00Z',
  range_start: '2026-10-15T00:00:00Z', range_end: '2026-11-15T00:00:00Z',
  periods: [{ ...groups, totals, period_start: '2026-10-15', period_end: '2026-11-14', daily_trend: [
    { ...aggregate([claude, codex, partial, zero]), date: '2026-10-16' }, { ...legacy, date: '2026-10-17' },
    { ...resumed, date: '2026-10-18' },
  ] }], history_limit: 50,
  history: [{ started_at: '2026-10-16T00:00:00Z', repo: 'o/r', project: 'one', operator: 'implement', status: 'success', href: '/runs/o__r/issue-545/attempt' }],
}

const server = await createServer({ configFile: false, plugins: [react()], optimizeDeps: { noDiscovery: true, entries: [] }, server: { middlewareMode: true }, appType: 'custom' })
try {
  const { fmtCost, UsageReport, usageQuery, coverage } = await server.ssrLoadModule('/src/routes/_app.usage.tsx')
  assert.equal(fmtCost(null, 1), 'Subscription')
  assert.equal(fmtCost(null, 0), 'Not reported')
  assert.equal(fmtCost(0, 0), '$0.0000')
  assert.equal(fmtCost(1.25, 1), '$1.2500 + subscription')
  assert.equal(coverage(undefined, 6), '覆盖率未报告')
  assert.equal(coverage(3, 6), '3/6 次报告 · 3 次缺失')
  const query = new URLSearchParams(usageQuery({ repo: 'o/r', project: 'one', role: 'acceptance', profile: 'codex', machine: 'm', model: 'gpt' }, 'current', '2026-10-16'))
  assert.equal(query.get('until'), '2026-10-17T00:00:00.000Z')
  assert.equal(query.get('from'), '2026-10-16')
  assert.equal(query.get('period'), null)
  for (const key of ['repo', 'project', 'role', 'profile', 'machine', 'model']) assert.ok(query.has(key))
  assert.equal(new URLSearchParams(usageQuery({}, '2026-10-15', '')).get('period'), '2026-10-15')
  const report = renderToStaticMarkup(createElement(UsageReport, { summary: fixture, onFilter() {}, onDay() {} }))
  assert.match(report, /6 次已结束尝试/)
  assert.match(report, /3\/6 次报告 · 3 次缺失/)
  assert.match(report, /4\/6 次报告 · 2 次缺失/)
  assert.match(report, /387/)
  assert.match(report, /1 次明确估算/)
  assert.match(report, /Subscription/)
  assert.match(report, /Not reported/)
  assert.match(report, /href="#run-history"/)
  assert.match(report, /href="\/runs\/o__r\/issue-545\/attempt"/)
  assert.match(report, /border-dashed/)
  assert.doesNotMatch(report, /NaN|Infinity|secret-prompt|secret-result/)
  const missing = renderToStaticMarkup(createElement(UsageReport, { summary: { ...fixture, totals: unknown }, onFilter() {}, onDay() {} }))
  assert.doesNotMatch(missing.split('每日已报告费用')[0], /\$0\.0000/)
  console.log('Usage report: mixed coverage, provenance, cache-inclusive total, filters, nullable chart and history passed')
  const { UsagePanel } = await server.ssrLoadModule('/src/routes/_app.runs.$slug.$issue.$ts.tsx')
  const { PilotRunDetailModal } = await server.ssrLoadModule('/src/components/PilotRun.tsx')
  const base = { status: 'success', started_at: '2026-10-28T00:00:00Z', project: 'project' }
  const tokens = {
    total_cost_usd: null, input_tokens: 37, output_tokens: null,
    cache_read_input_tokens: null, cache_creation_input_tokens: null,
    duration_ms: 100, num_turns: 1,
    model_usage: { unknown: { cost_usd: null, input_tokens: 37, output_tokens: null } },
  }
  for (const usage of [undefined, tokens]) {
    const meta = { ...base, usage }
    const runHTML = renderToStaticMarkup(createElement(UsagePanel, { meta }))
    const pilotHTML = renderToStaticMarkup(createElement(PilotRunDetailModal, { run: meta, onClose() {} }))
    assert.match(runHTML, /Not reported/)
    assert.match(pilotHTML, /Not reported/)
    assert.doesNotMatch(runHTML + pilotHTML, /NaN|\$0\.00/)
    if (usage) assert.match(runHTML, /37/)
  }
  const reported = renderToStaticMarkup(createElement(UsagePanel, {
    meta: { ...base, usage: { ...tokens, total_cost_usd: 0 } },
  }))
  assert.match(reported, /\$0\.0000/)
  console.log('Usage and Pilot rendering: nil usage, cost-less tokens, and measured zero passed')
} finally {
  await server.close()
}

// Optional isolated Chromium check. All network/data are synthetic; no desktop UI.
// PLAYWRIGHT_MODULE=/tmp/.../playwright/index.mjs USAGE_BROWSER=1 pnpm --dir web exec node scripts/check-usage-render.mjs
if (process.env.USAGE_BROWSER === '1') {
  const { preview } = await import('vite')
  const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
  const previewServer = await preview({ configFile: false, preview: { host: '127.0.0.1', port: 0 } })
  const origin = previewServer.resolvedUrls.local[0]
  let browser
  try {
    browser = await chromium.launch({ headless: true })
    for (const width of [1440, 390]) {
      for (const readonly of [false, true]) {
        const context = await browser.newContext({ viewport: { width, height: 1000 } })
        const page = await context.newPage()
        const queries = [], errors = []
        page.on('pageerror', e => errors.push(e.message))
        await page.route('**/*', route => {
          const url = new URL(route.request().url())
          if (url.origin !== new URL(origin).origin) return route.abort()
          if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
          assert.equal(route.request().method(), 'GET')
          const data = {
            '/api/session': { readonly }, '/api/version': {}, '/data/repos.json': [],
            '/api/usage': fixture,
          }[url.pathname]
          if (url.pathname === '/api/usage') queries.push(url.searchParams)
          return route.fulfill({ status: data === undefined ? 404 : 200, json: data ?? {} })
        })
        await page.goto(new URL('/usage?period=all&repo=o%2Fr', origin).href)
        await page.getByText('6 次已结束尝试', { exact: true }).waitFor()
        const noOverflow = async () => assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${width}/${readonly}: viewport overflow`)
        await noOverflow()
        for (const [label, key, value] of [
          ['项目', 'project', 'one'], ['角色', 'role', 'acceptance'], ['配置', 'profile', 'codex'],
          ['模型', 'model', 'gpt'], ['机器', 'machine', 'm'],
        ]) {
          await page.getByRole('combobox', { name: label, exact: true }).selectOption(value)
          await page.getByText('6 次已结束尝试', { exact: true }).waitFor()
          assert.ok(queries.some(q => q.get(key) === value), key)
        }
        await page.getByRole('link', { name: '查看筛选后的运行历史' }).click()
        assert.equal(new URL(page.url()).hash, '#run-history')
        assert.equal(await page.getByRole('link', { name: '运行详情', exact: true }).getAttribute('href'), '/runs/o__r/issue-545/attempt')
        await page.getByRole('button', { name: /^2026-10-16 / }).click()
        await page.getByText('6 次已结束尝试', { exact: true }).waitFor()
        assert.ok(queries.some(q => q.get('from') === '2026-10-16' && q.get('until') === '2026-10-17T00:00:00.000Z'))
        await noOverflow()
        assert.deepEqual(errors, [])
        await context.close()
      }
    }
    console.log('Usage browser: 1440/390px, local/LAN read-only, filters, daily selection and history links passed')
  } finally {
    if (browser) await browser.close()
    await new Promise(resolve => previewServer.httpServer.close(resolve))
  }
}
