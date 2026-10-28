// Headless regression checks for nullable SQL usage payloads. No browser required.
import assert from 'node:assert/strict'
import { createServer } from 'vite'
import react from '@vitejs/plugin-react'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

const server = await createServer({ configFile: false, plugins: [react()], optimizeDeps: { noDiscovery: true, entries: [] }, server: { middlewareMode: true }, appType: 'custom' })
try {
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
