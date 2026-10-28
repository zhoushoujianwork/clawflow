const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const Module = require('node:module')
const ts = require('typescript')
const React = require('react')
const { renderToStaticMarkup } = require('react-dom/server')

// Render the real layout with resolved session states, without a browser or API.
function renderLayout(readonly, pathname = '/dashboard', ready = true) {
  const filename = path.join(__dirname, 'src/routes/_app.tsx')
  const compiled = ts.transpileModule(fs.readFileSync(filename, 'utf8'), {
    compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS },
  }).outputText
  const component = new Module(filename, module)
  component.filename = filename
  component.paths = module.paths
  let state = 0
  const nativeRequire = component.require.bind(component)
  component.require = name => {
    if (name === 'react') return { ...React, useState: initial => {
      const value = state++ === 0 ? readonly : state === 2 ? ready : initial
      return React.useState(value)
    }}
    if (name === '@tanstack/react-router') return {
      createFileRoute: () => options => options,
      useLocation: () => pathname,
      Link: ({ to, children }) => React.createElement('a', { href: to }, children),
      Outlet: () => React.createElement('div', null, 'PAGE_CONTENT'),
    }
    if (name.endsWith('/session')) return { ReadOnlyContext: React.createContext(true) }
    if (name.endsWith('/useTheme')) return { useTheme: () => ({ theme: 'dark', toggle() {} }) }
    if (name.endsWith('/chatContext')) return {
      ChatProvider: ({ children }) => children,
      useChatDrawer: () => ({ open() {} }),
    }
    if (name.endsWith('/ChatDrawer')) return { ChatDrawer: () => 'CHAT_DRAWER' }
    if (name.endsWith('/SyncPopover')) return { SyncPopover: () => 'SYNC_CONTROL' }
    if (name.endsWith('/VersionBadge')) return { VersionBadge: () => null }
    return nativeRequire(name)
  }
  component._compile(compiled, filename)
  return renderToStaticMarkup(React.createElement(component.exports.Route.component))
}

test('remote session hides write and sensitive navigation controls', () => {
  const html = renderLayout(true)
  assert.match(html, /Read-only/)
  assert.match(html, /PAGE_CONTENT/)
  assert.doesNotMatch(html, /Report an issue|SYNC_CONTROL|CHAT_DRAWER|href="\/(settings|repos|projects|operators)"/)
})
test('loopback session keeps local controls', () => {
  const html = renderLayout(false)
  assert.match(html, /Report an issue/)
  assert.match(html, /SYNC_CONTROL/)
  assert.match(html, /CHAT_DRAWER/)
  assert.match(html, /href="\/settings"/)
})
test('remote deep links cannot mount privileged pages', () => {
  const html = renderLayout(true, '/settings')
  assert.match(html, /requires local access/)
  assert.doesNotMatch(html, /PAGE_CONTENT/)
})
test('pending session does not mount page controls', () => {
  const html = renderLayout(true, '/dashboard', false)
  assert.match(html, /Loading session/)
  assert.doesNotMatch(html, /PAGE_CONTENT|SYNC_CONTROL|CHAT_DRAWER/)
})

function dashboardComponents(readonly) {
  const filename = path.join(__dirname, 'src/routes/_app.dashboard.tsx')
  const source = fs.readFileSync(filename, 'utf8') + '\nexport { Dashboard, Row, AutoRunBanner }'
  const compiled = ts.transpileModule(source, {
    compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS },
  }).outputText
  const component = new Module(filename, module)
  component.filename = filename
  component.paths = module.paths
  const nativeRequire = component.require.bind(component)
  component.require = name => {
    if (name === '@tanstack/react-router') return {
      createFileRoute: () => options => options,
      Link: ({ children }) => React.createElement('a', null, children),
    }
    if (name.endsWith('/session')) return { useReadOnly: () => readonly }
    if (name.endsWith('/useDocumentTitle')) return { useDocumentTitle() {} }
    if (name.endsWith('/utils')) return { cn: (...args) => args.filter(Boolean).join(' ') }
    if (name.endsWith('/vcsUrls')) return { issueUrl: () => '#' }
    if (name.endsWith('/VcsIcon')) return { VcsIcon: () => null }
    if (name.endsWith('/IssueList')) return { SubIssueRing: () => null }
    return nativeRequire(name)
  }
  component._compile(compiled, filename)
  return component.exports
}

test('remote dashboard disables run and pause, hides cancellation', () => {
  for (const readonly of [true, false]) {
    const { Dashboard, Row, AutoRunBanner } = dashboardComponents(readonly)
    const dashboard = renderToStaticMarkup(React.createElement(Dashboard))
    const runButton = dashboard.match(/<button[^>]*>[\s\S]*?<\/button>/)[0]
    assert.equal(/disabled/.test(runButton), readonly)
    const banner = renderToStaticMarkup(React.createElement(AutoRunBanner, {
      intervalMin: 5, paused: false, nextFireMs: 0, now: 0, busy: false, onToggle() {},
    }))
    assert.equal(/disabled/.test(banner), readonly)
    const row = renderToStaticMarkup(React.createElement(Row, {
      r: { repo: 'owner/repo', issue_number: 1, operator: 'implement', started_at: '2026-10-28T00:00:00Z', status: 'running', path: 'runs/test' },
      repoMap: {}, onCancel() {},
    }))
    assert.equal(/<button/.test(row), !readonly)
  }
})
