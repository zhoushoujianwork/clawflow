// Isolated headless fixture; all API/data reads are synthetic and writes are rejected.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
import { workListFixture } from './work-list-fixture.mjs'
import { workDetailFixture } from './work-detail-fixture.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({root,configFile:false,preview:{host:'127.0.0.1',port:0}})
let browser
try {
  browser = await chromium.launch({headless:true})
  for (const width of [1440,390]) {
    const {repos,queue} = workListFixture()
    let board = {...workDetailFixture().board,repos,global_queue:queue}
    const context = await browser.newContext({viewport:{width,height:1000}})
    await context.addInitScript(() => localStorage.setItem('clawflow.pipeline',JSON.stringify({tab:'all',work:'delivered',q:'stored filter'})))
    const page = await context.newPage()
    const origin = new URL(server.resolvedUrls.local[0])
    await page.route('**/*',route=>{
      const url = new URL(route.request().url())
      if(url.origin!==origin.origin) return route.abort()
      if(!url.pathname.startsWith('/api/')&&!url.pathname.startsWith('/data/')) return route.continue()
      assert.equal(route.request().method(),'GET')
      const data = {'/api/pipeline':board,'/api/runs':[], '/api/session':{readonly:true},'/data/repos.json':[],
        '/data/projects.json':[{name:'Platform',repos:repos.slice(0,2).map(r=>r.repo)}]}[url.pathname]
      return route.fulfill({status:data?200:404,json:data||{}})
    })
    await page.goto(new URL('/pipeline',origin).href)
    const list = page.getByRole('region',{name:'Work list',exact:true})
    const controls = page.getByRole('region',{name:'Work filters'})
    await list.getByText('1–50 of 400 items',{exact:true}).waitFor()
    assert.equal(await controls.getByRole('searchbox').inputValue(),'')
    await controls.getByLabel('Project',{exact:true}).selectOption('Platform')
    await list.getByText('1–16 of 16 items',{exact:true}).waitFor()
    await controls.getByLabel('Stage',{exact:true}).selectOption('implementing')
    await controls.getByLabel('Role',{exact:true}).selectOption('implement')
    await list.getByText('1–2 of 2 items',{exact:true}).waitFor()
    await controls.getByLabel('Repository',{exact:true}).selectOption(repos[0].repo)
    await controls.getByRole('searchbox').fill('#1')
    await controls.getByLabel('Order',{exact:true}).selectOption('age')
    await list.getByText('1–1 of 1 items',{exact:true}).waitFor()
    const filteredURL = page.url()
    await page.reload()
    await list.getByText('1–1 of 1 items',{exact:true}).waitFor()
    assert.equal(await controls.getByRole('searchbox').inputValue(),'#1')
    await list.getByRole('button',{name:'Details · 2 execution attempts'}).click()
    const panel = page.getByRole('dialog')
    await panel.waitFor()
    const detail = new URL(page.url()).searchParams.get('detail')
    // Simulate a URL filter change while detail is open (native modal makes the background inert).
    await controls.getByRole('searchbox').evaluate(el=>{
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set
      setter.call(el,'no matching title'); el.dispatchEvent(new Event('input',{bubbles:true}))
    })
    await list.getByText('No items in this view. 0 results.',{exact:true}).waitFor()
    assert.equal(new URL(page.url()).searchParams.get('detail'),detail)
    assert.equal(await panel.isVisible(),true)
    await page.goBack()
    await list.getByText('1–1 of 1 items',{exact:true}).waitFor()
    assert.equal(await panel.isVisible(),true)
    await page.goForward()
    await list.getByText('No items in this view. 0 results.',{exact:true}).waitFor()
    await panel.getByRole('button',{name:'Close details'}).click()
    await controls.getByRole('button',{name:'Clear all filters'}).click()
    await list.getByText('1–50 of 400 items',{exact:true}).waitFor()
    await controls.getByLabel('Order',{exact:true}).selectOption('updated')
    const keys = await list.locator('[data-work-key]').evaluateAll(els=>els.map(e=>e.dataset.workKey))
    await list.locator('[data-work-key]').nth(5).scrollIntoViewIfNeeded()
    const anchor = await list.locator('[data-work-key]').nth(5).evaluate(el=>({key:el.dataset.workKey,top:el.getBoundingClientRect().top}))
    // Reorder provider input between polls. Sorting and scroll must stay stable.
    board = {...board,repos:[...repos].reverse().map(r=>({...r,items:[...r.items].reverse()}))}
    await page.clock.install()
    const refreshed = page.waitForResponse(r=>new URL(r.url()).pathname==='/api/pipeline')
    await page.clock.fastForward(31_000)
    await refreshed
    await page.waitForFunction(() => !document.querySelector('button[aria-label="Refresh pipeline"]')?.disabled)
    assert.deepEqual(await list.locator('[data-work-key]').evaluateAll(els=>els.map(e=>e.dataset.workKey)),keys)
    assert.ok(Math.abs(await list.locator('[data-work-key]').nth(5).evaluate(el=>el.getBoundingClientRect().top)-anchor.top)<2)
    // Removed selection keeps its identity and offers the existing missing-item state.
    await list.getByRole('button',{name:'Details · 2 execution attempts'}).first().click()
    await panel.waitFor()
    const removedDetail = new URL(page.url()).searchParams.get('detail')
    const [repo,issue] = JSON.parse(removedDetail)
    board = {...board,repos:board.repos.map(r=>({...r,items:r.repo===repo?r.items.filter(i=>i.issue!==issue):r.items}))}
    await page.clock.fastForward(31_000)
    await panel.getByText(/not in the current snapshot/).waitFor()
    assert.equal(new URL(page.url()).searchParams.get('detail'),removedDetail)
    await panel.getByRole('button',{name:'Close details'}).click()
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
    board = {...board,repos,global_queue:queue}
    // Invalid and legacy All input retains the bounded active default.
    await page.goto(new URL('/pipeline?tab=all&work=invalid&sort=invalid&stage=invalid',origin).href)
    await list.getByText('1–50 of 400 items',{exact:true}).waitFor()
    assert.equal(await list.locator('[data-stage="merged"]').count(),0)
    assert.equal(await controls.getByLabel('Order',{exact:true}).inputValue(),'queue')
    assert.equal(await controls.getByLabel('Stage',{exact:true}).inputValue(),'')
    await page.goto(filteredURL)
    await list.getByText('1–1 of 1 items',{exact:true}).waitFor()
    await context.close()
  }
  console.log('PASS: 1440/390px URL reload/back/forward, all controls/counts/clear, open detail filter change, polling order/scroll, removed selection, invalid URL and stored All safety')
} finally { await browser?.close(); await server.httpServer.close() }
