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
import { EventTimeline, foldEvents, importantEvent, projectActivity, projectLogActivity, sourceTransitions } from './src/components/ActivityTimeline'
import { activityFixture } from './scripts/activity-fixture.mjs'
const board = activityFixture()
const original = JSON.stringify(board)
const projected = projectActivity(board.repos)
assert.equal(projected.filter(e=>e.event==='execution/started').length,3)
assert.ok(projected.some(e=>e.event==='execution/failed' && e.href.endsWith('/failed-attempt')))
assert.ok(projected.some(e=>e.event==='execution/success' && e.href.endsWith('/retry-attempt')))
for (const kind of ['execution/stalled','vcs/PR','vcs/CI','acceptance/failed','delivery/merged']) assert.ok(projected.some(e=>e.event.startsWith(kind)))
const linkedLog = projectLogActivity([{time:board.generated_at,level:'info',event:'run/harvest_end',fields:{repo:'fixture/repo',pr:'10',status:'failed'}}],board.repos)
assert.equal(linkedLog[0].fields.issue,'1')
const polls = board.events.slice(0,3)
const folded = foldEvents(polls)
assert.equal(folded.length,1)
assert.equal(folded[0].count,3)
assert.equal(folded[0].since,polls[1].time)
assert.equal(folded[0].time,polls[0].time)
assert.equal(foldEvents(board.events.slice(3)).length,66)
assert.equal(importantEvent(polls[0]),false)
assert.ok(importantEvent(projected.find(e=>e.event==='execution/failed')))
const down = [{source:'execution',repo:'fixture/repo',last_success:null,stale:true}]
const up = [{...down[0],last_success:board.generated_at,stale:false}]
const outage = sourceTransitions(undefined,down,board.generated_at)
const recovered = sourceTransitions(down,up,board.generated_at)
assert.equal(outage[0].event,'source/unavailable (observed)')
assert.equal(recovered[0].event,'source/recovered (observed)')
assert.equal(sourceTransitions(up,up,board.generated_at).length,0)
assert.equal(sourceTransitions(undefined,up,board.generated_at).length,0)
const render = (events,filter='all',repo='',issue='') => renderToStaticMarkup(<EventTimeline events={events} filter={filter} repo={repo} issue={issue} repos={['fixture/repo','other/repo']} update={()=>{}} />)
const html = render([...projected,...board.events,...outage,...recovered])
assert.ok(!html.includes('<details open'))
assert.ok(html.includes('Activity ('))
assert.ok(html.includes('Load 50 more activity groups'))
assert.ok(html.includes('latest 50 events'))
assert.ok(html.includes('retained run history'))
const details = render([...projected,...polls,...outage,...recovered])
for (const text of ['×3 since','Source: offload_final','source/recovered','source/unavailable','failed-attempt','retry-attempt','Open attempt','Open work item']) assert.ok(details.includes(text),text)
assert.ok(!render(projected,'important','other/repo').includes('<li '))
assert.ok(!render(projected,'important','','2').includes('<li '))
assert.ok(render(projected,'important','fixture/repo','1').includes('execution/failed'))
const unsafe = render([{...polls[0],fields:{...polls[0].fields,prompt:'SENSITIVE',token:'SECRET',err:'RAW'}}])
for (const text of ['SENSITIVE','SECRET','RAW']) assert.ok(!unsafe.includes(text))
assert.equal(JSON.stringify(board),original)
assert.equal(JSON.stringify(projectActivity(board.repos)),JSON.stringify(projected))
console.log('PASS: activity ordering, poll folding, separate shared-session attempts, retained failure, sources, filters, pagination, safe fields and unchanged state')

`,
    },
    sourcemap: 'inline', bundle: true, platform: 'node', format: 'cjs', outfile: join(dir, 'test.cjs'),
    plugins: [{ name: 'router-stub', setup(b) {
      b.onResolve({filter:/^@tanstack\/react-router$/}, () => ({path:'router',namespace:'stub'}))
      b.onLoad({filter:/.*/,namespace:'stub'}, () => ({contents:'export const createFileRoute = () => options => options'}))
    }}],
  })
  process.stdout.write(execFileSync(process.execPath, ['--enable-source-maps', join(dir, 'test.cjs')]))
} finally { await rm(dir, {recursive:true,force:true}) }
