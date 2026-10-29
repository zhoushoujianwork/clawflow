// Headless rendering and event regression fixtures. Run: node web/scripts/test-pipeline.mjs
import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
const { build } = createRequire(require.resolve('vite'))('esbuild')
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const dir = await mkdtemp(join(tmpdir(), 'pipeline-test-'))
try {
  await build({
    stdin: {
      resolveDir: join(dirname(fileURLToPath(import.meta.url)), '..'),
      loader: 'tsx',
      contents: `
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import assert from 'node:assert/strict'
import { itemPilotActions, PilotTimeline, RuntimeHeader, APIUsagePanel, RepoSection, EventTimeline, Attention, foldEvents, importantEvent, preferences } from './src/routes/_app.pipeline'
const runtime = { version:'test', scheduler:{status:'idle'}, execution:{ready:true,profile:'actual',backend:'codex'} }
const noTier = renderToStaticMarkup(<RuntimeHeader runtime={runtime}/>)
assert.ok(!noTier.includes('tier:'))
const withTier = renderToStaticMarkup(<RuntimeHeader runtime={{...runtime, execution:{...runtime.execution,model:'recorded-model',service_tier:'priority'}}}/>)
assert.ok(withTier.includes('recorded-model'))
assert.ok(withTier.includes('priority'))
const apiUsage = renderToStaticMarkup(<APIUsagePanel usage={{captured_at:'2026-10-30T00:00:00Z', calls_by_stage:{scan:8,harvest:2}, budgets:{'https://api.github.com':{limit:5000,remaining:499,reset:'2026-10-30T01:00:00Z',backoff:'2026-10-30T01:00:00Z'}}}}/>)
assert.ok(apiUsage.includes('499/5000'))
assert.ok(apiUsage.includes('重置'))
assert.ok(apiUsage.includes('scan: 8'))
assert.ok(apiUsage.includes('harvest: 2'))
assert.equal(renderToStaticMarkup(<APIUsagePanel/>), '')
// Work summary fixtures share selectors with rendered repository rows.
import { WorkSummary, selectWorkItems, unknownExecution } from './src/components/WorkSummary'
const attempt = (profile, status = 'running', stale = false) => ({profile, status, provenance:{source:'offload_live', stale}})
const workNow = Date.now()
const baseItem = {state:'open', stage:'queued', evidence:[], why:'Fixture'}
const workItems = [
  {...baseItem, issue:1, title:'Profile alpha', executions:[attempt('alpha'), attempt('alpha')]},
  {...baseItem, issue:2, title:'Profile beta', executions:[attempt('beta')]},
  {...baseItem, issue:3, title:'Waiting'},
  {...baseItem, issue:4, title:'Attention', attention:['CI failed', 'Review failed']},
  {...baseItem, issue:5, title:'Stalled', executions:[attempt('alpha','stalled')]},
  {...baseItem, issue:6, title:'Unknown', run:{live:true}, executions:[attempt('beta','running',true)]},
  {...baseItem, issue:7, title:'Merged', state:'closed', stage:'merged', delivery:{pr:7, merged_at:new Date(workNow - 1000).toISOString()}},
  {...baseItem, issue:8, title:'Old merge', state:'closed', stage:'merged', delivery:{pr:8, merged_at:new Date(workNow - 8*86400000).toISOString()}},
  {...baseItem, issue:9, title:'Future merge', state:'closed', stage:'merged', delivery:{pr:9, merged_at:new Date(workNow + 86400000).toISOString()}},
  {...baseItem, issue:10, title:'Succeeded attempt is not delivered', executions:[{...attempt('alpha','success'), ended_at:new Date(workNow).toISOString(), provenance:{source:'offload_final', stale:false}}]},
]
const workRepo = {repo:'fixture/repo', enabled:true, items:workItems, max_in_flight:2}
const workProps = {repos:[workRepo],generatedAt:new Date(workNow).toISOString(),onFilter:()=>{}}
for (const [filter, ids] of Object.entries({running:[1,2], waiting:[3,10], attention:[4,5], delivered:[7]})) {
  assert.deepEqual(selectWorkItems(workItems, filter, workNow).map(i=>i.issue),ids)
  const rows = renderToStaticMarkup(<RepoSection board={workRepo} repoInfo={{}} queue={workItems.map((i,n)=>({repo:workRepo.repo,number:i.issue,position:n+1}))} tab="active" work={filter} open toggle={()=>{}}/>)
  assert.equal((rows.match(/data-stage=/g)||[]).length,ids.length)
  for (const i of workItems) assert.equal(rows.includes(i.title + '</span>'),ids.includes(i.issue))
}
const manyWaiting = Array.from({length:8}, (_,n)=>({...baseItem,issue:n+1,title:'Waiting '+n}))
const manyRows = renderToStaticMarkup(<RepoSection board={{...workRepo,items:manyWaiting}} repoInfo={{}} queue={manyWaiting.map((i,n)=>({repo:workRepo.repo,number:i.issue,position:n+1}))} tab="active" work="waiting" open toggle={()=>{}}/>)
assert.equal((manyRows.match(/data-stage=/g)||[]).length,selectWorkItems(manyWaiting,'waiting').length)
assert.ok(!manyRows.includes('+2 more'))
assert.equal(selectWorkItems([{stage:'merged',delivery:{merged_at:new Date(workNow - 7*86400000).toISOString()}}],'delivered',workNow).length,1)
assert.equal(selectWorkItems([{stage:'merged',delivery:{merged_at:'invalid'}}],'delivered',workNow).length,0)
assert.equal(unknownExecution(workItems[5]),true)
const summary = renderToStaticMarkup(<WorkSummary {...workProps}/>)
assert.ok(summary.includes('items, not attempts'))
assert.ok(summary.includes('1 active items have unknown execution activity'))
assert.ok(!summary.includes('alpha'))
assert.ok(!summary.includes('beta'))
const retained = renderToStaticMarkup(<WorkSummary {...workProps} refreshing failed/>)
assert.equal((retained.match(/text-2xl[^>]*>2</g)||[]).length,3)
assert.ok(retained.includes('keeping the current snapshot'))
assert.ok(retained.includes('Partial or stale snapshot'))
const loadingSummary = renderToStaticMarkup(<WorkSummary onFilter={()=>{}}/>)
assert.equal((loadingSummary.match(/>—</g)||[]).length,4)
assert.ok(loadingSummary.includes('Loading work'))
assert.ok(loadingSummary.includes('disabled'))
const emptySummary = renderToStaticMarkup(<WorkSummary {...workProps} repos={[]}/>)
assert.equal((emptySummary.match(/>0</g)||[]).length,4)
assert.ok(emptySummary.includes('No work in the available snapshot'))
const partialSummary = renderToStaticMarkup(<WorkSummary {...workProps} sources={[
 {source:'delivery',last_success:workProps.generatedAt,stale:false},
 {source:'execution',last_success:null,stale:true,error_category:'unavailable'},
 {source:'execution',last_success:workProps.generatedAt,stale:true},
]}/> )
assert.ok(partialSummary.includes('execution: 0 fresh, 1 stale, 1 unknown sources'))
assert.ok(partialSummary.includes('Partial or stale snapshot'))
// Exercise the same callback attached to each summary control without a DOM.
const selected = []
const tree = WorkSummary({...workProps,onFilter:key=>selected.push(key)})
for (const button of tree.props.children[0].props.children) button.props.onClick()
assert.deepEqual(selected,['running','waiting','attention','delivered'])
assert.equal(preferences({work:'running'}).work,'running')
assert.equal(preferences({work:'invalid'}).work,undefined)
assert.ok(noTier.includes('one execution only'))

const now = new Date().toISOString()
const repos = Array.from({length: 5}, (_, r) => ({
  repo: 'owner/repo-' + r, enabled:true, auto_merge:false, ci_required:true,
  acceptance_required:true, queue_scope:'all', max_in_flight:2,
  items:Array.from({length: 8}, (_, n) => ({
    issue:n+1, title:'Issue title ' + n, stage:n === 7 ? 'merged' : n === 0 ? 'implementing' : 'queued',
    state:n === 7 ? 'closed' : 'open', why:'Waiting for the next pass', evidence:[],
    run:n === 0 ? {live:true} : undefined,
    delivery:n === 7 ? {pr:100, merge_commit_sha:'abcdef012345', merged_at:now} : undefined,
    attention:n === 1 ? ['waiting for review (manual merge)'] : [],
  }))
}))
const queue = repos.flatMap((r, ri) => r.items.slice(0,7).map((i, n) => ({repo:r.repo,number:i.issue,position:ri*7+n+1})))
const props = {repoInfo:{}, queue, tab:'active', toggle:()=>{}}
const collapsed = renderToStaticMarkup(<>{repos.map(r => <RepoSection key={r.repo} board={r} {...props} open={false}/>)}</>)
assert.equal((collapsed.match(/<section/g)||[]).length,5)
assert.equal((collapsed.match(/aria-expanded="false"/g)||[]).length,5)
assert.ok(!collapsed.includes('Issue title'))
const active = renderToStaticMarkup(<RepoSection board={repos[0]} {...props} open />)
assert.equal((active.match(/data-stage=/g)||[]).length,6)
assert.ok(active.includes('+1 more'))
assert.ok(!active.includes('abcdef0'))
const delivered = renderToStaticMarkup(<RepoSection board={repos[0]} {...props} open tab="delivered"/>)
assert.ok(delivered.includes('abcdef0'))
assert.equal((delivered.match(/data-stage=/g)||[]).length,1)
const all = renderToStaticMarkup(<RepoSection board={repos[0]} {...props} open tab="all"/>)
assert.ok(all.includes('abcdef0'))
const attention = renderToStaticMarkup(<Attention board={{repos,events:[]}} repoInfo={{}}/>)
assert.ok(attention.includes('Needs attention (5)'))
const pilotActions = [
 {run_id:'wake',actor:'pilot',at:now,outcome:'applied',action:{id:'q',type:'ask_human',repo:'owner/repo-0',issue:2,body:'Which environment? <script>bad</script>'}},
 {run_id:'wake',actor:'pilot',at:now,outcome:'head_changed',action:{id:'m',type:'override_merge',repo:'owner/repo-0',pr:100,head:'a'.repeat(40)}},
 {run_id:'wake',actor:'pilot',at:now,outcome:'applied',action:{id:'n',type:'note',repo:'owner/repo-0',body:'Diagnosis recorded'}},
]
const pilotAttention = renderToStaticMarkup(<Attention board={{repos,events:[],pilot_actions:pilotActions}} repoInfo={{}}/>)
assert.ok(pilotAttention.includes('Needs attention (8)'))
assert.ok(pilotAttention.includes('Which environment?'))
assert.ok(pilotAttention.includes('override_merge · head_changed'))
assert.ok(pilotAttention.includes('Diagnosis recorded'))
assert.ok(!pilotAttention.includes('<script>'))
assert.equal(itemPilotActions(pilotActions, 'other/repo', {issue:2,pr:{number:100}}).length,0)
assert.equal(itemPilotActions(pilotActions, 'owner/repo-0', {issue:2}).length,1)
assert.equal(itemPilotActions(pilotActions, 'owner/repo-0', {issue:100}).length,0)
assert.equal(itemPilotActions(pilotActions, 'owner/repo-0', {issue:3,pr:{number:100}}).length,1)
assert.equal(itemPilotActions([{...pilotActions[2],created_issue:7}], 'owner/repo-0', {issue:7}).length,1)
const pilotTimeline = renderToStaticMarkup(<PilotTimeline actions={pilotActions}/>)
assert.ok(pilotTimeline.includes('Pilot timeline'))
assert.ok(pilotTimeline.includes('a'.repeat(40)))
assert.ok(pilotTimeline.includes('wake'))
assert.ok(!pilotTimeline.includes('<script>'))
const events = Array.from({length:50},(_,n)=>({time:new Date(Date.now()+n*1000).toISOString(), level:'info', event:'run/queue_wait', fields:{repo:'owner/repo-0',issue:'381'}}))
assert.equal(foldEvents(events).length,1)
assert.equal(foldEvents(events)[0].count,50)
assert.equal(foldEvents(events)[0].since,events[0].time)
assert.equal(foldEvents([events[0], {...events[1],fields:{repo:'other',issue:'381'}},events[2]]).length,3)
assert.ok(!importantEvent({...events[0],event:'run/automerge_sweep'}))
assert.ok(importantEvent({...events[0],event:'run/harvest_end'}))
assert.ok(importantEvent({...events[0],event:'run/lock_timeout',level:'warn'}))
assert.ok(importantEvent({...events[0],event:'queue/park'}))
const timeline = renderToStaticMarkup(<EventTimeline events={events} filter="important" repo="" repos={repos.map(r=>r.repo)} update={()=>{}}/>)
assert.ok(timeline.includes('×50 since'))
assert.ok(!timeline.includes('<details open'))
// Hidden events must still separate otherwise identical event groups.
for (const separator of [
  {...events[1], event:'run/automerge_skip'},
  {...events[1], fields:{repo:'other',issue:'381'}},
]) {
  const separated = [events[0], separator, events[2], events[3]]
  for (const filter of ['important', 'all']) {
    for (const repo of ['', 'owner/repo-0']) {
      const rendered = renderToStaticMarkup(<EventTimeline events={separated} filter={filter} repo={repo} repos={['owner/repo-0','other']} update={()=>{}}/>)
      const separatorVisible = (!repo || separator.fields.repo === repo) && (filter === 'all' || importantEvent(separator))
      assert.equal((rendered.match(/<li /g)||[]).length, separatorVisible ? 3 : 2)
      assert.ok(rendered.includes('×2 since'))
      assert.ok(!rendered.includes('×3 since'))
    }
  }
}
assert.equal(preferences({tab:'invalid'}).tab,undefined)
assert.equal(preferences({tab:'all'}).tab,'all')
console.log('PASS: Work summary selectors, mixed profiles, loading/empty/stale snapshots, complete filtered rows; 5 repos / 40 items / 50 events')
`,
    },
    bundle: true, platform: 'node', format: 'cjs', outfile: join(dir, 'test.cjs'),
    plugins: [{ name: 'router-stub', setup(b) {
      b.onResolve({filter:/^@tanstack\/react-router$/}, () => ({path:'router',namespace:'stub'}))
      b.onLoad({filter:/.*/,namespace:'stub'}, () => ({contents:'export const createFileRoute = () => options => options'}))
    }}],
  })
  process.stdout.write(execFileSync(process.execPath, [join(dir, 'test.cjs')]))
} finally { await rm(dir, {recursive:true,force:true}) }
