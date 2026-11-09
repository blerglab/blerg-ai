// Opt-in production boot/soak diagnostics for the foreground acceptance runner.
// Never record tokens, response bodies, query strings or raw heap snapshots.
import { readFileSync, readdirSync } from 'node:fs'
import { writeFile } from 'node:fs/promises'

export function typingMeasurement(commandAt, keydownAt, committedAt) {
  if (![commandAt, keydownAt, committedAt].every(Number.isFinite) || keydownAt < commandAt || committedAt < keydownAt) throw new Error('invalid_typing_timestamps')
  return { clientMs: committedAt - keydownAt, preKeydownMs: keydownAt - commandAt, commandToCommitMs: committedAt - commandAt }
}

// Capture the actual event before React's handlers; clean up only our listener.
export async function captureTypingKeydown(page) {
  const handle = await page.evaluateHandle(() => {
    const sample = { at: null }
    const listener = event => {
      if (sample.at !== null || event.key !== 'x' || !(event.target instanceof Element) || !event.target.matches('.agent-composer textarea')) return
      sample.at = performance.timeOrigin + event.timeStamp
    }
    document.addEventListener('keydown', listener, true)
    return { sample, stop: () => document.removeEventListener('keydown', listener, true) }
  })
  return async () => {
    try { return await handle.evaluate(({ sample, stop }) => { stop(); return sample.at }) }
    finally { await handle.dispose() }
  }
}

const median = values => {
  const sorted = [...values].sort((a, b) => a - b)
  const i = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? sorted[i] : (sorted[i - 1] + sorted[i]) / 2
}
export function growth(samples, field) {
  const values = samples.map(sample => sample[field])
  if (!values.length || values.some(value => !Number.isFinite(value))) throw new Error('invalid_memory_samples')
  const first = median(values.slice(0, 3)), last = median(values.slice(-3))
  return { first, last, delta: last - first, maximum: Math.max(...values) }
}
export function processSample(pid, root = '/proc', expectedStart) {
  if (!Number.isInteger(pid) || pid <= 0) throw new Error('invalid_process_id')
  const path = `${root}/${pid}`
  const status = readFileSync(`${path}/status`, 'utf8')
  const raw = readFileSync(`${path}/stat`, 'utf8')
  const stat = raw.slice(raw.lastIndexOf(') ') + 2).split(/\s+/)
  const startTicks = stat[19]
  if (expectedStart && startTicks !== expectedStart) throw new Error('process_replaced')
  const rss = status.match(/^VmRSS:\s+(\d+) kB$/m), threads = status.match(/^Threads:\s+(\d+)$/m)
  if (!rss || !threads || !/^\d+$/.test(startTicks ?? '')) throw new Error('invalid_process_sample')
  return { rssBytes: Number(rss[1]) * 1024, threads: Number(threads[1]), fdCount: readdirSync(`${path}/fd`).length, startTicks }
}
export function cpuSummary(profile) {
  const nodes = new Map((profile?.nodes ?? []).map(node => [node.id, node.callFrame]))
  const samples = new Map()
  for (let i = 0; i < (profile?.samples?.length ?? 0); i++) {
    const id = profile.samples[i]
    samples.set(id, (samples.get(id) ?? 0) + Math.max(0, profile.timeDeltas?.[i] ?? 0) / 1000)
  }
  return [...samples].map(([id, selfMs]) => {
    const frame = nodes.get(id) ?? {}
    let source = ''
    try { source = new URL(frame.url).pathname } catch { /* Internal/native frame. */ }
    return { functionName: frame.functionName ?? '', source, line: frame.lineNumber, selfMs }
  }).sort((a, b) => b.selfMs - a.selfMs).slice(0, 30)
}
const paint = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))

