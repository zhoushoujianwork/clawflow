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

test('remote sessions may open the run observation pages', () => {
  for (const path of ['/jobs', '/runners', '/jobs/0123abcdef']) {
    const html = renderLayout(true, path)
    assert.match(html, /PAGE_CONTENT/, path)
    assert.doesNotMatch(html, /requires local access/, path)
  }
  const nav = renderLayout(true, '/jobs').match(/<nav[^>]*>[\s\S]*?<\/nav>/)[0]
  assert.match(nav, /href="\/jobs"/)
  assert.match(nav, /href="\/runners"/)
})
test('remote sessions cannot reach neighbours of the run pages', () => {
  for (const path of ['/jobs/a/b', '/jobs/', '/jobs/x%2Fy', '/runners/x', '/jobsx']) {
    const html = renderLayout(true, path)
    assert.match(html, /requires local access/, path)
    assert.doesNotMatch(html, /PAGE_CONTENT/, path)
  }
})
