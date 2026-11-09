import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium } from 'playwright'
import { runAcceptance } from './release-perf-acceptance.mjs'

test('typing timing captures a real composer keydown and keeps focus setup separate', async () => {
 const { captureTypingKeydown, typingMeasurement } = await import('./release-perf-acceptance.mjs')
 const browser = await chromium.launch()
 try {
  const page = await browser.newPage()
  await page.setContent('<input aria-label="Other"><div class="agent-composer"><textarea onfocus="performance.mark(\'focus\')" oninput="performance.mark(\'committed\')"></textarea></div>')
  const stop = await captureTypingKeydown(page)
  const commandAt = await page.evaluate(() => Date.now())
  await page.locator('textarea').press('x')
  const keydownAt = await stop()
  const marks = await page.evaluate(() => Object.fromEntries(performance.getEntriesByType('mark').map(m => [m.name, performance.timeOrigin + m.startTime])))
  const result = typingMeasurement(commandAt, keydownAt, marks.committed)
  assert.ok(marks.focus <= keydownAt)
  assert.ok(result.clientMs >= 0)
  assert.ok(result.preKeydownMs >= 0)
  assert.ok(Math.abs(result.commandToCommitMs - result.clientMs - result.preKeydownMs) < 0.001)
  const other = await captureTypingKeydown(page)
  await page.locator('input').press('x')
  assert.equal(await other(), null)
 } finally { await browser.close() }
})

test('boot/soak driver uses native input, persists process samples, and redacts requests', { timeout: 20_000 }, async () => {
 const directory = await mkdtemp(join(tmpdir(), 'acceptance-browser-'))
 const server = createServer((request, response) => {
  if(request.url.startsWith('/api/')) {response.setHeader('Content-Type','application/json');response.end('{}');return}
  response.setHeader('Content-Type','text/html')
  response.end(`<button role="tab" data-tab-id="chat" aria-selected="true">Main</button>
   <button title="Release-perf workspace 0">Workspace 0</button><button title="Release-perf workspace 1">Workspace 1</button>
   <div class="session-agent-pane"><div class="session-transcript-pane"><div data-event-id="fixture-row">Ready</div></div></div>
   <div class="agent-composer"><textarea oninput="fetch('/api/sessions/fixture/live?token=fixture_canary')"></textarea></div>
   <script>performance.mark('transcript-rendered')</script>`)
 })
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
 const browser=await chromium.launch()
 try {
  const context=await browser.newContext()
  const page=await context.newPage()
  const origin=`http://127.0.0.1:${server.address().port}`
  await page.goto(origin+'/sessions/fixture')
  const output=join(directory,'result.json')
  const result=await runAcceptance(page,context,{seconds:0.8,intervalSeconds:0.1,pids:{runner:process.pid,daemon:process.pid},runnerURL:origin,longSession:'fixture',output})
  assert.equal(result.status,'complete')
  assert.equal(result.boot.samplesMs.length,3)
  assert.ok(result.soak.durationSeconds>=0.8)
  assert.ok(result.soak.samples.length>=2)
  assert.ok(result.soak.nativeInputMs.length>=5)
  assert.ok(result.soak.processes.daemon)
  assert.ok(result.soak.firstCycleCpu.length)
  assert.ok(result.soak.requestFamilies.live.count>0)
  assert.equal((await readFile(output,'utf8')).includes('fixture_canary'),false)
  const failedOutput=join(directory,'failed.json')
  await assert.rejects(runAcceptance(page,context,{seconds:1,pids:{},output:failedOutput}),/acceptance_measurement_failed/)
  assert.equal(JSON.parse(await readFile(failedOutput,'utf8')).status,'incomplete')
 } finally {
  await browser.close()
  server.closeAllConnections()
  await new Promise(resolve=>server.close(resolve))
  await rm(directory,{recursive:true,force:true})
 }
})

test('blackboard repetitions close the current Blackboard tab before reopening', async () => {
 const browser = await chromium.launch()
 try {
  const page = await browser.newPage()
  await page.setContent('<button aria-label="Close Blackboard" onclick="this.remove()">Close</button>')
  const { closeBlackboardTab } = await import('./release-perf-acceptance.mjs')
  await closeBlackboardTab(page)
  assert.equal(await page.getByRole('button', { name: 'Close Blackboard', exact: true }).count(), 0)
 } finally { await browser.close() }
})

test('three cache-disabled boot trials work for a release without built-in performance marks', async () => {
 const server = createServer((_request, response) => {
  response.setHeader('Content-Type', 'text/html')
  response.end('<button role="tab" data-tab-id="chat" aria-selected="true">Chat<svg aria-label="Pinned"></svg></button><div class="session-agent-pane"><div class="session-transcript-pane"><div data-testid="tool-group">Ran 4000 tool calls</div></div></div>')
 })
 await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
 const browser = await chromium.launch()
 try {
  const context = await browser.newContext(), page = await context.newPage()
  const { runBootTrials } = await import('./release-perf-acceptance.mjs')
  const boot = await runBootTrials(page, context, { runnerURL: `http://127.0.0.1:${server.address().port}`, longSession: 'fixture' })
  assert.equal(boot.samplesMs.length, 3)
  assert.equal(boot.navigation.length, 3)
  assert.ok(boot.samplesMs.every(value => value > 0 && value < 30_000))
 } finally {
  await browser.close(); server.closeAllConnections()
  await new Promise(resolve => server.close(resolve))
 }
})
