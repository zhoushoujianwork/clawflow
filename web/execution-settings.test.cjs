const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const Module = require('node:module')
const ts = require('typescript')
const React = require('react')
const { renderToStaticMarkup: render } = require('react-dom/server')

function load(relative, overrides = {}) {
  const filename = path.join(__dirname, relative)
  const component = new Module(filename, module)
  component.filename = filename
  component.paths = module.paths
  const nativeRequire = component.require.bind(component)
  component.require = name => overrides[name] || nativeRequire(name)
  component._compile(ts.transpileModule(fs.readFileSync(filename, 'utf8'), {
    compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS },
  }).outputText, filename)
  return component.exports
}
const section = 'src/components/ProvidersSection.tsx'
const { ExecutionView } = load(section, { '../lib/configEvents': { useConfigChanged() {} } })
const execution = { profile: 'configured-new', readiness: 'ready', message: 'Live execution unverified', role_profiles: { implement: 'coding' }, pilot_profile: 'steward', codex_sandbox: 'workspace-write', token: 'SECRET', prompt: 'PRIVATE' }
const observed = { profile: 'recorded-old', backend: 'codex', model: 'recorded-model', token: 'SECRET' }

test('Execution separates current configuration from recorded execution and collapses diagnostics', () => {
  const html = render(React.createElement(ExecutionView, { execution, observed, error: '', observedError: '' }))
  for (const text of ['Execution', 'Configured profile', 'configured-new', 'Last observed run profile', 'recorded-old', 'Live execution is unverified', 'Advanced execution diagnostics', 'coding', 'steward']) assert.ok(html.includes(text), text)
  assert.doesNotMatch(html, /<details[^>]*open|SECRET|PRIVATE|<input|Add provider|Test Connection/)
  assert.match(html, /Configuration readiness: ready/)
  const missing = render(React.createElement(ExecutionView, { execution: { ...execution, profile: '', readiness: 'blocked' }, observed: null, error: '', observedError: 'Recorded execution metadata is unavailable.' }))
  assert.match(missing, /Not configured/)
  assert.match(missing, /Not recorded \/ unavailable/)
  assert.match(missing, /readiness: blocked/)
  assert.doesNotMatch(missing, /recorded-old|recorded-model/)
})

test('Execution fetches only supported GET metadata, refreshes and tolerates independent outages', async () => {
  let cursor = 0
  const state = [], effects = [], requests = []
  let configChanged
  const hooks = { ...React,
    useState(initial) { const i = cursor++; if (!(i in state)) state[i] = initial; return [state[i], next => { state[i] = typeof next === 'function' ? next(state[i]) : next }] },
    useEffect(fn) { effects.push(fn) }, useCallback(fn) { return fn },
  }
  const { ExecutionSection } = load(section, { react: hooks, '../lib/configEvents': { useConfigChanged(fn) { configChanged = fn } } })
  const original = global.fetch
  let providerFails = false, historyFails = false
  global.fetch = async (url, options) => {
    requests.push({ url, options })
    if (historyFails && url === '/api/pipeline') throw new Error('RAW PRIVATE ERROR')
    return { ok: !(providerFails && url === '/api/providers'), json: async () => url === '/api/providers' ? execution : { runtime: { execution: observed }, prompt: 'PRIVATE' } }
  }
  const draw = () => { cursor = 0; return ExecutionSection() }
  const settle = () => new Promise(resolve => setImmediate(resolve))
  try {
    let tree = draw()
    assert.match(render(tree), /Loading execution settings/)
    let cleanup = effects.pop()()
    await settle()
    tree = draw()
    assert.match(render(tree), /configured-new/)
    assert.match(render(tree), /recorded-old/)
    const before = state[4]
    tree.props.children[1].props.onClick()
    assert.equal(state[4], before + 1)
    cleanup()
    historyFails = true
    draw(); cleanup = effects.pop()(); await settle()
    assert.match(render(draw()), /configured-new/)
    assert.match(render(draw()), /Recorded execution metadata is unavailable/)
    assert.doesNotMatch(render(draw()), /recorded-old|RAW PRIVATE/)
    cleanup()
    providerFails = true; historyFails = false
    configChanged()
    draw(); cleanup = effects.pop()(); await settle()
    assert.match(render(draw()), /Could not read execution readiness/)
    assert.match(render(draw()), /recorded-old/)
    assert.doesNotMatch(render(draw()), /configured-new/)
    cleanup()
    for (const { url, options } of requests) {
      assert.ok(['/api/providers', '/api/pipeline'].includes(url))
      assert.equal(options.method, undefined)
      assert.equal(options.cache, 'no-store')
    }
  } finally { global.fetch = original }
})

