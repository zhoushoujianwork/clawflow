import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtemp, rm, readFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
import { workListFixture } from './work-list-fixture.mjs'
const require = createRequire(import.meta.url)
const { build } = createRequire(require.resolve('vite'))('esbuild')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const fixture = workListFixture()
assert.equal(fixture.repos.length, 50)
assert.equal(fixture.repos.flatMap(r => r.items).length, 500)
const dir = await mkdtemp(join(tmpdir(), 'work-list-test-'))
try {
  await build({ stdin: { resolveDir: root, loader: 'tsx', contents: `
import React from 'react'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import { WorkList, workRows, workReason, WORK_PAGE_SIZE } from './src/components/WorkList'
import { workListFixture } from './scripts/work-list-fixture.mjs'
const {repos, queue, now} = workListFixture()
const props = {repos, queue, repoInfo:{}}
const active = workRows(repos, queue, 'active')
assert.equal(active.length, 400)
assert.deepEqual(active.slice(0,50).map(r=>r.repo),repos.map(r=>r.repo))
assert.deepEqual(active.slice(0,350).map(r=>r.queue.position),queue.map(q=>q.position))
// Priority labels are displayed, never used to reorder the service's verdict.
assert.equal(active[0].queue.tier,'P3')
assert.equal(active[1].queue.tier,'P0')
const duplicates = repos.map(r=>({...r, items:[...r.items, r.items[0]]}))
for (const [filter,count] of [['active',400],['running',50],['waiting',300],['delivered',50],['attention',50]]) {
  const rows = workRows(duplicates, queue, filter)
  assert.equal(rows.length,count)
  assert.equal(new Set(rows.map(r=>r.key)).size,count)
  const markup = renderToStaticMarkup(<WorkList {...props} repos={duplicates} filter={filter}/>)
  assert.equal((markup.match(/data-work-key=/g)||[]).length,Math.min(count,WORK_PAGE_SIZE))
  assert.equal((markup.match(/data-work-reason=/g)||[]).length,Math.min(count,WORK_PAGE_SIZE))
  assert.ok(markup.includes('LongTitleWithoutSpaces'))
  assert.ok(markup.includes('DetailedReasonWithoutSpaces'))
  if (filter === 'running' || filter === 'active') assert.ok(markup.includes('2 execution attempts'))
}
const first = renderToStaticMarkup(<WorkList {...props}/>)
assert.ok(first.includes('1–50 of 400 items'))
assert.ok(first.includes('Page 1 of 8'))
assert.ok(first.indexOf('Task 0-0:') < first.indexOf('data-stage='))
assert.ok(first.indexOf('data-stage=') < first.indexOf('data-work-reason'))
assert.ok(first.indexOf('data-work-reason') < first.indexOf('Role: implement'))
assert.ok(first.includes('Role: implement'))
assert.ok(first.includes('Elapsed: 2min'))
assert.ok(first.includes('Last progress: '+now))
assert.ok(!first.includes('<details open'))
const reason = (n,r=0) => workReason(repos[r].items[n],repos[r].enabled,queue.find(q=>q.repo===repos[r].repo&&q.number===n+1))
assert.ok(reason(2).includes('Waiting for CI'))
assert.ok(reason(3).includes('Waiting for independent acceptance'))
assert.ok(reason(4).includes('Waiting for dependencies'))
assert.ok(reason(5).includes('Waiting for repository capacity'))
assert.ok(reason(1,9).includes('Automation disabled'))
const delivered = workRows(repos,queue,'delivered')
assert.ok(delivered.every(r=>r.item.stage==='merged' && r.item.delivery.merged_at===now))
assert.ok(!delivered.some(r=>r.item.issue===7))
// Execution-only success, a stage name, invalid and future dates are not delivery evidence.
const invalid = [repos[0].items[6], {...repos[0].items[7], issue:11, delivery:undefined},
  ...['invalid',new Date(Date.now()+86400000).toISOString()].map((merged_at,n)=>({...repos[0].items[7],issue:12+n,delivery:{merged_at}}))]
assert.equal(workRows([{...repos[0],items:invalid}],[],'delivered').length,0)
const unknown = renderToStaticMarkup(<WorkList {...props} repos={[{...repos[0],items:[repos[0].items[1]]}]}/>)
assert.ok(unknown.includes('Role: unknown'))
assert.ok(unknown.includes('Elapsed: unknown'))
assert.ok(unknown.includes('Last progress: unknown'))
assert.ok(renderToStaticMarkup(<WorkList {...props} repos={[]}/>).includes('No items in this view'))
const reversed = workRows([...repos].reverse(),queue,'running')
assert.deepEqual(reversed.map(r=>r.key),workRows(repos,queue,'running').map(r=>r.key))
console.log('PASS: 50 repositories / 500 items; queue order, stable keys, bounded rows, deduplication, waiting reasons, unknown metrics and merge-only delivery')
` }, bundle: true, platform: 'node', format: 'cjs', outfile: join(dir,'test.cjs') })
  process.stdout.write(execFileSync(process.execPath,[join(dir,'test.cjs')]))
  const source = await readFile(join(root,'src/components/WorkList.tsx'),'utf8')
  assert.ok(!source.includes('truncate'))
  assert.ok(source.includes('[overflow-wrap:anywhere]'))
} finally { await rm(dir,{recursive:true,force:true}) }
