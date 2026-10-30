// Isolated server-render fixtures; no provider, database or desktop needed.
import { createRequire } from 'node:module'
import { mkdtemp, rm, readFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
import assert from 'node:assert/strict'
const require = createRequire(import.meta.url)
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dir = await mkdtemp(join(tmpdir(), 'run-detail-test-'))
try {
  const { build } = createRequire(require.resolve('vite'))('esbuild')
  await build({stdin:{resolveDir:root,loader:'tsx',contents:`
import React from 'react'
import assert from 'node:assert/strict'
import { renderToStaticMarkup as render } from 'react-dom/server'
import { TraceView, buildSteps } from './src/components/TraceView'
import { ExecutionEvidence, UsagePanel, elapsedEvidence } from './src/routes/_app.runs.$slug.$issue.$ts'
const timestamp = '2026-10-30T00:00:01Z'
const events = [null, 42, {}, {type:'assistant',message:{content:{}}}, {type:'assistant',message:{content:[null,{type:'text',text:{}}]}},
 {schema_version:1,type:'start',timestamp,run_id:'retry-1',session:'same-session'},
 {schema_version:1,type:'final',timestamp,run_id:'retry-1',session:'same-session',status:'error',exit_code:1},
 {schema_version:1,type:'start',timestamp,run_id:'retry-2',session:'same-session'},
 {schema_version:1,type:'progress',timestamp,native_type:'tool_call',tool_category:'Read',text:'DO NOT RENDER',args:'SECRET'},
 {schema_version:1,type:'final',timestamp,status:'timeout',exit_code:124,error_kind:'timeout'},
 {schema_version:1,type:'unknown',timestamp}, {schema_version:99,type:'progress'},
 {type:'assistant',message:{content:[{type:'text',text:'<script>alert(1)</script>'},{type:'tool_use',id:'tool',name:'Read',input:{path:'fixture'}}]}},
 {type:'user',message:{content:[{type:'tool_result',tool_use_id:'tool',content:'done'}]}},
 {type:'result',subtype:'success',result:'legacy result',total_cost_usd:null},
 {version:1,type:'observation',observation:{type:'status',message:'old AGH record'}},
 {version:1,type:'result',result:{status:'completed',text:'AGH final'}},
 {version:1,type:'result',result:{status:'failed',text:{},error:{}}},
 {type:'result',subtype:{},duration_ms:'wrong',total_cost_usd:{}},
 {version:10,type:'observation'}]
const steps = buildSteps(events)
assert.ok(steps.some(s=>s.text?.includes('tool_call · tool: Read')))
assert.equal(steps.filter(s=>s.runID==='retry-1').length,2)
assert.equal(steps.filter(s=>s.runID==='retry-2').length,1)
assert.ok(steps.some(s=>s.subtype?.includes('timeout · exit: 124')))
assert.ok(steps.some(s=>s.text?.includes('Unknown event')))
assert.ok(steps.some(s=>s.source==='Legacy Claude'))
assert.ok(steps.some(s=>s.source==='Legacy AGH'))
assert.ok(steps.some(s=>s.kind==='tool'&&s.result?.body==='done'))
const markup = render(<TraceView events={events} loading={false} running={false} openByDefault={true}/>)
for(const label of ['Offload','Legacy Claude','Legacy AGH',timestamp,'tool_call','timeout','timestamp not recorded']) assert.ok(markup.includes(label),label)
assert.ok(!markup.includes('DO NOT RENDER'))
assert.ok(!markup.includes('SECRET'))
assert.ok(!markup.includes('<script>'))
assert.ok(markup.includes('&lt;script&gt;'))
assert.ok(markup.includes('[overflow-wrap:anywhere]'))
const meta = {operator:'implement',repo:'owner/repo',issue_number:549,status:'failed',started_at:'2026-10-30T00:00:00Z',ended_at:'2026-10-30T00:05:00Z',elapsed_ms:1234,attempt_id:'retry-1',offload_run_id:'offload-1',caller_run_id:'caller-1',session_id:'same-session',execution_status:'timeout',exit_code:124,error_kind:'timeout',profile:'profile',model:'model',machine:'machine',usage:{duration_ms:9999,total_cost_usd:null,input_tokens:null,output_tokens:null}}
assert.equal(elapsedEvidence(meta),'1s (stored Offload elapsed)')
assert.equal(elapsedEvidence({...meta,elapsed_ms:0}),'0ms (stored Offload elapsed)')
assert.equal(elapsedEvidence({...meta,elapsed_ms:null}),'5m0s (fallback: run timestamps)')
assert.equal(elapsedEvidence({...meta,elapsed_ms:null,ended_at:undefined}),'Not reported')
assert.equal(elapsedEvidence({...meta,offload_run_id:undefined,execution_status:undefined,elapsed_ms:null,ended_at:undefined}),'9s (fallback: legacy usage)')
assert.equal(elapsedEvidence({...meta,elapsed_ms:null,ended_at:undefined,usage:undefined}),'Not reported')
const retry = {...meta,attempt_id:'retry-2',offload_run_id:'offload-2',execution_status:'ok',exit_code:0}
const evidence = render(<><ExecutionEvidence meta={meta}/><ExecutionEvidence meta={retry}/></>)
for(const label of ['retry-1','retry-2','offload-1','offload-2','profile','model','machine','timeout','exit: 0']) assert.ok(evidence.includes(label),label)
assert.equal((evidence.match(/same-session/g)||[]).length,2)
const unknown = render(<UsagePanel meta={meta}/>)
assert.ok(unknown.includes('Not reported'))
assert.ok(!unknown.includes('$0'))
assert.ok(!unknown.includes('NaN'))
const zero = render(<UsagePanel meta={{...meta,usage:{total_cost_usd:0,input_tokens:0,output_tokens:0,cost_is_estimate:true,cost_basis:'per_run'}}}/>)
assert.ok(zero.includes('$0.0000 (estimate)'))
assert.ok(zero.includes('per_run'))
assert.ok(!render(<UsagePanel meta={{...meta,availability:{cost:false,tokens:false},usage:{total_cost_usd:0,input_tokens:0}}}/>).includes('$0'))
console.log('PASS: Offload, legacy Claude/AGH, malformed/unknown, timeout, same-session attempts, nullable usage, elapsed fallbacks, escaped text')
`},bundle:true,platform:'node',format:'cjs',outfile:join(dir,'test.cjs'),plugins:[{name:'router-stub',setup(b){
    b.onResolve({filter:/^@tanstack\/react-router$/},()=>({path:'router',namespace:'stub'}))
    b.onLoad({filter:/.*/,namespace:'stub'},()=>({contents:'export const createFileRoute = () => options => options; export const Link = () => null'}))
  }}]})
  process.stdout.write(execFileSync(process.execPath,[join(dir,'test.cjs')]))
  const source = await readFile(join(root,'src/routes/_app.runs.$slug.$issue.$ts.tsx'),'utf8')
  assert.ok(source.includes('traceEnabled ? fetch'))
  assert.ok(source.includes('tracePath === basePath'))
  assert.ok(source.includes('controller.abort()'))
  assert.ok(source.includes('workDetailKey(repo, Number(issueNum))'))
  assert.ok(!source.includes('dangerouslySetInnerHTML'))
} finally { await rm(dir,{recursive:true,force:true}) }
