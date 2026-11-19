import { afterEach, expect, it, vi } from 'vitest'
vi.mock('./vegaWorker?worker&inline', () => ({default: StuckWorker}))
import { renderVegaLite } from './vega'
import { readVisualTheme } from './theme'

const { StuckWorker } = vi.hoisted(() => ({StuckWorker: class StuckWorker {
  static instances: StuckWorker[] = []
  terminate = vi.fn()
  postMessage = vi.fn()
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: ErrorEvent) => void) | null = null
  constructor() { StuckWorker.instances.push(this) }
}}))
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); StuckWorker.instances = [] })

it('terminates a stuck compiler without blocking the page', async () => {
  vi.useFakeTimers()
  const container = document.createElement('div')
  const work = renderVegaLite({data: {values: [{x: 1}]}, mark: 'point'}, container, readVisualTheme(container))
  const refused = expect(work).rejects.toThrow('This chart is too heavy to draw')
  await vi.advanceTimersByTimeAsync(10_000)
  await refused
  expect(StuckWorker.instances).toHaveLength(1)
  expect(StuckWorker.instances[0].terminate).toHaveBeenCalledOnce()
  expect(container.querySelector('canvas')).toBeNull()
})

it('checks static policy before creating a worker', async () => {
  const container = document.createElement('div')
  await expect(renderVegaLite({data: {url: 'https://example.com/data'}, mark: 'bar'}, container)).rejects.toThrow()
  expect(StuckWorker.instances).toHaveLength(0)
})
it('terminates cancelled work and lets the next queued chart start', async () => {
  const container = document.createElement('div')
  const controller = new AbortController()
  const spec = {data: {values: [{x: 1}]}, mark: 'point'}
  const first = renderVegaLite(spec, container, readVisualTheme(container), controller.signal)
  const refused = expect(first).rejects.toThrow('Aborted')
  await vi.waitFor(() => expect(StuckWorker.instances).toHaveLength(1))
  controller.abort()
  await refused
  expect(StuckWorker.instances[0].terminate).toHaveBeenCalledOnce()
  const second = renderVegaLite(spec, container)
  const failure = expect(second).rejects.toThrow('This chart is too heavy to draw')
  await vi.waitFor(() => expect(StuckWorker.instances).toHaveLength(2))
  StuckWorker.instances[1].onerror?.(new ErrorEvent('error'))
  await failure
  expect(StuckWorker.instances[1].terminate).toHaveBeenCalledOnce()
})
it('never starts already cancelled queued work', async () => {
  const controller = new AbortController()
  controller.abort()
  const container = document.createElement('div')
  await expect(renderVegaLite({mark: 'point'}, container, readVisualTheme(container), controller.signal)).rejects.toThrow('Aborted')
  expect(StuckWorker.instances).toHaveLength(0)
})
