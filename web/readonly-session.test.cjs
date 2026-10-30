const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const Module = require('node:module')
const ts = require('typescript')
const React = require('react')
const { renderToStaticMarkup } = require('react-dom/server')

// Render the real layout with resolved session states, without a browser or API.
function renderLayout(readonly, pathname = '/pipeline', ready = true) {
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
  assert.doesNotMatch(html, /Report an issue|Steward|Management|SYNC_CONTROL|CHAT_DRAWER|href="\/(settings|repos|projects|operators)"/)
})
test('loopback session keeps local controls', () => {
  const html = renderLayout(false)
  assert.match(html, /Report an issue/)
  assert.match(html, /SYNC_CONTROL/)
  assert.match(html, /CHAT_DRAWER/)
  assert.match(html, /href="\/settings"/)
})
test('Work and Usage lead navigation with one work destination', () => {
  const html = renderLayout(false, '/pipeline')
  const nav = html.match(/<nav[^>]*>[\s\S]*?<\/nav>/)[0]
  assert.match(nav, /^<nav[^>]*><a href="\/pipeline">Work<\/a><a href="\/usage">Usage<\/a>/)
  for (const route of ['repos', 'projects', 'operators', 'settings']) {
    assert.match(nav, new RegExp('href="/' + route + '"'))
  }
  assert.doesNotMatch(nav, /Dashboard|href="\/dashboard"/)
  assert.equal((nav.match(/href="\/pipeline"/g) || []).length, 1)
  assert.doesNotMatch(nav, /data-active="true"/)
  assert.match(html, /href="#main-content"/)
  assert.match(html, /id="main-content"[^>]*tabindex="-1"/)
})
test('nested management routes mark their group active', () => {
  assert.match(renderLayout(false, '/repos/owner/repo'), /data-active="true">Management/)
  assert.match(renderLayout(false, '/settings'), /data-active="true">Administration/)
})
test('administration retains local destinations and is absent on LAN', () => {
  const local = renderLayout(false)
  const group = local.match(/<details[^>]*><summary[^>]*>Administration<\/summary>([\s\S]*?)<\/details>/)[1]
  assert.match(group, /href="\/operators"/)
  assert.match(group, /href="\/settings"/)
  assert.doesNotMatch(group, /Dashboard|href="\/dashboard"/)
  assert.doesNotMatch(renderLayout(true), />Administration<|href="\/(operators|settings|dashboard)"/)
})
test('remote deep links cannot mount privileged pages', () => {
  const html = renderLayout(true, '/settings')
  assert.match(html, /requires local access/)
  assert.doesNotMatch(html, /PAGE_CONTENT/)
})
test('pending session does not mount page controls', () => {
  const html = renderLayout(true, '/pipeline', false)
  assert.match(html, /Loading session/)
  assert.doesNotMatch(html, /PAGE_CONTENT|SYNC_CONTROL|CHAT_DRAWER/)
})
