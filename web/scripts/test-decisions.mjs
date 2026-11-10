// Headless rendering fixtures for the decisions list. Run: node web/scripts/test-decisions.mjs
import { createRequire } from 'node:module'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
const require = createRequire(import.meta.url)
const { build } = createRequire(require.resolve('vite'))('esbuild')
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dir = await mkdtemp(join(tmpdir(), 'decisions-test-'))
try {
  await build({ stdin: { resolveDir: root, loader: 'tsx', contents: `
import React from 'react'
import assert from 'node:assert/strict'
import { renderToStaticMarkup as render } from 'react-dom/server'
import { DecisionsView, safeHref, KIND_LABELS } from './src/components/Decisions'
const base = { reason: 'because', action: 'do this' }
const report = { decisions: [
  { id: 'a', kind: 'execution_config', title: 'Execution setup: w1', ...base, url: 'https://example.com/guide' },
  { id: 'b', kind: 'needs_confirmation', repo: 'o/r', issue: 11, title: 'ambiguous', ...base, url: 'https://github.com/o/r/issues/11', issue_url: 'https://github.com/o/r/issues/11' },
  { id: 'c', kind: 'owner_skipped', repo: 'o/r', issue: 12, title: 'parked', ...base, url: 'https://github.com/o/r/issues/12', issue_url: 'https://github.com/o/r/issues/12' },
  { id: 'd', kind: 'acceptance_blocked', repo: 'o/r', issue: 13, pr: 130, title: 'review', ...base, url: 'https://github.com/o/r/pull/130', issue_url: 'https://github.com/o/r/issues/13' },
  { id: 'e', kind: 'repair_stopped', repo: 'o/r', issue: 14, pr: 140, title: 'failing', ...base, url: 'https://github.com/o/r/pull/140', issue_url: 'https://github.com/o/r/issues/14' },
] }
const html = render(<DecisionsView report={report} />)
assert.ok(html.includes('Needs your decision (5)'))
for (const kind of Object.keys(KIND_LABELS)) assert.ok(html.includes('data-decision-kind="' + kind + '"'), kind)
for (const href of ['https://example.com/guide', 'https://github.com/o/r/issues/11', 'https://github.com/o/r/issues/12', 'https://github.com/o/r/pull/130', 'https://github.com/o/r/pull/140', 'https://github.com/o/r/issues/13', 'https://github.com/o/r/issues/14']) {
  assert.ok(html.includes('href="' + href + '"'), href)
}
assert.ok(html.includes('o/r PR #130') && html.includes('o/r #11'))
assert.ok(html.includes('What to do: ') && html.includes('do this'))
// A PR link and the same-issue link are not repeated.
assert.equal(html.split('href="https://github.com/o/r/issues/11"').length - 1, 1)

// Empty list: explicit empty state, no heading.
const empty = render(<DecisionsView report={{ decisions: [] }} />)
assert.ok(empty.includes('Nothing needs your decision'))
assert.ok(!empty.includes('Needs your decision ('))
assert.ok(!empty.includes('may hide items'))

// An empty list with unread sources must say so rather than imply all clear.
const gap = render(<DecisionsView report={{ decisions: [], incomplete: [{ source: 'o/r', reason: 'boom' }] }} />)
assert.ok(gap.includes('Nothing needs your decision') && gap.includes('may hide items') && gap.includes('o/r: boom'))

// Loading, failure, and last-known data on a failed refresh.
assert.ok(render(<DecisionsView report={null} />).includes('Loading decisions'))
const failed = render(<DecisionsView report={null} error="HTTP 503" />)
assert.ok(failed.includes('role="alert"') && failed.includes('HTTP 503') && !failed.includes('Nothing needs your decision'))
const stale = render(<DecisionsView report={report} error="HTTP 503" />)
assert.ok(stale.includes('showing last known list') && stale.includes('Needs your decision (5)'))

// Links are http(s) only; other schemes render as no link.
assert.equal(safeHref('javascript:alert(1)'), undefined)
assert.equal(safeHref(undefined), undefined)
assert.equal(safeHref('https://x.test/a'), 'https://x.test/a')
const unsafe = render(<DecisionsView report={{ decisions: [{ id: 'x', kind: 'owner_skipped', title: 't', ...base, url: 'javascript:alert(1)' }] }} />)
assert.ok(!unsafe.includes('javascript:'))
console.log('PASS: decisions list renders every kind, links, empty/gap/error states')
` }, bundle: true, platform: 'node', format: 'cjs', outfile: join(dir, 'test.cjs'), jsx: 'automatic', logLevel: 'error' })
  execFileSync(process.execPath, [join(dir, 'test.cjs')], { stdio: 'inherit' })
} finally {
  await rm(dir, { recursive: true, force: true })
}
