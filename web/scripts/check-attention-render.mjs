// Isolated synthetic API fixture only. Build web first; never connects to providers.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0 } })
const now = '2026-10-30T00:00:00Z'
const items = [527, 496, 511, 101, 102, 103, 104].map(issue => ({issue,title:`Fixture #${issue}: unreadable CI and exhausted repair`,state:'open',stage:'blocked',why:'CI status could not be read',attention:['CI status could not be read: unavailable','repair exhausted: 3 total attempts'],evidence:[],pr:{number:issue+1000,state:'open',ci:'unknown',ci_error:'unavailable'},executions:[{run_path:`./data/runs/owner__repo/issue-${issue}/fixture/`,started_at:now,status:'failed',provenance:{source:'offload_final',stale:false}}]}))
const repo = {repo:'owner/repo',enabled:true,items,error:'VCS unavailable',stale:true}
let board = { repos:[repo], generated_at:now, stale:false, refreshing:false, events:Array.from({length:30},()=>({time:now,event:'run/lock_timeout',fields:{repo:repo.repo,issue:'527',reason:'busy'}})), runtime:{version:'fixture',scheduler:{status:'idle'},execution:{ready:true}} }
let browser
try {
  browser = await chromium.launch({headless:true})
  for (const width of [1440,390]) {
    const context = await browser.newContext({viewport:{width,height:1000}})
    const page = await context.newPage()
    const origin = new URL(server.resolvedUrls.local[0])
    await page.route('**/*', route => {
      const url = new URL(route.request().url())
      if (url.origin !== origin.origin) return route.abort()
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
      const data = {'/api/pipeline':board,'/api/session':{readonly:true},'/data/repos.json':[]}[url.pathname]
      return route.fulfill({status:data?200:404,json:data || {}})
    })
    await page.goto(new URL('/pipeline',origin).href)
    const attention = page.getByRole('region',{name:'Needs attention',exact:true})
    await attention.getByRole('heading',{name:'Needs attention (8)',exact:true}).waitFor()
    assert.equal(await attention.locator('[data-incident-key]').count(),5)
    await attention.getByRole('button',{name:'Show more (3)',exact:true}).click()
    assert.equal(await attention.locator('[data-incident-key]').count(),8)
    for (const issue of [527,496,511]) {
      const card = attention.locator('[data-incident-key]').filter({has:page.locator('strong').filter({hasText:`Fixture #${issue}:`})})
      assert.equal(await card.count(),1)
      await card.locator('summary').click()
      assert.equal(await card.getByText('CI status could not be read: unavailable',{exact:true}).isVisible(),true)
      assert.ok((await card.getByRole('link',{name:'View issue',exact:true}).getAttribute('href')).endsWith(`/issues/${issue}`))
      assert.ok((await card.getByRole('link',{name:'View run',exact:true}).getAttribute('href')).includes(`/issue-${issue}/fixture`))
    }
    assert.equal(await attention.getByText('Repository source unavailable',{exact:true}).count(),1)
    assert.ok(await attention.evaluate(el=>el.scrollWidth<=el.clientWidth))
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
    await context.close()
  }
  // The same snapshot with recovered items and sources has no active incidents.
  board = {...board,events:[],repos:[{...repo,error:undefined,stale:false,items:items.map(item=>({...item,state:'closed',stage:'merged'}))}]}
  const page = await browser.newPage()
  await page.route('**/*',route=>{
    const url = new URL(route.request().url())
    const origin = new URL(server.resolvedUrls.local[0])
    if(url.origin!==origin.origin) return route.abort()
    if(!url.pathname.startsWith('/api/')&&!url.pathname.startsWith('/data/')) return route.continue()
    const data = {'/api/pipeline':board,'/api/session':{readonly:true},'/data/repos.json':[]}[url.pathname]
    return route.fulfill({status:data?200:404,json:data||{}})
  })
  await page.goto(new URL('/pipeline',server.resolvedUrls.local[0]).href)
  await page.getByText('Nothing needs you',{exact:true}).waitFor()
  console.log('PASS: attention grouping, expansion, show-more, links, resolution and wrapping at 1440/390px')
} finally {
  await browser?.close()
  await server.httpServer.close()
}
