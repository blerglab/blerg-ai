import { describe, expect, it, vi } from 'vitest'
import { renderBlergChart } from './render'
import { drawBar } from './marks/bar'
import { readVisualTheme } from '../theme'

const values = [
  {month: 'Jan', team: 'A', open: 3, closed: 5}, {month: 'Feb', team: 'A', open: 4, closed: 2},
  {month: 'Jan', team: 'B', open: 1, closed: 6}, {month: 'Feb', team: 'B', open: 7, closed: 3},
]
const bar = {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}

describe('BLG-452 step 4: composition', () => {
  it('hconcat draws each child side by side, left to right', () => {
    const spec = {data: {values}, hconcat: [{...bar, width: 200, height: 150}, {...bar, width: 300, height: 150}]}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const result = renderBlergChart(spec, theme, container, 500, 150)
    const groups = container.querySelectorAll('[data-scene-key="plot-body"] > g > g')
    expect(groups.length).toBeGreaterThanOrEqual(2)
    expect(groups[0].getAttribute('transform')).toBe('translate(0,0)')
    expect(groups[1].getAttribute('transform')).toBe('translate(200,0)') // after the first child's own declared width
    expect(result.svg).toContain('<rect') // both panels drew real bars
  })

  // Review finding: a concat child with no data of its own used to draw empty instead of
  // inheriting the parent's — Vega-Lite's own rule (vegaPolicy.ts's checkSpec: "its own inline
  // data or named dataset, else its parent's") applies here too, not just to layer.
  it('hconcat: a child with no data of its own inherits the parent\'s', () => {
    const spec = {data: {values}, hconcat: [bar, {...bar, encoding: {...bar.encoding, y: {field: 'closed', type: 'quantitative'}}}]}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const result = renderBlergChart(spec, theme, container, 500, 150)
    const rects = container.querySelectorAll('[data-chart-renderer] rect')
    expect(rects.length).toBeGreaterThan(1) // not just the background rect from an empty second panel
    expect(result.svg).toContain('<rect')
  })

  it('hconcat: a child with its own data keeps it, not the parent\'s', () => {
    const own = [{month: 'Mar', team: 'A', open: 9, closed: 1}]
    const spec = {data: {values}, hconcat: [bar, {...bar, data: {values: own}}]}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(spec, theme, container, 500, 150)
    const groups = container.querySelectorAll('[data-scene-key="plot-body"] > g > g')
    // Only the second panel's own single-row data draws one bar; the parent's four rows (two
    // months) would draw two.
    expect(groups[1].querySelectorAll('rect').length).toBe(1)
  })

  it('vconcat draws each child top to bottom', () => {
    const spec = {data: {values}, vconcat: [bar, bar]}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(spec, theme, container, 400, 300)
    const groups = container.querySelectorAll('[data-scene-key="plot-body"] > g > g')
    expect(groups[0].getAttribute('transform')).toBe('translate(0,0)')
    expect(groups[1].getAttribute('transform')).toBe('translate(0,150)') // 300 / 2 children
  })

  it('facet draws one panel per distinct category, filtered to that category\'s rows', () => {
    const spec = {data: {values}, facet: {field: 'team', type: 'nominal'}, columns: 2, spec: bar}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const result = renderBlergChart(spec, theme, container, 400, 300)
    const groups = container.querySelectorAll('[data-scene-key="plot-body"] > g > g')
    expect(groups.length).toBe(2) // team A and team B
    expect(result.svg).toContain('<rect')
  })

  it('repeat substitutes the {"repeat": "repeat"} field placeholder per panel', () => {
    const spec = {
      data: {values}, repeat: ['open', 'closed'],
      spec: {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: {repeat: 'repeat'}, type: 'quantitative'}}},
    }
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const result = renderBlergChart(spec, theme, container, 400, 200)
    const groups = container.querySelectorAll('[data-scene-key="plot-body"] > g > g')
    expect(groups.length).toBe(2) // one panel for "open", one for "closed"
    expect(result.svg).toContain('<rect')
  })

  it('layer still throws NotShippedYet and falls back (BLG-452 step 4 follow-up)', () => {
    const spec = {data: {values}, layer: [bar]}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    expect(() => renderBlergChart(spec, theme, container, 400, 300)).toThrow(/layer/)
  })

  // Review finding: no test covered hover on a composed chart at all — tooltip regions are
  // translated per panel (translateRegions in render.ts), and nothing proved a point in the
  // *second* panel reports its own (not the first panel's) tooltip text.
  it('hconcat: hovering the second panel shows that panel\'s own tooltip, not the first panel\'s', () => {
    const secondEncoding = {...bar.encoding, y: {field: 'closed', type: 'quantitative'}, tooltip: [{field: 'month', type: 'nominal'}, {field: 'closed', type: 'quantitative'}]}
    const spec = {
      data: {values}, hconcat: [
        {...bar, width: 200, height: 150, encoding: {...bar.encoding, tooltip: [{field: 'month', type: 'nominal'}, {field: 'open', type: 'quantitative'}]}},
        {...bar, width: 200, height: 150, encoding: secondEncoding},
      ],
    }
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(spec, theme, container, 400, 150)
    const tooltip = container.querySelector<HTMLElement>('.visual-tooltip')!
    expect(tooltip).not.toBeNull()
    // jsdom has no real layout (render.test.ts's own tooltip test notes the same limitation), so
    // the point to hover comes from the second panel's own computed region, offset by the known
    // +200 translation (its own declared width, same as "hconcat draws each child side by side"
    // above) — exactly what translateRegions applies in render.ts, not re-derived from the DOM.
    const region = drawBar(values, secondEncoding, {}, 200, 150, theme).tooltipRegions[0]!
    const svg = container.querySelector('[data-chart-renderer="blerg"]')!
    vi.spyOn(svg, 'getBoundingClientRect').mockReturnValue({left:0,top:0,width:400,height:150} as DOMRect)
    svg.dispatchEvent(new PointerEvent('pointermove', {clientX: 200 + (region.x1 + region.x2) / 2, clientY: (region.y1 + region.y2) / 2, bubbles: true}))
    expect(tooltip.hidden).toBe(false)
    expect(tooltip.textContent).toContain('closed') // the second panel's own field, not "open"
  })
})
