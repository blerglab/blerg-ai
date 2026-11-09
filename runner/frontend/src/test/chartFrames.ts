import {analyzeChartImage, type ChartFrameSample} from './chartFrameWitness'
import {mkdir, rm, writeFile} from 'node:fs/promises'
import {resolve} from 'node:path'
import type {BrowserCommandContext} from 'vitest/node'

export async function chartFrames(_context: BrowserCommandContext, name: string, samples: unknown[]) {
  if (!/^[a-z0-9-]+$/.test(name)) throw new Error('Invalid frame report name')
  const directory = resolve(process.cwd(), '../../.cache/blg513')
  await mkdir(directory, {recursive: true})
  await writeFile(resolve(directory, `${name}.json`), JSON.stringify({name, samples}, null, 2))
}

import type {CDPSession, Page} from 'playwright'
const captures = new WeakMap<Page, {cdp: CDPSession; name: string; frames: {data: string; timestamp: number; terminal?: boolean}[]; bytes: number; truncated: boolean; recording: boolean; accepting: boolean}>()
export async function startChartCapture({page, context}: BrowserCommandContext, name: string, recording = true) {
  if (!/^[a-z0-9-]+$/.test(name)) throw new Error('Invalid capture name')
  // Keep compositor coordinates stable while Vitest changes the iframe's authored width.
  await page.setViewportSize({width: 3840, height: 2160})
  const cdp = await context.newCDPSession(page)
  const capture = {cdp, name, frames: [] as {data: string; timestamp: number; terminal?: boolean}[], bytes: 0, truncated: false, recording, accepting: true}
  captures.set(page, capture)
  cdp.on('Page.screencastFrame', event => {
    if (!capture.accepting) { void cdp.send('Page.screencastFrameAck', {sessionId: event.sessionId}).catch(() => {}); return }
    if (capture.frames.length < 600 && capture.bytes + event.data.length < 64_000_000) {
      capture.frames.push({data: event.data, timestamp: event.metadata.timestamp ?? 0})
      capture.bytes += event.data.length
    } else capture.truncated = true
    void cdp.send('Page.screencastFrameAck', {sessionId: event.sessionId}).catch(() => {})
  })
  if (recording) await cdp.send('Page.startScreencast', {format: 'jpeg', quality: 80, maxWidth: 3840, maxHeight: 2160, everyNthFrame: 1})
}
export async function stopChartCapture({page}: BrowserCommandContext, samples: ChartFrameSample[] = []) {
  const capture = captures.get(page)!
  if (capture.recording) {
    // A full-viewport terminal composite is an explicit completion signal for the last
    // committed scene; stopping a stream alone may discard its in-flight final encoding.
    const terminal = await capture.cdp.send('Page.captureScreenshot', {format: 'jpeg', quality: 80, captureBeyondViewport: false, fromSurface: true})
    await capture.cdp.send('Page.stopScreencast')
    // A late stream event must not be appended after the terminal composite.
    capture.accepting = false
    if (capture.frames.length < 600 && capture.bytes + terminal.data.length < 64_000_000) capture.frames.push({data: terminal.data, timestamp: Date.now() / 1000, terminal: true})
    else capture.truncated = true
  }
  await capture.cdp.detach()
  captures.delete(page)
  if (!capture.recording) return {count: 0, truncated: false, terminal: false, analyzed: 0, blankFrames: [] as number[]}
  const blankFrames: number[] = []
  let analyzed = 0
  let analysisError: unknown
  const paintedSamples: number[] = []
  // Transfer measurements once; each JPEG carries the exact batch ID it painted.
  await page.evaluate(samples => { (globalThis as unknown as {__blergChartSamples?: ChartFrameSample[]}).__blergChartSamples = samples }, samples)
  for (const [index, frame] of capture.frames.entries()) {
    try {
      const result = await page.evaluate(analyzeChartImage, {jpeg: frame.data})
      paintedSamples[index] = result.frame
      if (result.analyzed) analyzed++
      if (result.blank) blankFrames.push(index)
    } catch (error) { analysisError = error; break }
  }
  await page.evaluate(() => { delete (globalThis as unknown as {__blergChartSamples?: ChartFrameSample[]}).__blergChartSamples })
  const directory = resolve(process.cwd(), '../../.cache/blg513', `${capture.name}-frames`)
  await rm(directory, {recursive: true, force: true})
  await mkdir(directory, {recursive: true})
  const metadata = []
  for (const [index, frame] of capture.frames.entries()) {
    const name = `${String(index).padStart(4, '0')}.jpg`
    await writeFile(resolve(directory, name), Buffer.from(frame.data, 'base64'))
    metadata.push({name, timestamp: frame.timestamp, kind: frame.terminal ? 'terminal' : 'stream', sample: paintedSamples[index]})
  }
  await writeFile(resolve(directory, 'frames.json'), JSON.stringify({truncated: capture.truncated, analyzed, blankFrames, frames: metadata}, null, 2))
  if (analysisError) throw analysisError
  return {count: metadata.length, truncated: capture.truncated, terminal: !!capture.frames.at(-1)?.terminal, analyzed, blankFrames}
}

// Locator screenshots can scroll/resize Vitest's outer page. Capture the app's known pane
// directly from the compositor so recording and PNG capture share stable coordinates.
export async function chartScreenshot({page}: BrowserCommandContext, name: string, width: number, height: number) {
  if (!/^[a-z0-9-]+$/.test(name) || !Number.isInteger(width) || !Number.isInteger(height) || width < 1 || width > 3840 || height < 1 || height > 2160) throw new Error('Invalid chart screenshot')
  const capture = captures.get(page)
  if (!capture) throw new Error('Chart capture is not active')
  if (capture.recording) throw new Error('PNG screenshots require the separate stills pass')
  const result = await capture.cdp.send('Page.captureScreenshot', {format: 'png', captureBeyondViewport: false, fromSurface: true, clip: {x: 0, y: 0, width, height, scale: 1}})
  const directory = resolve(process.cwd(), '../../.cache/blg513')
  await mkdir(directory, {recursive: true})
  await writeFile(resolve(directory, `${name}.png`), Buffer.from(result.data, 'base64'))
  return result.data
}
