const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const Module = require('node:module')
const ts = require('typescript')
const React = require('react')
const { renderToStaticMarkup } = require('react-dom/server')
const filename = path.join(__dirname, 'src/components/VersionBadge.tsx')
const compiled = ts.transpileModule(fs.readFileSync(filename, 'utf8'), {
  compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS },
}).outputText
const component = new Module(filename, module)
component.filename = filename
component.paths = module.paths
component._compile(compiled, filename)
const { VersionBadge } = component.exports
const props = { currentVersion: 'v0.75.0-54-gabcdef0', latestVersion: 'v0.75.0', channel: 'source', versionDetail: 'diverged', updating: false, restarting: false, showTooltip: true, triggerUpdate() {}, setShowTooltip() {} }
test('unsafe or unknown version has no upgrade button or offer', () => {
  const html = renderToStaticMarkup(React.createElement(VersionBadge, { ...props, updateAvailable: false }))
  assert.doesNotMatch(html, /<button|Click to upgrade/)
  assert.match(html, /source/)
  assert.match(html, /diverged/)
})
test('verified advance displays branch distance and upgrade button', () => {
  const html = renderToStaticMarkup(React.createElement(VersionBadge, { ...props, updateAvailable: true, versionDetail: '3 commits behind main' }))
  assert.match(html, /<button/)
  assert.match(html, /3 commits behind main/)
})
