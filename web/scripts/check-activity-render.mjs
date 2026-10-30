// Isolated headless fixture. Every data request is intercepted; no live services.
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { preview } from 'vite'
import { activityFixture } from './activity-fixture.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const server = await preview({root,configFile:false,preview:{host:'127.0.0.1',port:0}})
let browser
try {
  browser = await chromium.launch({headless:true,...(process.env.CHROMIUM_EXECUTABLE_PATH ? {executablePath:process.env.CHROMIUM_EXECUTABLE_PATH} : {})})
  for (const width of [1440,390]) {
    const context = await browser.newContext({viewport:{width,height:1000}})
    const page = await context.newPage()
    const origin = new URL(server.resolvedUrls.local[0])
    let board = activityFixture()
    const requests = []
    await page.route('**/*', route => {
      const req = route.request()
      const url = new URL(req.url())
      if (url.origin !== origin.origin) return route.abort()
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/data/')) return route.continue()
      requests.push(req.method()+' '+url.pathname)
      const data = {'/api/pipeline':board,'/api/session':{readonly:true},'/data/repos.json':[],'/data/projects.json':[]}[url.pathname]
      return route.fulfill({status:data?200:404,json:data||{}})
    })
    await page.goto(new URL('/pipeline',origin).href)
    const timeline = page.getByLabel('Activity timeline',{exact:true})
    await timeline.waitFor()
    assert.equal(await timeline.getAttribute('open'),null)
    await timeline.locator('summary').focus()
    await page.keyboard.press('Enter')
    assert.notEqual(await timeline.getAttribute('open'),null)
    assert.equal(await timeline.locator('li').count(),50)
    await timeline.getByRole('button',{name:'Load 50 more activity groups'}).click()
    assert.ok(await timeline.locator('li').count()>50)
    assert.equal(await timeline.getByText('execution/failed',{exact:true}).count(),1)
    const failed = timeline.locator('li').filter({has:page.getByText('execution/failed',{exact:true})})
    assert.ok((await failed.getByRole('link').getAttribute('href')).endsWith('/failed-attempt'))
    assert.equal(await timeline.getByText('execution/success',{exact:true}).count(),1)
    await timeline.getByLabel('Event importance').selectOption('all')
    await timeline.getByRole('button',{name:'Load 50 more activity groups'}).click()
    assert.ok((await timeline.innerText()).includes('×3 since'))
    await timeline.getByLabel('Event repository').selectOption('fixture/repo')
    await timeline.getByLabel('Activity issue').fill('2')
    await timeline.getByText('No matching activity.').waitFor()
    await timeline.getByLabel('Activity issue').fill('1')
    await timeline.locator('li').first().waitFor()
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
    await timeline.getByLabel('Activity issue').fill('')
    await timeline.getByLabel('Event repository').selectOption('')
    // Change only existing source-health metadata and explicitly refresh this fixture.
    board = {...board,sources:[{source:'execution',last_success:null,stale:true}]}
    await page.getByRole('button',{name:'Refresh pipeline',exact:true}).click()
    await timeline.getByText('source/unavailable (observed)',{exact:true}).waitFor()
    board = {...board,sources:[{source:'execution',last_success:board.generated_at,stale:false}]}
    await page.getByRole('button',{name:'Refresh pipeline',exact:true}).click()
    await timeline.getByText('source/recovered (observed)',{exact:true}).waitFor()
    assert.ok((await timeline.innerText()).includes('Source: execution'))
    await timeline.locator('summary').click()
    assert.equal(await timeline.getAttribute('open'),null)
    assert.ok(requests.every(r=>r.startsWith('GET ')))
    await context.close()
  }
  console.log('PASS: Activity at 1440/390px, default collapse, filters, pagination, terminal attempts, recovery, links, overflow and read-only requests')
} finally {
  await browser?.close()
  await server.httpServer.close()
}
