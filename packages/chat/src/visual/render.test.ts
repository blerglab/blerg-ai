import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderVisual } from './render'

// BLG-468: the 'mermaid' kind now tries our own flowchart renderer first (render.ts); a diagram
// outside its declared subset must still fall through to the existing Mermaid path unchanged.
// Mocked the same way mermaid.test.ts already does, so this exercises the real fallback wiring
// rather than reimplementing the sandboxed-frame protocol.
const mermaidApi = vi.hoisted(() => ({ render: vi.fn() }))
vi.mock('./mermaidFrame', () => ({
  renderInFrame: async (source: string) => { const { svg } = await mermaidApi.render(source); return { svg, measured: null } },
}))
const originalDecode = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'decode')
const decode = vi.fn<() => Promise<void>>()
Object.defineProperty(HTMLImageElement.prototype, 'decode', { configurable: true, value: decode })
afterAll(() => {
  if (originalDecode) Object.defineProperty(HTMLImageElement.prototype, 'decode', originalDecode)
  else delete (HTMLImageElement.prototype as Partial<HTMLImageElement>).decode
})
beforeEach(() => {
  mermaidApi.render.mockReset().mockResolvedValue({ svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>Safe</text></svg>' })
  decode.mockReset().mockResolvedValue(undefined)
})

describe('renderVisual dispatcher', () => {
  it('draws a declared-subset flowchart with our own renderer, never touching the Mermaid frame', async () => {
    const container = document.createElement('div')
    const result = await renderVisual('mermaid', 'flowchart LR\nA[Start] --> B[Finish]', container)
    expect(container.querySelector('[data-diagram-renderer="blerg-flow"]')).not.toBeNull()
    expect(mermaidApi.render).not.toHaveBeenCalled()
    result.dispose()
  })

  it('falls back to Mermaid for a diagram outside the declared subset', async () => {
    const container = document.createElement('div')
    // BLG-468 §1: a plain sequence diagram is now its own declared subset (slice 2) — an ER
    // diagram stays outside both of this dispatcher's own parsers, so it still proves the real
    // fallback path, not a kind either of them happens to draw.
    const result = await renderVisual('mermaid', 'erDiagram\nOWNER ||--o{ SESSION : owns', container)
    expect(container.querySelector('[data-diagram-renderer^="blerg-"]')).toBeNull()
    expect(mermaidApi.render).toHaveBeenCalledTimes(1)
    result.dispose()
  })

  it('passes a card chip activation through to onChipOpen', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const opened: string[] = []
    const result = await renderVisual('mermaid', 'flowchart LR\nA[BLG-468] --> B[Plain]', container, undefined, undefined, undefined, key => opened.push(key))
    container.querySelector('[data-blerg-chip-key]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(opened).toEqual(['card:BLG:468'])
    result.dispose()
    container.remove()
  })
  it('draws a shipped mark with our renderer', async () => {
    const container = document.createElement('div')
    const source = JSON.stringify({data: {values: [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}]}, mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}})
    const result = await renderVisual('vega-lite', source, container)
    expect(result.svg).toContain('<svg')
    expect(container.querySelector('[data-chart-renderer="blerg"]')).not.toBeNull()
    result.dispose()
  })

  it('clears any partial blerg draw before attempting the Vega fallback, when classify says blerg but drawing throws (design doc case 4, M8)', async () => {
    const container = document.createElement('div')
    // bar + size is classified 'blerg' (both are on classify.ts's allow-list) but marks/bar.ts
    // doesn't draw a size encoding and throws NotShippedYet — the dispatcher must catch that and
    // clear whatever renderBlergChart partially attached before it ever reaches the Vega
    // fallback (which separately needs a real Worker, unavailable in this jsdom suite, so this
    // test only asserts what M8 is actually about: blerg's own partial DOM never survives into
    // that attempt). bar + color now draws with our own renderer (BLG-452 priority 2).
    const source = JSON.stringify({
      data: {values: [{month: 'Jan', open: 3, n: 1}, {month: 'Feb', open: 4, n: 2}]},
      mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}, size: {field: 'n', type: 'quantitative'}},
    })
    await renderVisual('vega-lite', source, container).catch(() => {})
    expect(container.querySelector('[data-chart-renderer="blerg"]')).toBeNull()
  })

  // BLG-467: our renderer lays out again at the container's own width — shrinking an explicit
  // spec width to fit a narrower pane, never growing it past what the author asked for, and
  // scaling height to match so a resize never distorts the chart.
  it('shrinks a chart with an explicit width to a narrower container, keeping its aspect ratio', async () => {
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', {value: 300, configurable: true})
    const source = JSON.stringify({
      width: 600, height: 300,
      data: {values: [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}]},
      mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}},
    })
    const result = await renderVisual('vega-lite', source, container)
    expect(result.width).toBe(300)
    expect(result.height).toBe(150)
    result.dispose()
  })

  it('keeps an explicit chart width when the container is wider, instead of growing it', async () => {
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', {value: 1200, configurable: true})
    const source = JSON.stringify({
      width: 400, height: 200,
      data: {values: [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}]},
      mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}},
    })
    const result = await renderVisual('vega-lite', source, container)
    expect(result.width).toBe(400)
    expect(result.height).toBe(200)
    result.dispose()
  })

  // Review finding: the blerg path never checked `signal` at all — an abort mid-draw ran
  // renderBlergChart to completion anyway, instead of throwing the AbortError callers (e.g.
  // VisualBlock.tsx's own draw()) specifically check for to distinguish a cancel from a real
  // failure.
  it('throws AbortError for the blerg path when already aborted, drawing nothing', async () => {
    const container = document.createElement('div')
    const source = JSON.stringify({data: {values: [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}]}, mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}})
    const controller = new AbortController()
    controller.abort()
    await expect(renderVisual('vega-lite', source, container, undefined, controller.signal)).rejects.toMatchObject({name: 'AbortError'})
    expect(container.querySelector('[data-chart-renderer="blerg"]')).toBeNull()
  })
})

