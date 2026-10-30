// Isolated render fixtures: all API/data calls are intercepted; no live services.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
import { capacityFixtures } from './capacity-fixture.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({root,configFile:false,preview:{host:'127.0.0.1',port:0}})
let browser
try {
  browser = await chromium.launch({headless:true})
  for (const width of [1440,390]) {
    const context = await browser.newContext({viewport:{width,height:1000}})
    const page = await context.newPage()
    const origin = new URL(server.resolvedUrls.local[0])
    let snapshot
    const requests = []
    await page.route('**/*', route => {
      const req = route.request()
      const url = new URL(req.url())
      if (url.origin !== origin.origin) return route.abort()
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
      requests.push(req.method() + ' ' + url.pathname)
      const data = {'/api/pipeline':snapshot,'/api/session':{readonly:true},'/data/repos.json':[],'/data/projects.json':[]}[url.pathname]
      return route.fulfill({status:data?200:404,json:data||{}})
    })
    for (const [name, board] of Object.entries(capacityFixtures())) {
      snapshot = board
      await page.goto(new URL('/pipeline',origin).href)
      const capacity = page.getByLabel('Execution capacity',{exact:true})
      await capacity.waitFor()
      assert.equal(await capacity.getAttribute('open'),null)
      assert.equal(await page.getByLabel('Worker pool',{exact:true}).count(),0)
      assert.equal(await page.getByLabel('Worker diagnostics',{exact:true}).isVisible(),false)
      assert.equal(await page.getByText('Coordinator disabled',{exact:true}).isVisible(),false)
      const text = await capacity.locator('summary').innerText()
      assert.ok(text.includes('Local agent slots:'))
      assert.ok(text.includes('Coordinator queued tasks:'))
      assert.ok(text.includes('Connected workers:'))
      if(name==='mixed') {
        assert.ok(text.includes('Coordinator queued tasks: 1'))
        assert.ok(text.includes('Connected workers: 1'))
      }
      if(name==='full') assert.ok(text.includes('2 / 2 · full'))
      if(['disabled','local','stale','unknown'].includes(name)) assert.ok(text.includes('Connected workers: unknown'))
      await capacity.locator('summary').focus()
      await page.keyboard.press('Enter')
      assert.notEqual(await capacity.getAttribute('open'),null)
      if(name==='local'||name==='mixed') assert.ok((await capacity.innerText()).includes('Local active work items (Offload): 1'))
      if(name==='lost') {
        assert.ok((await capacity.innerText()).includes('heartbeat lost'))
        assert.ok((await capacity.innerText()).includes('heartbeat unknown'))
      }
      if(name==='stale') assert.ok((await capacity.innerText()).includes('connection unknown (stale snapshot)'))
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
      await page.keyboard.press('Enter')
      assert.equal(await page.getByLabel('Worker diagnostics',{exact:true}).isVisible(),false)
      if(name==='disabled') {
        await page.locator('summary').filter({hasText:'System status'}).click()
        assert.equal(await page.getByText('Coordinator disabled',{exact:true}).isVisible(),true)
        assert.equal(await page.getByText('Coordinator disabled',{exact:true}).getAttribute('role'),null)
      }
    }
    assert.ok(requests.every(r=>r.startsWith('GET ')))
    await context.close()
  }
  console.log('PASS: capacity fixtures at 1440/390px, collapsed default, keyboard details, diagnostics-only disabled, overflow and read-only requests')
} finally {
  await browser?.close()
  await server.httpServer.close()
}
