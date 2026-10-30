// Isolated headless UI fixture. Build first. All API requests are intercepted.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
import { workDetailFixture } from './work-detail-fixture.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({root,configFile:false,preview:{host:'127.0.0.1',port:0}})
const fixture = workDetailFixture()
let browser
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
      let data = {'/api/pipeline':fixture.board,'/api/session':{readonly:true},'/data/repos.json':[]}[url.pathname]
      if(url.pathname==='/api/runs') {
        assert.equal(url.searchParams.get('repo'),'owner/repo')
        assert.equal(url.searchParams.get('limit'),'100')
        assert.equal(url.searchParams.has('path'),false)
        assert.equal(url.searchParams.has('attempt_id'),false)
        data = url.searchParams.has('cursor') ? [] : fixture.history
        return route.fulfill({json:data,headers:url.searchParams.has('cursor')?{}:{'X-Next-Cursor':'fixture-page-2'}})
      }
      return route.fulfill({status:data?200:404,json:data||{}})
    })
    const base = new URL('/pipeline?work=waiting&events=all&repo=owner%2Frepo',origin).href
    await page.goto(base)
    const open = page.getByRole('button',{name:/Details · 4 execution attempts/})
    await open.waitFor()
    assert.equal(requests.some(r=>r.startsWith('/api/runs')),false)
    await open.focus()
    await page.keyboard.press('Enter')
    const panel = page.getByRole('dialog')
    await panel.waitFor()
    await panel.getByText(/\$0.01/).first().waitFor()
    const deepLink = page.url()
    assert.ok(new URL(deepLink).searchParams.has('detail'))
    assert.equal(await panel.locator('[data-attempt-id]').count(),4)
    assert.deepEqual(await panel.locator('[data-attempt-id]').evaluateAll(els=>els.map(e=>e.dataset.attemptId)),['legacy','failed-first','repair-success','review'])
    assert.ok((await panel.innerText()).includes('Latest attempt · reviewer'))
    assert.ok((await panel.innerText()).includes('same-session'))
    assert.ok((await panel.innerText()).includes('independent-review'))
    assert.ok((await panel.innerText()).includes('Cost: unknown'))
    assert.ok((await panel.innerText()).includes('No merge recorded'))
    assert.ok((await panel.innerText()).includes('passed (stale)'))
    assert.ok((await panel.innerText()).includes('bbbbbbb'))
    assert.ok((await panel.innerText()).includes('CI observation time: unknown'))
    assert.equal(await panel.getByRole('link',{name:'Open existing run details (loads transcript)'}).count(),3)
    assert.ok((await panel.getByRole('link',{name:'Open existing run details (loads transcript)'}).first().getAttribute('href')).includes('/runs/owner__repo/issue-541/failed-first'))
    assert.ok(await panel.evaluate(el=>el.scrollWidth<=el.clientWidth))
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
    // Native modal traps Tab and supports Escape, then restores the opener.
    for(let i=0;i<9;i++) await page.keyboard.press('Tab')
    assert.equal(await panel.evaluate(el=>el.contains(document.activeElement)),true)
    await page.keyboard.press('Escape')
    await panel.waitFor({state:'hidden'})
    assert.equal(await open.evaluate(el=>el===document.activeElement),true)
    for(const [key,value] of [['work','waiting'],['events','all'],['repo','owner/repo']]) assert.equal(new URL(page.url()).searchParams.get(key),value)
    await page.goBack()
    await panel.waitFor()
    await page.goBack()
    await panel.waitFor({state:'hidden'})
    assert.equal(await open.evaluate(el=>el===document.activeElement),true)
    await page.goForward()
    await panel.waitFor()
    await page.reload()
    await panel.waitFor()
    assert.equal(new URL(page.url()).searchParams.get('work'),'waiting')
    await panel.getByRole('button',{name:/Load more repository history/}).click()
    await panel.getByText(/End of available history/).waitFor()
    assert.ok(requests.every(r=>!r.includes('events.jsonl')&&!r.includes('transcript')&&!r.includes('path=')))
    await panel.getByRole('button',{name:'Close details'}).click()
    await panel.waitFor({state:'hidden'})
    // Deep links survive a missing/removed snapshot item without selecting another issue.
    const missing = new URL(deepLink)
    missing.searchParams.set('detail',JSON.stringify(['owner/repo',999]))
    await page.goto(missing.href)
    await panel.getByText(/not in the current snapshot/).waitFor()
    assert.equal(await panel.locator('[data-attempt-id]').count(),0)
    await page.reload()
    await panel.getByText(/not in the current snapshot/).waitFor()
    assert.equal(await panel.locator('[data-attempt-id]').count(),0)
    for(const [key,value] of [['work','waiting'],['events','all'],['repo','owner/repo']]) assert.equal(new URL(page.url()).searchParams.get(key),value)
    await context.close()
  }
  console.log('PASS: 1440/390px; keyboard focus/trap/close, URL reload/back/forward with filters, retries, independent review, legacy fields, changed head, no merge, bounded metadata pagination, no raw transcript requests')
} finally { await browser?.close(); await server.httpServer.close() }
