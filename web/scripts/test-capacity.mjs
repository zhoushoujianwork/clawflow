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
import { ExecutionCapacity } from './src/components/ExecutionCapacity'
import { capacityFixtures } from './scripts/capacity-fixture.mjs'
const now = Date.now()
const fixtures = capacityFixtures(now)
const render = (key, extra={}) => renderToStaticMarkup(<ExecutionCapacity board={fixtures[key]} now={now} {...extra}/>)
for (const key of Object.keys(fixtures)) {
  const html = render(key)
  assert.ok(html.includes('Capacity'))
  assert.ok(!html.includes('<details open'))
  assert.ok(!html.includes('role="alert"'))
  assert.ok(!html.includes('Coordinator disabled'))
  assert.ok(html.includes('Remote slot capacity: unknown'))
}
const summary = html => html.slice(html.indexOf('<summary'), html.indexOf('</summary>'))
assert.ok(summary(render('disabled')).includes('Connected workers: unknown'))
assert.ok(!summary(render('disabled')).includes('No registered workers'))
assert.ok(render('local').includes('Local active work items (Offload): 1'))
assert.ok(render('mixed').includes('Local active work items (Offload): 1'))
assert.ok(summary(render('mixed')).includes('Connected workers: 1'))
assert.ok(summary(render('mixed')).includes('Coordinator queued tasks: 1'))
assert.ok(summary(render('lost')).includes('Connected workers: 2'))
assert.ok(render('lost').includes('heartbeat lost'))
assert.ok(render('lost').includes('heartbeat unknown'))
assert.ok(summary(render('full')).includes('2 / 2 · full'))
assert.ok(summary(render('stale')).includes('Connected workers: unknown'))
assert.ok(summary(render('stale')).includes('Coordinator queued tasks: unknown'))
assert.ok(render('stale').includes('Local active work items (Offload): unknown'))
assert.ok(!render('stale').includes('heartbeat recent'))
assert.ok(summary(render('unknown')).includes('Local agent slots: unknown'))
assert.ok(summary(render('full',{failed:true})).includes('Local agent slots: unknown'))
assert.ok(summary(render('mixed',{now:now+121000})).includes('Connected workers: unknown'))
const mismatch = structuredClone(fixtures.local)
mismatch.repos[0].items[0].executions[0].provenance.source_machine = 'other'
assert.ok(renderToStaticMarkup(<ExecutionCapacity board={mismatch} now={now}/>).includes('Local active work items (Offload): 0'))
console.log('PASS: capacity disabled/local/mixed/lost/full/stale/unknown fixtures, attributed activity and failed refresh')

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