it('fills a dashboard rectangle without applying authored aspect or exceeding the drawing side budget', async () => {
  const container = document.createElement('div')
  const source = JSON.stringify({mark: 'bar', data: {values: [{day: 'Mon', n: 2}]}, encoding: {x: {field: 'day', type: 'nominal'}, y: {field: 'n', type: 'quantitative'}}})
  for (const availableWidth of [300, 3840, 8000]) {
    const drawing = await renderVisual('vega-lite', source, container, undefined, undefined, {available: {width: availableWidth, height: 260}, fill: true, stretch: true})
    expect(drawing.width).toBe(Math.min(4096, availableWidth))
    expect(drawing.height).toBe(260)
    drawing.dispose()
  }
})

it('settles a stretched native resize at the same geometry as a fresh final-size surface', async () => {
  const source = JSON.stringify({mark: 'bar', data: {values: [{day: 'Mon', n: 2}, {day: 'Tue', n: 4}]}, encoding: {x: {field: 'day', type: 'nominal'}, y: {field: 'n', type: 'quantitative'}}})
  const first = document.createElement('div'), fresh = document.createElement('div')
  const chart = await renderVisual('vega-lite', source, first, undefined, undefined, {available: {width: 300, height: 260}, fill: true, stretch: true})
  chart.resize!({width: 1800, height: 260}, 1)
  const reference = await renderVisual('vega-lite', source, fresh, undefined, undefined, {available: {width: 1800, height: 260}, fill: true, stretch: true})
  expect([...first.querySelectorAll('text')].map(el => [el.textContent, el.getAttribute('x'), el.getAttribute('y'), el.getAttribute('font-size')]))
    .toEqual([...fresh.querySelectorAll('text')].map(el => [el.textContent, el.getAttribute('x'), el.getAttribute('y'), el.getAttribute('font-size')]))
  chart.dispose(); reference.dispose()
})
