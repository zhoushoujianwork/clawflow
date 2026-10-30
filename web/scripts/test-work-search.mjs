import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
const require = createRequire(import.meta.url)
const { defaultParseSearch, defaultStringifySearch, createMemoryHistory } = require('@tanstack/react-router')
const { build } = createRequire(require.resolve('vite'))('esbuild')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
// Use the installed router history and query codec, including detail-preserving back/forward.
const selected = JSON.stringify(['fixture/repository-0', 1])
const entries = [{q:'#1',work:'running',detail:selected},{q:'nothing',stage:'queued',detail:selected}]
const history = createMemoryHistory({initialEntries:['/pipeline'+defaultStringifySearch(entries[0])]})
history.push('/pipeline'+defaultStringifySearch(entries[1]))
assert.deepEqual({...defaultParseSearch(history.location.search)},entries[1])
history.back()
assert.deepEqual({...defaultParseSearch(history.location.search)},entries[0])
history.forward()
assert.deepEqual({...defaultParseSearch(history.location.search)},entries[1])
history.destroy()
const dir = await mkdtemp(join(tmpdir(), 'work-search-test-'))
try {
  await build({ stdin: { resolveDir: root, loader: 'tsx', contents: `
import React from 'react'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import { workSearch, CLEAR_WORK_SEARCH } from './src/lib/workSearch'
import { WorkSearchControls } from './src/components/WorkSearchControls'
import { preferences } from './src/routes/_app.pipeline'
import { WorkList, workRows, workRole } from './src/components/WorkList'
import { WorkDetail } from './src/components/WorkDetail'
import { workListFixture } from './scripts/work-list-fixture.mjs'
const {repos,queue} = workListFixture()
const projects = [{name:'Platform',repos:[repos[0].repo,repos[1].repo]}]
const state = {q:'Task & #1',workRepo:repos[0].repo,project:'Platform',stage:'implementing',role:'implement',sort:'age',detail:JSON.stringify([repos[0].repo,1]),work:'running'}
assert.deepEqual(workSearch(${JSON.stringify(defaultParseSearch(defaultStringifySearch({q:'Task & #1',workRepo:'fixture/repository-0',project:'Platform',stage:'implementing',role:'implement',sort:'age'})))}),workSearch(state))
assert.deepEqual(workSearch({q:[],workRepo:42,project:{},stage:'invented',role:true,sort:'random'}),workSearch({}))
assert.equal(workSearch({q:'x'.repeat(257)}).q,undefined)
assert.equal(workSearch(${JSON.stringify(defaultParseSearch('?q=542'))}).q,'542')
assert.equal(preferences({tab:'all'}).work,undefined)
assert.equal(workRows(repos,queue,'active',workSearch({tab:'all'})).some(r=>r.item.stage==='merged'),false)
const matching = (query) => workRows(repos,queue,'active',query,projects)
assert.equal(matching({q:'task 0-0:'}).length,1)
assert.equal(matching({q:'#1'}).length,50)
assert.equal(matching({q:repos[0].repo+'#1'}).length,1)
assert.equal(matching({q:'nonexistent'}).length,0)
assert.equal(matching({workRepo:repos[0].repo}).length,8)
assert.equal(matching({project:'Platform'}).length,16)
assert.equal(matching({project:'removed'}).length,0)
assert.equal(matching({stage:'queued'}).length,200)
assert.equal(matching({role:'implement'}).length,100)
assert.equal(matching({role:'unknown'}).length,300)
assert.equal(matching({project:'Platform',stage:'implementing',role:'implement',q:'#1'}).length,2)
assert.equal(matching({workRepo:'removed'}).length,0)
const markup = renderToStaticMarkup(<WorkSearchControls search={state} repos={repos} projects={projects} projectError={false} update={()=>{}}/>)
for(const label of ['Title or issue','Repository','Project','Stage','Role','Order','Clear all filters']) assert.ok(markup.includes(label))
assert.ok(renderToStaticMarkup(<WorkList repos={repos} queue={queue} repoInfo={{}} query={{q:'nonexistent'}}/>).includes('0 results'))
const afterClear = preferences({...state,...CLEAR_WORK_SEARCH})
assert.equal(afterClear.detail,state.detail)
assert.equal(afterClear.q,undefined)
assert.equal(afterClear.work,undefined)
// Selection is independent of the visible list; filtering cannot retarget the panel.
assert.equal(matching({q:'nonexistent'}).length,0)
const missing = renderToStaticMarkup(<WorkDetail repo={repos[0].repo} issue={999} onClose={()=>{}}/>)
assert.ok(missing.includes('not in the current snapshot'))
const times = ['2026-01-01T00:00:00Z','2026-02-01T00:00:00Z',undefined,'invalid','0001-01-01T00:00:00Z']
const items = times.map((updated_at,n)=>({...repos[0].items[1],issue:n+1,updated_at}))
const input = [{...repos[0],items}]
const immutable = JSON.stringify(input)
for(const [sort,ids] of [['updated',[2,1,3,4,5]],['age',[1,2,3,4,5]]]) {
 const sorted = workRows(input,[],'active',{sort})
 assert.deepEqual(sorted.map(r=>r.item.issue),ids)
 assert.deepEqual(workRows([{...input[0],items:[...items].reverse()}],[],'active',{sort}).map(r=>r.key),sorted.map(r=>r.key))
}
assert.equal(JSON.stringify(input),immutable)
const refreshed = repos.map(r=>({...r,items:[...r.items].reverse()})).reverse()
for(const sort of [undefined,'age','updated']) assert.deepEqual(workRows(repos,queue,'active',{sort}).map(r=>r.key),workRows(refreshed,queue,'active',{sort}).map(r=>r.key))
assert.deepEqual(workRows(repos,queue,'active').slice(0,350).map(r=>r.queue.position),queue.map(q=>q.position))
const a = {status:'success',provenance:{source:'offload_final',stale:false},started_at:'2026-01-01T00:00:00Z'}
assert.equal(workRole({...items[0],executions:[{...a,attempt_id:'b',role:'reviewer'},{...a,attempt_id:'a',role:'implement'}]}),'implement')
console.log('PASS: URL codec, validation, combined search/filters, bounded active default, empty results, clear retaining detail, immutable stable refresh ordering and unknown times/roles')
` }, bundle: true, platform: 'node', format: 'cjs', outfile: join(dir,'test.cjs'), plugins: [{name:'router-stub',setup(b){
    b.onResolve({filter:/^@tanstack\/react-router$/},()=>({path:'router',namespace:'stub'}))
    b.onLoad({filter:/.*/,namespace:'stub'},()=>({contents:'export const createFileRoute = () => options => options'}))
  }}] })
  process.stdout.write(execFileSync(process.execPath,[join(dir,'test.cjs')]))
} finally { await rm(dir,{recursive:true,force:true}) }
