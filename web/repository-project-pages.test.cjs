const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const Module = require('node:module')
const ts = require('typescript')
const React = require('react')
const { renderToStaticMarkup } = require('react-dom/server')
const { defaultStringifySearch, defaultParseSearch } = require('@tanstack/react-router')

// Isolated SSR fixtures: no network, desktop, saved configuration or agents.
function renderPage(file, values, params = {}) {
  let state = 0
  function load(filename) {
    const m = new Module(filename, module)
    m.filename = filename
    m.paths = module.paths
    const nativeRequire = m.require.bind(m)
    m.require = name => {
      if (name === 'react') return { ...React, useState: initial => {
        const index = state++
        return React.useState(Object.hasOwn(values, index) ? values[index] : initial)
      } }
      if (name === '@tanstack/react-router') return {
        createFileRoute: () => options => ({ ...options, useParams: () => params, useSearch: () => ({}), useNavigate: () => () => {} }),
        useNavigate: () => () => {},
        Link: ({ to, search, params: p, children, ...props }) => React.createElement('a', {
          ...props, href: to.replace('$name', p?.name || '').replace('$', p?._splat || '') + defaultStringifySearch(search || {}),
        }, children),
      }
      if (name.endsWith('/chatContext')) return { useChatDrawer: () => ({ open() {} }) }
      if (name.endsWith('/DocUpdater')) return { useDocUpdater: () => ({ trigger: 'UPDATE_DOC', form: null }) }
      if (name.endsWith('/GitSyncCell')) return { GitSyncCell: () => 'GIT_SYNC' }
      if (name.endsWith('/vcsUrls')) return { repoUrl: repo => 'https://github.com/' + repo, useRepoInfoMap: () => ({}) }
      if (name.endsWith('/VcsIcon')) return { VcsIcon: () => null }
      if (name.startsWith('.')) {
        const local = path.resolve(path.dirname(filename), name)
        for (const ext of ['.tsx', '.ts']) if (fs.existsSync(local + ext)) return load(local + ext)
      }
      return nativeRequire(name)
    }
    m._compile(ts.transpileModule(fs.readFileSync(filename, 'utf8'), {
      compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS, esModuleInterop: true },
    }).outputText, filename)
    return m.exports
  }
  const page = load(path.join(__dirname, 'src/routes', file))
  return renderToStaticMarkup(React.createElement(page.Route.component))
}
const repo = { full_name: 'team/repo', enabled: true, auto_merge: false, base_branch: 'main', bound_machine: 'host-a' }
const project = { name: '平台 & tools', repos: ['team/repo'], context_md: '# Context', testing_md: '# Testing', deployment_md: '# Deployment' }
function links(html, expectedWork, expectedUsage) {
  const hrefs = [...html.matchAll(/href="([^"]+)"/g)].map(m => m[1].replaceAll('&amp;', '&'))
  const work = hrefs.find(h => h.startsWith('/pipeline?'))
  assert.ok(work)
  assert.deepEqual({ ...defaultParseSearch(work.slice(work.indexOf('?'))) }, expectedWork)
  const usage = hrefs.find(h => h.startsWith('/usage?'))
  assert.ok(usage)
  assert.deepEqual(Object.fromEntries(new URLSearchParams(usage.split('?')[1])), expectedUsage)
}
test('repository retains management and primary context; links scope Work and Usage even when bound elsewhere', () => {
  for (const hostname of ['host-a', 'host-b']) {
    const html = renderPage('_app.repos.$.tsx', { 0: { ...repo, primary_project: 'primary' }, 1: false, 6: hostname, 8: ['other', 'primary'] }, { _splat: repo.full_name })
    links(html, { workRepo: repo.full_name }, { repo: repo.full_name })
    for (const text of ['Status', 'Auto-merge', 'Bound machine', 'host-a', 'primary ★', 'GIT_SYNC', 'CI：未报告', '独立验收：未报告']) assert.ok(html.includes(text), text)
    assert.ok(!html.includes('running ·'))
    assert.equal(/<button disabled=""[^>]*>[\s\S]*?Status/.test(html), hostname === 'host-b')
  }
})
test('project retains membership, document access, explicit actions and history without backlog/digest summaries', () => {
  const html = renderPage('_app.projects.$name.tsx', { 0: project, 1: false }, { name: project.name })
  links(html, { project: project.name }, { project: project.name })
  for (const text of ['Member repos', 'team/repo', 'context.md', 'testing.md', 'deployment.md', 'Add repo', 'Delete', 'UPDATE_DOC', 'Pilot 历史记录']) assert.ok(html.includes(text), text)
  assert.ok(!/Backlog|awaiting eval|Latest Pilot wake/.test(html))
})
test('list pages offer scoped destinations without a duplicate last-run column', () => {
  links(renderPage('_app.repos.index.tsx', { 0: [repo], 3: 'host-a', 4: false }), { workRepo: repo.full_name }, { repo: repo.full_name })
  links(renderPage('_app.projects.index.tsx', { 0: [project], 1: false }), { project: project.name }, { project: project.name })
})

test('known delivery policies remain distinguishable from unavailable evidence', () => {
  const html = renderPage('_app.repos.$.tsx', { 0: repo, 1: false, 10: { ci_required: true, acceptance_required: false } }, { _splat: repo.full_name })
  assert.ok(html.includes('CI：必需'))
  assert.ok(html.includes('独立验收：非必需'))
})
