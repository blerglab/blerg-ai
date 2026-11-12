import {expect, it, vi} from 'vitest'
import type {BrowserCommandContext} from 'vitest/node'
import {startChartCapture} from './chartFrames'

it('refreshes the compositor before opening a stream that may replay its cached image', async () => {
  // CDP can start with the previous capture's image, whose witness has no sample
  // in this capture. A screenshot is the explicit current-composite barrier.
  let cached = 'preceding witness'
  const send = vi.fn(async (method: string) => {
    if (method === 'Page.captureScreenshot') cached = 'current witness'
    if (method === 'Page.startScreencast') expect(cached).toBe('current witness')
    return {data: ''}
  })
  const page = {setViewportSize: vi.fn(async () => {})}
  const context = {newCDPSession: vi.fn(async () => ({send, on: vi.fn()}))}
  await startChartCapture({page, context} as unknown as BrowserCommandContext, 'cached-composite')
  expect(send).toHaveBeenCalledWith('Page.startScreencast', expect.any(Object))
})
