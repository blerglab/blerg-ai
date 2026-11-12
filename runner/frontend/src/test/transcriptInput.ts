import type { CDPSession, Page } from 'playwright'
import type { BrowserCommandContext } from 'vitest/node'

const touches = new WeakMap<Page, CDPSession>()
const traces = new WeakMap<Page, { cdp: CDPSession; events: unknown[] }>()

// No locator/readiness round trip through the busy renderer before injecting the native key.
export async function workspaceKey({ page }: BrowserCommandContext) {
  await page.keyboard.press('a')
}

// Test-only Chrome trace: includes JS stacks and layout/paint, rather than attributing every
// long task to the outer test runner. Marks in the iframe identify individual keys and switches.
export async function workspaceTrace({ page, context }: BrowserCommandContext, action: 'start' | 'stop') {
  if (action === 'start') {
    const cdp = await context.newCDPSession(page), events: unknown[] = []
    cdp.on('Tracing.dataCollected', ({ value }) => events.push(...value))
    traces.set(page, { cdp, events })
    await cdp.send('Profiler.enable')
    await cdp.send('Profiler.start')
    await cdp.send('Tracing.start', { categories: 'devtools.timeline,blink.user_timing,disabled-by-default-devtools.timeline,disabled-by-default-devtools.timeline.stack' })
    return ''
  }
  const trace = traces.get(page)!
  const complete = new Promise<void>(resolve => trace.cdp.once('Tracing.tracingComplete', () => resolve()))
  await trace.cdp.send('Tracing.end')
  await complete
  const { profile } = await trace.cdp.send('Profiler.stop')
  await trace.cdp.detach()
  traces.delete(page)
  return JSON.stringify({ traceEvents: trace.events, profile })
}

// Chromium's renderer-main-thread task time excludes idle waits between frames. Keep the
// protocol calls outside the measured interaction; unlike a CPU trace this needs no profiler.
const taskMetrics = new WeakMap<Page, { cdp: CDPSession; start: Map<string, number> }>()
export async function workspaceTaskTime({ page, context }: BrowserCommandContext, action: 'start' | 'stop') {
  if (action === 'start') {
    const cdp = await context.newCDPSession(page)
    await cdp.send('Performance.enable')
    const { metrics } = await cdp.send('Performance.getMetrics')
    taskMetrics.set(page, { cdp, start: new Map(metrics.map(m => [m.name, m.value])) })
    return { taskMs: 0, scriptMs: 0, layoutMs: 0, styleMs: 0 }
  }
  const measurement = taskMetrics.get(page)
  if (!measurement) throw new Error('Main-thread measurement was not started')
  try {
    const { metrics } = await measurement.cdp.send('Performance.getMetrics')
    const end = new Map(metrics.map(m => [m.name, m.value]))
    const delta = (name: string) => {
      const before = measurement.start.get(name), after = end.get(name)
      if (before === undefined || after === undefined) throw new Error(`Missing browser metric: ${name}`)
      return (after - before) * 1000
    }
    return { taskMs: delta('TaskDuration'), scriptMs: delta('ScriptDuration'), layoutMs: delta('LayoutDuration'), styleMs: delta('RecalcStyleDuration') }
  } finally {
    taskMetrics.delete(page)
    await measurement.cdp.detach()
  }
}

// Playwright input goes through Chromium's scrolling/focus defaults, not dispatchEvent.
export async function transcriptInput({ page, iframe, context }: BrowserCommandContext, kind: string) {
  // Avoid Vitest scaling the iframe: native scrollbar hit testing needs actual pixels.
  await page.setViewportSize({ width: 4200, height: 1200 })
  const root = iframe.locator('.agent-timeline')
  const box = await root.boundingBox()
  if (!box) throw new Error('Missing transcript viewport')
  const x = box.x + Math.min(100, box.width / 2), y = box.y + box.height / 2
  if (kind.startsWith('outside:')) {
    await iframe.locator('#outside').focus()
    await page.keyboard.press(kind.slice(8))
  } else if (kind === 'find') {
    await page.keyboard.press('Control+f')
    await page.keyboard.type('Row 0:')
    await page.keyboard.press('Enter')
    await page.keyboard.press('Escape')
  } else if (kind === 'click') {
    await page.mouse.click(x, y)
  } else if (kind === 'wheel') {
    await page.mouse.move(x, y)
    await page.mouse.wheel(0, -250)
  } else if (kind === 'PageUp' || kind === 'Home' || kind === 'PageUpTop') {
    await page.mouse.click(x, y)
    if (kind === 'PageUpTop') {
      for (let i = 0; i < 80 && await root.evaluate(el => el.scrollTop) > 1; i++) {
        await page.keyboard.press('PageUp')
        await page.waitForTimeout(120)
      }
    } else await page.keyboard.press(kind)
  } else if (kind === 'Shift+Tab') {
    await iframe.locator('#outside').focus()
    await page.keyboard.press('Shift+Tab')
  } else if (kind === 'selection' || kind === 'selectionTop' || kind === 'hold') {
    await page.mouse.move(x, y)
    await page.mouse.down()
    if (kind === 'selection' || kind === 'selectionTop') {
      await page.mouse.move(x, box.y - 40, { steps: 20 })
      await page.waitForTimeout(400)
      if (kind === 'selectionTop') {
        for (let i = 0; i < 60 && await root.evaluate(el => el.scrollTop) > 1; i++) await page.waitForTimeout(100)
      }
      await page.mouse.up()
    }
  } else if (kind === 'release') {
    await page.mouse.up()
  } else if (kind === 'scrollbar') {
    await page.mouse.move(box.x + box.width - 6, box.y + box.height - 8)
    await page.mouse.down()
    await page.mouse.move(box.x + box.width - 6, box.y + 100, { steps: 20 })
    await page.mouse.up()
  } else if (kind === 'touchStart' || kind === 'touchMove' || kind === 'touchEnd') {
    let cdp = touches.get(page)
    if (!cdp) { cdp = await context.newCDPSession(page); touches.set(page, cdp) }
    await cdp.send('Input.dispatchTouchEvent', { type: kind, touchPoints: kind === 'touchEnd' ? [] : [{ x, y: kind === 'touchMove' ? y + 180 : y - 80 }] })
    if (kind === 'touchEnd') { await cdp.detach(); touches.delete(page) }
  } else throw new Error(`Unknown input: ${kind}`)
  await page.waitForTimeout(kind === 'click' ? 20 : 400)
}
