import { createRequire } from 'node:module'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
const require = createRequire(import.meta.url)
const { build } = createRequire(require.resolve('vite'))('esbuild')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dir = await mkdtemp(join(tmpdir(), 'scheduled-checks-test-'))
try {
  await build({ stdin: { resolveDir: root, loader: 'tsx', contents: `
import React from 'react'
import assert from 'node:assert/strict'
import { renderToStaticMarkup as render } from 'react-dom/server'
import { ScheduledChecksPanel, latestCheckRun, resultLink } from './src/components/ScheduledChecksPanel'
import { Attention, attentionIncidents } from './src/components/Attention'
import { WorkSummary } from './src/components/WorkSummary'
import { ReadOnlyContext } from './src/lib/session'
import { scheduledChecksFixture as fixture } from './scripts/scheduled-checks-fixture.mjs'
const board = {repos:[],events:[]}
assert.equal(render(<ScheduledChecksPanel data={fixture('none')}/>),'')
for (const kind of ['healthy','overlap','disabled','stale','failed']) {
  const data = fixture(kind)
  const html = render(<ScheduledChecksPanel data={data}/>)
  assert.ok(html.includes('Observations, excluded from delivered issues'))
  assert.ok(html.includes(resultLink(data.checks[0].id)))
  assert.ok(!html.includes('PRIVATE_'))
  assert.ok(!html.includes('/settings'))
  assert.ok(!html.includes('<details open'))
  assert.ok(html.includes(kind === 'healthy' ? 'offload_reported' : 'Cost: unknown'))
  assert.ok(html.includes('Profile/model: unknown'))
  assert.ok(!html.includes('<script>'))
  const duplicate = {...data,checks:[...data.checks,...data.checks]}
  const rows = attentionIncidents(board,duplicate)
  assert.equal(rows.length,['failed','disabled'].includes(kind) ? 1 : 0)
  if (rows.length) {
    const attention = render(<Attention board={board} schedules={duplicate} repoInfo={{}}/>)
    assert.equal((attention.match(/data-incident-key=/g)||[]).length,1)
    assert.ok(attention.includes('Latest failure evidence &lt;script&gt;'))
    assert.ok(!attention.includes('View repository'))
  }
  const summary = render(<WorkSummary repos={[]} generatedAt={new Date().toISOString()} onFilter={()=>{}}/>)
  assert.ok(!summary.includes('morning-check'))
  if(kind==='overlap') assert.ok(html.includes('previous invocation still running'))
  if(kind==='disabled') assert.ok(html.includes('Disabled after three failures'))
  if(kind==='stale') assert.ok(html.includes('Stale result: newer run not in available history'))
}
const overlapFailure = fixture('overlap')
overlapFailure.checks[0].failures = 1
assert.equal(attentionIncidents(board,overlapFailure).length,1)
const data = fixture('healthy')
const zero = structuredClone(data)
zero.runs[0].usage = {cost_basis:'offload_reported', cost_unavailable:false,total_cost_usd:0,tokens_unavailable:false,input_tokens:0,output_tokens:0,cache_read_input_tokens:0,cache_creation_input_tokens:0}
assert.ok(render(<ScheduledChecksPanel data={zero}/>).includes('Cost: 0 USD'))
assert.ok(render(<ScheduledChecksPanel data={zero}/>).includes('Tokens: 0 input'))
zero.runs[0].usage = {total_cost_usd:0}
assert.ok(render(<ScheduledChecksPanel data={zero}/>).includes('Cost: unknown'))
const local = render(<ReadOnlyContext.Provider value={false}><ScheduledChecksPanel data={data}/></ReadOnlyContext.Provider>)
assert.ok(local.includes('/settings#scheduled-checks'))
const admin = render(<ScheduledChecksPanel data={data} administration/>)
assert.ok(admin.includes('clawflow pilot schedule'))
assert.ok(!admin.includes('PRIVATE_'))
const missing = render(<ScheduledChecksPanel data={{checks:data.checks,runs:[]}}/>)
assert.ok(missing.includes('Latest result unavailable'))
const reversed = {...data,runs:[{...data.runs[0],id:'new',started_at:'2026-10-31T06:00:00Z'},data.runs[0]]}
assert.equal(latestCheckRun(reversed,data.checks[0].id).id,'new')
const stale = render(<ScheduledChecksPanel data={data} error="Scheduled observations unavailable; previous results may be stale."/>)
assert.ok(stale.includes('previous results may be stale'))
console.log('PASS: empty, healthy, overlap, disabled, stale, deduplicated attention, attribution, unknown metrics and access-safe rendering')
` }, bundle: true, platform: 'node', format: 'cjs', outfile: join(dir,'test.cjs') })
  process.stdout.write(execFileSync(process.execPath,[join(dir,'test.cjs')]))
} finally { await rm(dir,{recursive:true,force:true}) }
