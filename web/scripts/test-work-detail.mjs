// Metadata-only server render and selectors, runnable without a browser/provider.
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtemp, rm, readFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
const require = createRequire(import.meta.url)
const { defaultParseSearch, defaultStringifySearch } = require('@tanstack/react-router')
const { build } = createRequire(require.resolve('vite'))('esbuild')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dir = await mkdtemp(join(tmpdir(),'work-detail-test-'))
try {
  await build({stdin:{resolveDir:root,loader:'tsx',contents:`
import React from 'react'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import { WorkDetail, AttemptList, workDetailKey, chronologicalAttempts } from './src/components/WorkDetail'
import { preferences, ItemDetail } from './src/routes/_app.pipeline'
import { runDetailUrl } from './src/lib/runDetailUrl'
import { workRows } from './src/components/WorkList'
import { workDetailFixture } from './scripts/work-detail-fixture.mjs'
const {item,history,board} = workDetailFixture()
// The browser fixture opens this row through the waiting filter.
assert.deepEqual(workRows(board.repos, [], 'waiting').map(row=>row.item.issue), [541])
// Unfinished unknown executions must still be excluded from waiting.
const unfinished = {...item, executions:item.executions.map(a=>a.attempt_id === 'legacy' ? {...a, ended_at:undefined} : a)}
assert.equal(workRows([{...board.repos[0], items:[unfinished]}], [], 'waiting').length, 0)
const ordered = chronologicalAttempts(item.executions)
assert.deepEqual(ordered.map(a=>a.attempt_id),['legacy','failed-first','repair-success','review'])
assert.notDeepEqual(item.executions,ordered) // selector does not mutate the polled snapshot
const markup = renderToStaticMarkup(<AttemptList attempts={item.executions} history={history}/>)
assert.equal((markup.match(/data-attempt-id=/g)||[]).length,4)
assert.equal((markup.match(/Latest attempt/g)||[]).length,1)
assert.ok(markup.includes('Latest attempt · reviewer'))
assert.ok(markup.indexOf('failed-first')<markup.indexOf('repair-success'))
assert.ok(markup.includes('execution_failed'))
assert.ok(markup.includes('independent-review'))
assert.equal((markup.match(/session: same-session/g)||[]).length,2)
assert.ok(markup.includes('Cost: unknown'))
assert.ok(markup.includes('Tokens (input / output / cache read / cache creation): unknown'))
assert.ok(markup.includes('$0.01 · estimate'))
assert.ok(markup.includes('10 / 2 / unknown / unknown'))
assert.ok(markup.includes('offload_final'))
assert.ok(markup.includes('2026-10-30T00:01:00Z'))
assert.equal((markup.match(/Open existing run details/g)||[]).length,3)
assert.ok(markup.includes('/runs/owner__repo/issue-541/failed-first'))
assert.ok(!markup.includes('Delivery successful'))
const undated = renderToStaticMarkup(<AttemptList attempts={[{status:'unknown',provenance:{source:'unknown',stale:false}}]} history={[]}/>)
assert.ok(undated.includes('Latest attempt unknown'))
assert.ok(!undated.includes('border-brand'))
const unavailable = renderToStaticMarkup(<AttemptList attempts={item.executions} history={[]}/>)
assert.ok(!unavailable.includes('$0.01'))
const zero = renderToStaticMarkup(<AttemptList attempts={[{...ordered[1],exit_code:0,elapsed_ms:0}]} history={[{...history[0],usage:{input_tokens:0,output_tokens:0,total_cost_usd:0}}]}/>)
assert.ok(zero.includes('Exit: 0'))
assert.ok(zero.includes('elapsed: 0 ms'))
assert.ok(zero.includes('$0'))
const evidence = renderToStaticMarkup(<ItemDetail item={item}/>)
assert.ok(evidence.includes('No merge recorded'))
assert.ok(evidence.includes('bbbbbbb'))
assert.ok(evidence.includes('pending'))
assert.ok(evidence.includes('passed (stale)'))
assert.ok(evidence.includes('ClawFlow independent acceptance'))
assert.ok(evidence.includes('Source: VCS PR/CI'))
assert.ok(evidence.includes('2026-10-30T00:05:00Z'))
assert.ok(evidence.includes('CI observation time: unknown'))
const selection = workDetailKey('owner/repo',541)
const state = {detail:selection,work:'waiting',repo:'owner/repo',events:'all'}
const params = new URLSearchParams(state)
assert.deepEqual(preferences(Object.fromEntries(params)).detail,selection)
assert.equal(preferences(Object.fromEntries(params)).work,'waiting')
assert.equal(preferences({detail:123}).detail,undefined)
// Exercise the real router codec, including manually composed JSON deep links.
assert.equal(preferences(${JSON.stringify(defaultParseSearch('?detail=' + encodeURIComponent(JSON.stringify(['owner/repo', 999]))))}).detail,workDetailKey('owner/repo',999))
assert.equal(preferences(${JSON.stringify(defaultParseSearch(defaultStringifySearch({detail:JSON.stringify(['owner/repo',541]),work:'waiting',events:'all',repo:'owner/repo'})))}).detail,selection)
for(const detail of [[],['owner/repo'],['owner/repo',0],['owner/repo','999'],['owner/repo',999,'extra']]) assert.equal(preferences({detail}).detail,undefined)
for(const path of ['/etc/passwd','https://evil.invalid/runs/a/issue-1/b','./data/runs/../issue-1/b/']) assert.equal(runDetailUrl(path),undefined)
assert.equal(runDetailUrl('./data/runs/owner__repo/issue-541/failed-first/'),'/runs/owner__repo/issue-541/failed-first')
const panel = renderToStaticMarkup(<WorkDetail repo="owner/repo" issue={541} item={item} onClose={()=>{}}/>)
assert.ok(panel.includes('aria-labelledby="work-detail-title"'))
assert.ok(panel.includes('Execution success does not establish CI'))
const missing = renderToStaticMarkup(<WorkDetail repo="owner/repo" issue={999} onClose={()=>{}}/>)
assert.ok(missing.includes('not in the current snapshot'))
console.log('PASS: isolated detail render; chronological retries, independent reviewer, unknown/zero usage, source timestamps, run links, immutable sorting, URL round trip and missing work')
`},bundle:true,platform:'node',format:'cjs',outfile:join(dir,'test.cjs'),plugins:[{name:'router-stub',setup(b){
    b.onResolve({filter:/^@tanstack\/react-router$/},()=>({path:'router',namespace:'stub'}))
    b.onLoad({filter:/.*/,namespace:'stub'},()=>({contents:'export const createFileRoute = () => options => options'}))
  }}]})
  process.stdout.write(execFileSync(process.execPath,[join(dir,'test.cjs')]))
  const source = await readFile(join(root,'src/components/WorkDetail.tsx'),'utf8')
  assert.ok(!source.includes('events.jsonl'))
  assert.ok(!source.includes('attempt_id:'))
  assert.ok(source.includes("limit: '100'"))
  assert.ok(source.includes('controller.abort()'))
} finally { await rm(dir,{recursive:true,force:true}) }