async function mainTab(page) {
  await page.locator('[role="tab"][data-tab-id="chat"]').first().click()
  await page.waitForFunction(() => !!document.querySelector('[role="tab"][data-tab-id="chat"][aria-selected="true"]'))
}
async function ready(page, since) {
  await page.waitForFunction(start => performance.getEntriesByName('acceptance-transcript-painted', 'mark').some(mark => performance.timeOrigin + mark.startTime >= start), since, { timeout: 30_000 })
  await paint(page)
}
async function typeKey(page) {
  const input = page.locator('.agent-composer textarea').first()
  await input.focus()
  await input.evaluate(element => { window.__acceptanceKeyAt = null; element.addEventListener('keydown', event => { window.__acceptanceKeyAt = event.timeStamp }, { once: true }) })
  await input.press('x')
  await paint(page)
  const ms = await page.evaluate(() => window.__acceptanceKeyAt === null ? null : performance.now() - window.__acceptanceKeyAt)
  if (!Number.isFinite(ms) || ms < 0) throw new Error('invalid_native_key_sample')
  await input.fill('')
  return ms
}

// Services remain owned by release-perf and are stopped by its existing defers.
// The optional external daemon is sampled read-only; no engine is launched.
export async function runAcceptance(page, context, config) {
  const result = { status: 'running', boot: { samplesMs: [], navigation: [] }, soak: { durationSeconds: 0, samples: [], processes: {}, requestFamilies: {}, nativeInputMs: [] } }
  const cdp = await context.newCDPSession(page)
  const requests = new Map(), identities = new Map()
  const requestFinished = request => {
    const pathname = new URL(request.url()).pathname
    if (!pathname.startsWith('/api/')) return
    const family = pathname.endsWith('/live') ? 'live' : pathname.endsWith('/topics') ? 'topics' : pathname.endsWith('/permissions') ? 'permissions' : pathname.includes('insights') ? 'insights' : pathname.includes('board') ? 'board' : 'other_api'
    const elapsed = request.timing().responseEnd
    if (elapsed >= 0) {
      const held = requests.get(family) ?? { count: 0, totalMs: 0, maxMs: 0 }
      held.count++; held.totalMs += elapsed; held.maxMs = Math.max(held.maxMs, elapsed)
      requests.set(family, held)
    }
  }
  try {
    if (!Number.isFinite(config.seconds) || config.seconds <= 0 || !config.pids.daemon) throw new Error('invalid_soak_configuration')
    for (const [name, pid] of Object.entries(config.pids)) identities.set(name, processSample(pid).startTicks)
    await cdp.send('Performance.enable')
    result.boot = await runBootTrials(page, context, config)
    await page.evaluate(() => {
      window.__acceptanceLongTasks = []
      window.__acceptanceLongTaskCount = 0
      window.__acceptanceObserver = new PerformanceObserver(list => {
        for (const entry of list.getEntries()) {
          window.__acceptanceLongTaskCount++
          if (window.__acceptanceLongTasks.length < 2000) window.__acceptanceLongTasks.push({ startMs: entry.startTime, durationMs: entry.duration })
        }
      })
      window.__acceptanceObserver.observe({ type: 'longtask', buffered: false })
    })
    page.on('requestfinished', requestFinished)
    await cdp.send('Profiler.enable')
    await cdp.send('Profiler.start')
    const start = Date.now()
    let next = start, iteration = 0
    for (;;) {
      const atSeconds = (Date.now() - start) / 1000
      // GC-normalized browser heap samples; process RSS is not a Go heap profile.
      await cdp.send('HeapProfiler.collectGarbage')
      const { metrics } = await cdp.send('Performance.getMetrics')
      const values = Object.fromEntries(metrics.map(metric => [metric.name, metric.value]))
      const dom = await cdp.send('Memory.getDOMCounters')
      const processes = {}
      for (const [name, pid] of Object.entries(config.pids)) processes[name] = processSample(pid, '/proc', identities.get(name))
      result.soak.samples.push({ atSeconds, jsHeapBytes: values.JSHeapUsedSize, nodes: dom.nodes, documents: dom.documents, processes })
      result.soak.durationSeconds = (Date.now() - start) / 1000
      // Persist each sample so an interrupted run retains an honest partial result.
      await writeFile(config.output, JSON.stringify(result, null, 2), { mode: 0o600 })
      if (Date.now() - start >= config.seconds * 1000) break
      // Repeated real in-app navigation retains the JS process/caches; no periodic
      // reload resets away a leak. The initial boot trials above are separate.
      const history = page.getByRole('button', { name: /History/ })
      if (await page.locator('[title="Release-perf workspace 1"]').count() === 0) await history.first().click()
      await page.locator('[title="Release-perf workspace 1"]').click()
      await page.locator('[title="Release-perf workspace 0"]').click()
      await mainTab(page)
      await page.locator('.agent-composer textarea').first().waitFor({ state: 'visible' })
      for (let key = 0; key < 5; key++) result.soak.nativeInputMs.push(await typeKey(page))
      if (iteration++ === 0) {
        const { profile } = await cdp.send('Profiler.stop')
        result.soak.firstCycleCpu = cpuSummary(profile)
      }
      next += config.intervalSeconds * 1000
      // Real measurement pacing, never a test's completion signal.
      await new Promise(resolve => setTimeout(resolve, Math.max(0, Math.min(next, start + config.seconds * 1000) - Date.now())))
    }
    result.soak.browserHeap = growth(result.soak.samples, 'jsHeapBytes')
    for (const name of Object.keys(config.pids)) result.soak.processes[name] = growth(result.soak.samples.map(sample => sample.processes[name]), 'rssBytes')
    result.soak.requestFamilies = Object.fromEntries([...requests].map(([name, sample]) => [name, { ...sample, meanMs: sample.totalMs / sample.count }]))
    const tasks = await page.evaluate(() => { window.__acceptanceObserver.disconnect(); return { entries: window.__acceptanceLongTasks, count: window.__acceptanceLongTaskCount } })
    result.soak.longTasks = tasks.entries
    result.soak.longTaskCount = tasks.count
    result.soak.longTasksTruncated = tasks.count > tasks.entries.length
    result.soak.daemonWorkload = 'read-only RSS sampling of the explicitly supplied existing daemon; no synthetic engine launches'
    result.status = 'complete'
  } catch (error) {
    result.status = 'incomplete'
    // Keep raw transport/OS messages in neither JSON nor terminal output.
    result.cause = /^[a-z_]+$/.test(error.message) ? error.message : error.name
    throw new Error('acceptance_measurement_failed')
  } finally {
    page.off('requestfinished', requestFinished)
    try { await cdp.detach() } finally {
      await writeFile(config.output, JSON.stringify(result, null, 2), { mode: 0o600 })
    }
  }
  return result
}