test('Administration retains active/custom operator inspection with collapsed diagnostics', () => {
  const { OperatorInspection } = load('src/routes/_app.operators.tsx', {
    '@tanstack/react-router': { createFileRoute: () => options => options },
    '../lib/useDocumentTitle': { useDocumentTitle() {} },
    '../lib/utils': { cn: (...values) => values.filter(Boolean).join(' ') },
  })
  const ops = ['implement', 'repair-ci', 'track-progress', 'reply-comment', 'custom-check'].map(name => ({ name, description: 'Active inspection', target: 'issue', labels_required: ['ready'], labels_excluded: ['done'], lock_label: 'obsolete-lock', source: 'builtin' }))
  const html = render(React.createElement(OperatorInspection, { ops }))
  for (const text of [...ops.map(o => o.name), 'requires', 'excludes', 'Operator diagnostics', 'source: builtin', 'Historical run records']) assert.ok(html.includes(text), text)
  assert.doesNotMatch(html, /obsolete-lock|<details[^>]*open|classify|evaluate-bug|evaluate-feat|decompose/)
  assert.match(render(React.createElement(OperatorInspection, { ops: [], error: true })), /Could not load/)
  assert.match(render(React.createElement(OperatorInspection, { ops: [] })), /No operators registered/)
})

test('No UI invokes retired provider mutations or retains obsolete model presets', () => {
  const files = fs.readdirSync(path.join(__dirname, 'src'), { recursive: true }).filter(f => /\.tsx?$/.test(f))
  const sources = files.map(f => [f, fs.readFileSync(path.join(__dirname, 'src', f), 'utf8')])
  const providerCalls = sources.filter(([, s]) => s.includes('/api/providers'))
  assert.deepEqual(providerCalls.map(([f]) => f), ['components/ProvidersSection.tsx'])
  assert.match(providerCalls[0][1], /read\('\/api\/providers'\)/)
  const settings = fs.readFileSync(path.join(__dirname, 'src/routes/_app.settings.tsx'), 'utf8')
  assert.match(settings, /<ExecutionSection \/>/)
  assert.doesNotMatch(settings, /MODEL_PRESETS/)
  assert.match(settings, /type=\{show \? 'text' : 'password'\}/)
  assert.match(settings, /useState\(false\)/)
})

test('Historical run pages still display retired and custom operator names', () => {
  for (const operator of ['classify', 'evaluate-bug', 'evaluate-feat', 'decompose', 'custom-old']) {
    let cursor = 0
    const meta = { operator, repo: 'fixture/repo', issue_number: 1, started_at: '2026-01-01T00:00:00Z', status: 'success' }
    const { Route } = load('src/routes/_app.runs.$slug.$issue.$ts.tsx', {
      react: { ...React, useState: initial => [cursor++ === 0 ? meta : initial, () => {}], useEffect() {} },
      '@tanstack/react-router': {
        createFileRoute: () => options => ({ ...options, useParams: () => ({ slug: 'fixture__repo', issue: '1', ts: 'old' }) }),
        Link: ({ children }) => React.createElement('a', null, children),
      },
      '../lib/useDocumentTitle': { useDocumentTitle() {} },
      '../lib/utils': { cn: (...values) => values.filter(Boolean).join(' ') },
      '../lib/vcsUrls': { useRepoInfoMap: () => ({}), repoUrl: () => '#repo', issueUrl: () => '#issue' },
      '../components/VcsIcon': { VcsIcon: () => null },
      '../components/Markdown': { Markdown: () => null },
      '../components/WorkDetail': { workDetailKey: () => 'fixture' },
      '../components/TraceView': { TraceView: () => null },
    })
    assert.ok(render(React.createElement(Route.component)).includes(operator), operator)
  }
})
