// Build first. Headless, intercepted fixtures only; no desktop/provider access.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({root,configFile:false,preview:{host:'127.0.0.1',port:0}})
let browser
const long = 'very-long-evidence-'.repeat(400)
const meta = {operator:'implement',repo:'owner/repo',issue_number:549,issue_title:long,status:'failed',started_at:'2026-10-30T00:00:00Z',ended_at:'2026-10-30T00:01:00Z',elapsed_ms:1234,attempt_id:'attempt-2',offload_run_id:'offload-2',caller_run_id:'caller-2',session_id:'same-session',profile:long,model:'model',machine:'fixture',execution_status:'timeout',exit_code:124,error_kind:'timeout',usage:{total_cost_usd:null,input_tokens:null,output_tokens:null},summary:'<script>window.injected=true</script>\n'+long}
const events = [
 {schema_version:1,type:'progress',timestamp:'2026-10-30T00:00:01Z',run_id:'offload-2',native_type:'tool_call',tool_category:'Read',text:'PRIVATE_UNPROJECTED'},
 {schema_version:1,type:'final',timestamp:'2026-10-30T00:00:02Z',run_id:'offload-2',status:'timeout',exit_code:124,error_kind:long},
 {type:'assistant',message:{content:[{type:'text',text:'<img src=x onerror="window.injected=true">'+long}]}},
 {version:1,type:'result',result:{status:'completed',text:'<script>window.injected=true</script>'+long}}
]
try {
  browser = await chromium.launch({headless:true})
  for (const width of [1440,390]) {
    const context = await browser.newContext({viewport:{width,height:1000}})
    const page = await context.newPage()
    const requests = []
    const origin = new URL(server.resolvedUrls.local[0])
    await page.route('**/*',route=>{
      const url = new URL(route.request().url())
      if(url.origin!==origin.origin) return route.abort()
      if(!url.pathname.startsWith('/api/')&&!url.pathname.startsWith('/data/')) return route.continue()
      requests.push(url.pathname+url.search)
      if(url.pathname.endsWith('/events.jsonl')) return route.fulfill({body:events.map(e=>JSON.stringify(e)).join('\n')+'\nmalformed\nnull\n'})
      const data = {'/api/runs':[meta],'/api/session':{readonly:false},'/data/repos.json':[]}[url.pathname]
      return route.fulfill({status:data?200:404,json:data||{}})
    })
    await page.goto(new URL('/runs/owner__repo/issue-549/attempt-2',origin).href)
    await page.getByText('1s (stored Offload elapsed)',{exact:false}).first().waitFor()
    assert.equal(requests.some(r=>r.includes('events.jsonl')),false)
    const back = page.getByRole('link',{name:'Back to Work details'})
    const url = new URL(await back.getAttribute('href'),origin)
    assert.deepEqual(JSON.parse(url.searchParams.get('detail')),['owner/repo',549])
    await page.getByRole('button',{name:'Load execution events / legacy transcript'}).click()
    await page.getByText('Progress · tool_call · tool: Read',{exact:true}).waitFor()
    assert.ok(requests.some(r=>r.includes('events.jsonl')))
    await page.getByRole('button',{name:/Execution finished/}).click()
    await page.getByRole('button',{name:/<img src=x/}).click()
    await page.getByRole('button',{name:/AGH completed/}).click()
    assert.equal(await page.evaluate(()=>window.injected),undefined)
    assert.equal(await page.locator('img[src="x"]').count(),0)
    assert.ok(!(await page.locator('body').innerText()).includes('PRIVATE_UNPROJECTED'))
    assert.ok(!(await page.locator('body').innerText()).includes('$0.0000'))
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
    await page.screenshot({path:join(process.env.TMPDIR || '/tmp',`clawflow-run-detail-${width}.png`),fullPage:true})
    await context.close()
  }
  console.log('PASS: 1440/390px isolated run render; opt-in events, Work link, escaped text and no horizontal overflow')
} finally {await browser?.close(); await server.httpServer.close()}
