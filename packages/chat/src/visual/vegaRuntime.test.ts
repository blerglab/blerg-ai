import { View } from 'vega'
import { expect, it, vi } from 'vitest'
import { drawChart } from './vegaRuntime'
import { CHART_LIMITS } from './vegaBudget'
import { readVisualTheme } from './theme'
const theme = readVisualTheme(document.documentElement)
const spec = {data: {values: [{x: 'A', y: 1}, {x: 'B', y: 2}, {x: 'C', y: 3}]}, mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}, tooltip: {field: 'x'}}}

it('compiles and lays out a real chart with inert SVG and bounded tooltip regions', async () => {
  const drawing = await drawChart(spec, theme)
  expect(drawing.svg).toContain('<svg')
  expect(drawing.width).toBeGreaterThan(100)
  expect(drawing.regions.map(region => region.text)).toEqual(['A', 'B', 'C'])
  expect(drawing.regions.every(region => Number.isFinite(region.x1) && region.x2 > region.x1)).toBe(true)
})
it('enforces runtime row storage rather than relying on the policy estimate', async () => {
  await expect(drawChart(spec, theme, {...CHART_LIMITS, rows: 2})).rejects.toThrow('This chart is too heavy to draw')
})
it('caps real scene items', async () => {
  await expect(drawChart(spec, theme, {...CHART_LIMITS, sceneItems: 2})).rejects.toThrow('This chart is too heavy to draw')
})
it('counts realized facet cells at runtime', async () => {
  const facet = {data: spec.data, facet: {column: {field: 'x', type: 'nominal'}}, spec: {mark: spec.mark, encoding: spec.encoding}}
  await expect(drawChart(facet, theme, {...CHART_LIMITS, panels: 2})).rejects.toThrow('This chart is too heavy to draw')
  await expect(drawChart(facet, theme)).resolves.toHaveProperty('svg')
})
it('caps serialized SVG and restores dataflow guards after refusal', async () => {
  await expect(drawChart(spec, theme, {...CHART_LIMITS, svgBytes: 100})).rejects.toThrow('This chart is too heavy to draw')
  await expect(drawChart(spec, theme)).resolves.toHaveProperty('svg')
})
it('caps cumulative row slots and operator expansion independently', async () => {
  await expect(drawChart(spec, theme, {...CHART_LIMITS, rowSlots: 5})).rejects.toThrow('This chart is too heavy to draw')
  await expect(drawChart(spec, theme, {...CHART_LIMITS, operators: 1})).rejects.toThrow('This chart is too heavy to draw')
})
it('measures serialized output in UTF-8 bytes', async () => {
  const text = {data: {values: [{label: 'é'.repeat(100)}]}, mark: 'text', encoding: {text: {field: 'label'}}}
  const drawing = await drawChart(text, theme)
  expect(new TextEncoder().encode(drawing.svg).byteLength).toBeGreaterThan(drawing.svg.length)
  await expect(drawChart(text, theme, {...CHART_LIMITS, svgBytes: drawing.svg.length})).rejects.toThrow('This chart is too heavy to draw')
})

it('refuses repeated dash expansion before SVG serialization begins', async () => {
  // Keep the failing case small: its dash content alone exceeds this output budget.
  const chart = {data: {values: Array.from({length: 30}, () => ({x: 1}))},
    mark: {type: 'point', strokeDash: Array(100).fill(1)}}
  const serialize = vi.spyOn(View.prototype, 'toSVG')
  try {
    await expect(drawChart(chart, theme, {...CHART_LIMITS, svgBytes: 5000})).rejects.toThrow('This chart is too heavy to draw')
    expect(serialize).not.toHaveBeenCalled()
  } finally { serialize.mockRestore() }
})

it('rejects the policy-accepted 9000 by 9000 dash payload before serialization', async () => {
  const chart = {data: {values: Array.from({length: 9000}, () => ({x: 1}))},
    mark: {type: 'point', strokeDash: Array(9000).fill(1)}}
  const serialize = vi.spyOn(View.prototype, 'toSVG')
  try {
    await expect(drawChart(chart, theme)).rejects.toThrow('This chart is too heavy to draw')
    expect(serialize).not.toHaveBeenCalled()
  } finally { serialize.mockRestore() }
})

it('draws at a fill size Blerg chose above the author limit, while the author limit still holds', async () => {
  const theme = readVisualTheme(document.body)
  const spec = {data: {values: [{a: 'x', b: 1}, {a: 'y', b: 2}]}, mark: 'bar', encoding: {x: {field: 'a', type: 'nominal'}, y: {field: 'b', type: 'quantitative'}}}
  const drawing = await drawChart(spec, theme, CHART_LIMITS, {width: 2400, height: 900})
  expect(drawing.width).toBeGreaterThan(2000)
  await expect(drawChart({...spec, width: 2400}, theme)).rejects.toThrow(/1 to 1024/)
  // A size past the drawing budget's side is ignored, never applied.
  const capped = await drawChart(spec, theme, CHART_LIMITS, {width: 99999, height: 900})
  expect(capped.width).toBeLessThan(2000)
})

it('refuses missing worker text metrics without accessing a nonexistent document', async () => {
 const old=document
 vi.stubGlobal('document',undefined);vi.stubGlobal('OffscreenCanvas',undefined)
 try {await expect(drawChart({...spec,title:'Measured heading'},theme)).rejects.toMatchObject({name:'TextMetricsUnavailable',message:'Chart text metrics are unavailable in this browser.'})}
 finally {vi.unstubAllGlobals();expect(document).toBe(old)}
})
