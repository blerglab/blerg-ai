import { afterEach, expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import { render, waitFor } from '@testing-library/react'
import { VisualBlock } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import { renderVegaLite } from '../../../../packages/chat/src/visual/vega'
import { CHART_DEADLINE_MS } from '../../../../packages/chat/src/visual/vegaBudget'

afterEach(async () => { vi.unstubAllGlobals(); await page.viewport(1280, 900) })
it('keeps the page responsive and terminates synchronously stuck chart work', async () => {
  const NativeWorker = globalThis.Worker
  const source = URL.createObjectURL(new Blob(['while (true) {}'], {type: 'text/javascript'}))
  const terminate = vi.fn()
  vi.stubGlobal('Worker', class extends NativeWorker {
    constructor() { super(source) }
    terminate() { terminate(); super.terminate() }
  })
  let ticks = 0
  const timer = setInterval(() => { ticks++ }, 25)
  try {
    const container = document.createElement('div')
    const started = performance.now()
    await expect(renderVegaLite({data: {values: [{x: 1}]}, mark: 'point'}, container)).rejects.toThrow('This chart is too heavy to draw')
    expect(performance.now() - started).toBeGreaterThanOrEqual(CHART_DEADLINE_MS - 100)
    expect(ticks).toBeGreaterThan(5)
    expect(terminate).toHaveBeenCalledOnce()
    expect(container.querySelector('canvas')).toBeNull()
  } finally {
    clearInterval(timer)
    URL.revokeObjectURL(source)
  }
}, 15_000)

for (const width of [390, 3840]) {
  it(`fits a worker-rendered chart at ${width}px within the pixel budget`, async () => {
    await page.viewport(width, 900)
    const source = JSON.stringify({data: {values: [{x:'Alpha',y:1}]}, mark:'bar', encoding:{x:{field:'x',type:'nominal'}, y:{field:'y',type:'quantitative'}, tooltip:{field:'x'}}})
    const {container} = render(<div style={{width:'calc(100vw - 40px)', maxWidth:1200}}><VisualBlock kind="vega-lite" source={source} /></div>)
    const canvas = await waitFor(() => { const canvas = container.querySelector('canvas'); expect(canvas).not.toBeNull(); return canvas! }, {timeout:15000})
    expect(canvas.getBoundingClientRect().width).toBeLessThanOrEqual(width - 40)
    expect(container.querySelector('.visual-surface')!.scrollWidth).toBeLessThanOrEqual(container.querySelector('.visual-surface')!.clientWidth + 1)
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(4096 * 2048)
    await page.viewport(1280, 900)
  }, 20000)
}

it('refuses repeated dash expansion in a real worker and renders the next chart', async () => {
  const container = document.createElement('div')
  const chart = {data: {values: Array.from({length: 9000}, () => ({x: 1}))},
    mark: {type: 'point', strokeDash: Array(9000).fill(1)}}
  await expect(renderVegaLite(chart, container)).rejects.toThrow('This chart is too heavy to draw')
  expect(container.querySelector('canvas')).toBeNull()
  await renderVegaLite({data: {values: [{x: 1}]}, mark: 'point'}, container)
  expect(container.querySelector('canvas')).not.toBeNull()
}, 20_000)

it('keeps measured layered chrome inside a short filled card', async () => {
  const host = document.createElement('div')
  const chart = {title: 'A measured layered chart', data: {values: [{x: 0, y: 2}, {x: 1, y: 4}]}, layer: [
    {mark: 'line', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}},
    {mark: 'rule', encoding: {y: {datum: 3}}},
  ]}
  const drawing = await renderVegaLite(chart, host, undefined, undefined, {available: {width: 390, height: 120}, fill: true, stretch: true})
  expect(drawing.width).toBeLessThanOrEqual(390)
  expect(drawing.height).toBeLessThanOrEqual(120)
  drawing.dispose()
})
