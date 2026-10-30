// Exercise the real route tree and router, without a browser, API or desktop.
import { createRequire } from 'node:module'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
const require = createRequire(import.meta.url)
const { build } = createRequire(require.resolve('vite'))('esbuild')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dir = await mkdtemp(join(tmpdir(), 'dashboard-redirect-'))
try {
  await build({ stdin: { resolveDir: root, loader: 'tsx', contents: `
import assert from 'node:assert/strict'
import { createRouter, createMemoryHistory, defaultStringifySearch } from '@tanstack/react-router'
import { routeTree } from './src/routeTree.gen'
const context = {q:'Task & #42',workRepo:'owner/repo',project:'Platform',stage:'ci_failed',role:'implement',sort:'age',work:'attention',tab:'all',detail:['owner/repo',42],repo:'owner/repo',events:'all',activityIssue:'42'}
for (const path of ['/', '/dashboard', '/dashboard/']) {
 for (const search of [{}, context]) {
  const history = createMemoryHistory({initialEntries:[path + defaultStringifySearch(search) + '#context']})
  let destination
  let workRedirects = 0
  for (let hops = 0; hops < 3; hops++) {
    destination = createRouter({routeTree,history})
    await destination.load()
    const redirect = destination.state.redirect
    if (!redirect) break
    if (redirect.options.to === '/pipeline') {
      workRedirects++
      assert.equal(redirect.options.replace, true)
      assert.deepEqual({...redirect.options.search}, search)
      assert.equal(redirect.options.hash, 'context')
    }
    // A trailing slash may first receive the router's canonical-path redirect.
    await destination.navigate({...redirect.options, replace: true})
  }
  assert.equal(workRedirects, 1)
  assert.equal(destination.state.location.pathname, '/pipeline')
  assert.equal(destination.state.redirect, undefined, 'redirects must terminate')
  const validated = destination.state.matches.at(-1).search
  if (search.q) {
    for (const key of Object.keys(context).filter(k=>k!=='detail')) assert.equal(validated[key],context[key])
    assert.equal(validated.detail, JSON.stringify(context.detail))
  }
  assert.equal(history.length, 1, 'redirect replaces obsolete history entry')
  history.destroy()
 }
}
for (const [path,routeId,params] of [
 ['/pipeline','/_app/pipeline',{}],
 ['/repos/owner/repo','/_app/repos/$',{_splat:'owner/repo'}],
 ['/projects/Platform','/_app/projects/$name',{name:'Platform'}],
 ['/runs/owner__repo/issue-42/20261030','/_app/runs/$slug/$issue/$ts',{slug:'owner__repo',issue:'issue-42',ts:'20261030'}],
]) {
 const history = createMemoryHistory({initialEntries:[path]})
 const router = createRouter({routeTree,history})
 await router.load()
 assert.equal(router.state.redirect,undefined)
 assert.equal(router.state.matches.at(-1).routeId,routeId)
 for (const [key,value] of Object.entries(params)) assert.equal(router.state.matches.at(-1).params[key],value)
 history.destroy()
}
console.log('PASS: legacy/root bookmarks, query context, hash, replacement history, no loops, Work and repo/project/run routes')
` }, bundle: true, platform: 'node', format: 'esm', outfile: join(dir, 'test.mjs') })
  process.stdout.write(execFileSync(process.execPath, [join(dir, 'test.mjs')]))
} finally { await rm(dir, { recursive: true, force: true }) }
