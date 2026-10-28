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
import { RuntimeHeader, RepoSection, EventTimeline, Attention, foldEvents, importantEvent, preferences } from './src/routes/_app.pipeline'
const runtime = { version:'test', scheduler:{status:'idle'}, execution:{ready:true,profile:'actual',backend:'codex'} }
const noTier = renderToStaticMarkup(<RuntimeHeader runtime={runtime}/>)
assert.ok(!noTier.includes('tier:'))
const withTier = renderToStaticMarkup(<RuntimeHeader runtime={{...runtime, execution:{...runtime.execution,model:'recorded-model',service_tier:'priority'}}}/>)
assert.ok(withTier.includes('recorded-model'))
assert.ok(withTier.includes('priority'))
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
assert.equal(preferences({tab:'invalid'}).tab,undefined)
assert.equal(preferences({tab:'all'}).tab,'all')
console.log('PASS: 5 repos / 40 items / 50 events; collapsed, tabs, queue truncation, attention, event folding')
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