export async function closeBlackboardTab(page) {
  const close = page.getByRole('button', { name: 'Close Blackboard', exact: true })
  if (await close.count()) {
    await close.click()
    await close.waitFor({ state: 'detached' })
  }
}

// Inject identical DOM readiness into both releases: v0.5.4 predates the app's
// release-performance marks. This observes real transcript rows, then two frames.
export async function runBootTrials(page, context, config) {
  await page.addInitScript(() => {
    let scheduled = false
    const ready = () => {
      if (scheduled || !document.querySelector('.session-transcript-pane [data-event-id], .session-transcript-pane [data-testid="tool-group"]')) return
      scheduled = true
      observer.disconnect()
      requestAnimationFrame(() => requestAnimationFrame(() => performance.mark('acceptance-transcript-painted')))
    }
    const observer = new MutationObserver(ready)
    observer.observe(document, { childList: true, subtree: true })
    ready()
  })
  const result = { samplesMs: [], navigation: [], readiness: 'version-neutral transcript event row or folded tool group plus two animation frames' }
  const cdp = await context.newCDPSession(page)
  try {
    await cdp.send('Network.enable')
    await cdp.send('Network.setCacheDisabled', { cacheDisabled: true })
    await page.goto(`${config.runnerURL}/sessions/${config.longSession}`)
    await mainTab(page)
    for (let i = 0; i < 3; i++) {
      const since = Date.now()
      await page.goto(`${config.runnerURL}/sessions/${config.longSession}`)
      await ready(page, since)
      result.samplesMs.push(Date.now() - since)
      result.navigation.push(await page.evaluate(() => {
        const timing = performance.getEntriesByType('navigation')[0]
        return timing ? { responseEndMs: timing.responseEnd, domInteractiveMs: timing.domInteractive, loadEndMs: timing.loadEventEnd, transferBytes: timing.transferSize } : null
      }))
    }
    return result
  } finally {
    try { await cdp.send('Network.setCacheDisabled', { cacheDisabled: false }) } finally { await cdp.detach() }
  }
}
