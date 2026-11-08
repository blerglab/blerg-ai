import { describe, expect, it, vi } from 'vitest'
import { renderBlergChart, NotShippedYet } from './render'
import { drawBar } from './marks/bar'
import { drawRule } from './marks/rule'
import { drawRectHeatmap } from './marks/rect'
import { readVisualTheme } from '../theme'

const values = [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}]
const bar = {data: {values}, mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}

describe('renderBlergChart', () => {
  it('draws a bar chart, mounting real DOM built from the returned SVG (design doc §0)', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const result = renderBlergChart(bar, theme, container, 400, 300)
    expect(result.width).toBe(400)
    expect(result.height).toBe(300)
    expect(result.svg).toContain('<svg')
    expect(container.querySelector('[data-chart-renderer="blerg"]')).not.toBeNull()
    expect(container.querySelectorAll('rect').length).toBeGreaterThan(1) // background + bars
    result.dispose()
    expect(container.children.length).toBe(0)
  })

  it('mounts a .visual-tooltip sibling and shows it on hover over a drawn bar', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const spec = {...bar, encoding: {...bar.encoding, tooltip: [{field: 'month', type: 'nominal'}, {field: 'open', type: 'quantitative'}]}}
    renderBlergChart(spec, theme, container, 400, 300)
    const tooltip = container.querySelector<HTMLElement>('.visual-tooltip')!
    expect(tooltip).not.toBeNull()
    expect(tooltip.hidden).toBe(true)
    // jsdom has no real layout, so getBoundingClientRect() on the mounted SVG/rects is
    // meaningless here; pick a point from drawBar's own computed region directly instead (the
    // same values renderBlergChart's internal call produced), with svg's own rect at (0,0) in
    // jsdom, matching attachTooltip's coordinate math (clientX/clientY - bounds.left/top).
    const region = drawBar(values, spec.encoding, {}, 400, 300, theme).tooltipRegions[0]!
    const svg = container.querySelector('[data-chart-renderer="blerg"]')!
    vi.spyOn(svg, 'getBoundingClientRect').mockReturnValue({left: 0, top: 0, width: 400, height: 300} as DOMRect)
    svg.dispatchEvent(new PointerEvent('pointermove', {clientX: (region.x1 + region.x2) / 2, clientY: (region.y1 + region.y2) / 2, bubbles: true}))
    expect(tooltip.hidden).toBe(false)
    // "Feb" sorts before "Jan" (Vega-Lite's default nominal domain sort is alphabetical,
    // confirmed empirically — design doc §3.3/color.ts), so it's the first bar drawn.
    expect(tooltip.textContent).toBe('month: Feb\nopen: 4')
  })

  it('throws NotShippedYet for a sort-by-field object (custom sort: ascending/descending/null/array are implemented, a sort-by-another-field object is not)', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const spec = {data: {values}, mark: 'bar', encoding: {x: {field: 'month', type: 'nominal', sort: {field: 'open', op: 'sum', order: 'descending'}}, y: {field: 'open', type: 'quantitative'}}}
    expect(() => renderBlergChart(spec, theme, container, 400, 300)).toThrow(NotShippedYet)
  })

  it('throws NotShippedYet for a mark this file does not draw yet (tick: a firm v1 exclusion)', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    expect(() => renderBlergChart({data: {values}, mark: 'tick', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}, theme, container, 400, 300)).toThrow(NotShippedYet)
  })

  it('throws NotShippedYet for bar + size, rather than drawing it wrong (color is now implemented, BLG-452 priority 2)', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const withSize = {...bar, encoding: {...bar.encoding, size: {field: 'open', type: 'quantitative'}}}
    expect(() => renderBlergChart(withSize, theme, container, 400, 300)).toThrow(NotShippedYet)
  })

  it('draws a bar+text layer (BLG-452 priority 3), labeling each bar with its own value', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const spec = {data: {values}, encoding: bar.encoding, layer: [{mark: 'bar'}, {mark: 'text', encoding: {text: {field: 'open', type: 'quantitative'}}}]}
    renderBlergChart(spec, theme, container, 400, 300)
    const labels = [...container.querySelectorAll('text[data-chart-label="true"]')].map(el => el.textContent)
    expect(labels.sort()).toEqual(['3', '4'])
  })

  it('draws a line+rule layer (BLG-452 priority 3), the rule using the theme reference stroke', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const lineValues = [{t: 1, v: 10}, {t: 2, v: 15}, {t: 3, v: 12}]
    const spec = {data: {values: lineValues}, layer: [
      {mark: 'line', encoding: {x: {field: 't', type: 'quantitative'}, y: {field: 'v', type: 'quantitative'}}},
      {mark: 'rule', encoding: {y: {datum: 12}}},
    ]}
    renderBlergChart(spec, theme, container, 400, 300)
    expect(container.querySelector('path')).not.toBeNull()
    expect(container.querySelector(`line[stroke="${theme.muted}"]`)).not.toBeNull()
  })

  // Found while reviewing BLG-452's screenshots: the pre-existing VisualOutput.browser.test.tsx
  // layered-rule test caught this regression once layer specs stopped blanket-falling-back — a
  // line's point overlay (mark.point) isn't drawn by this layer path, so it must fall back rather
  // than silently draw the line without its dots.
  it('throws NotShippedYet for a line+rule layer when the line mark has its own point overlay', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const lineValues = [{t: 1, v: 10}, {t: 2, v: 15}, {t: 3, v: 12}]
    const spec = {data: {values: lineValues}, layer: [
      {mark: {type: 'line', point: true}, encoding: {x: {field: 't', type: 'quantitative'}, y: {field: 'v', type: 'quantitative'}}},
      {mark: 'rule', encoding: {y: {datum: 12}}},
    ]}
    expect(() => renderBlergChart(spec, theme, container, 400, 300)).toThrow(NotShippedYet)
  })

  it('throws NotShippedYet for an unsupported layer combination (BLG-452 priority 3 scope)', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const spec = {data: {values}, encoding: bar.encoding, layer: [{mark: 'bar'}, {mark: 'point'}]}
    expect(() => renderBlergChart(spec, theme, container, 400, 300)).toThrow(NotShippedYet)
  })

  // BLG-452 priority 5: a temporal x used to coerce to a plain string category in bar/rule/rect/
  // boxplot (none of them checked the type, and none buckets by timeUnit), which would silently
  // mis-order and mis-bucket real calendar data — a latent correctness gap, closed this round by
  // falling back to Vega instead, the same way bin/size were closed in priority 2.
  it('throws NotShippedYet for a temporal x on bar, rather than drawing it with wrong (alphabetical-string) order', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const temporalValues = [{when: '2024-02-01', open: 3}, {when: '2024-01-01', open: 4}]
    const spec = {data: {values: temporalValues}, mark: 'bar', encoding: {x: {field: 'when', type: 'temporal', timeUnit: 'yearmonth'}, y: bar.encoding.y}}
    expect(() => renderBlergChart(spec, theme, container, 400, 300)).toThrow(NotShippedYet)
  })

  it('throws for a temporal x on boxplot (boxplot.ts has its own NotShippedYet class, same pattern as every other mark file)', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const temporalValues = [{when: '2024-01-01', open: 3}, {when: '2024-01-01', open: 4}]
    const spec = {data: {values: temporalValues}, mark: 'boxplot', encoding: {x: {field: 'when', type: 'temporal'}, y: bar.encoding.y}}
    expect(() => renderBlergChart(spec, theme, container, 400, 300)).toThrow()
  })

  it('throws NotShippedYet for a temporal x on rule', () => {
    const theme = readVisualTheme(document.createElement('div'))
    const temporalValues = [{when: '2024-01-01', lo: 1, hi: 5}]
    const encoding = {x: {field: 'when', type: 'temporal'}, y: {field: 'lo', type: 'quantitative'}, y2: {field: 'hi', type: 'quantitative'}}
    expect(() => drawRule(temporalValues, encoding, {}, 400, 300, theme)).toThrow()
  })

  it('throws NotShippedYet for a temporal axis on rect', () => {
    const theme = readVisualTheme(document.createElement('div'))
    const temporalValues = [{when: '2024-01-01', y: 'A', v: 1}]
    const encoding = {x: {field: 'when', type: 'temporal'}, y: {field: 'y', type: 'nominal'}, color: {field: 'v', type: 'quantitative'}}
    expect(() => drawRectHeatmap(temporalValues, encoding, 400, 300, theme)).toThrow()
  })

  // Review finding: no test proved our own renderer actually repaints on a theme change — only
  // Vega's canvas-repaint path (ThemeShell.browser.test.tsx) had coverage. VisualBlock.tsx's own
  // redraw mechanism is generic (its effect's dependency array includes `theme`, dispose + redraw
  // on any change, for every kind), so this proves what that mechanism actually depends on: that
  // renderBlergChart draws the *new* theme's own colors, not stale ones, when called again.
  it('repaints with the new theme\'s colors when redrawn (VisualBlock.tsx\'s own dispose-and-redraw mechanism)', () => {
    const darkHost = document.createElement('div')
    darkHost.style.setProperty('--chat-accent', '#1188dd')
    const darkTheme = readVisualTheme(darkHost)
    const lightHost = document.createElement('div')
    lightHost.style.setProperty('--chat-accent', '#dd7711')
    const lightTheme = readVisualTheme(lightHost)
    expect(darkTheme.accent).not.toBe(lightTheme.accent)

    const container = document.createElement('div')
    const first = renderBlergChart(bar, darkTheme, container, 400, 300)
    expect(first.svg).toContain(darkTheme.accent)
    first.dispose()
    expect(container.children.length).toBe(0) // the old draw's DOM is gone before the redraw, same as VisualBlock.tsx's draw()

    const second = renderBlergChart(bar, lightTheme, container, 400, 300)
    expect(second.svg).toContain(lightTheme.accent)
    expect(second.svg).not.toContain(darkTheme.accent)
    expect(container.querySelector('[data-chart-renderer="blerg"]')).not.toBeNull()
  })
})

// Slice B: geometry and export belong to the mounted surface.
it('resizes and repaints the mounted scene atomically, rejecting old generations', () => {
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const visual = renderBlergChart(bar, theme, container, 400, 300)
  const root = container.querySelector('svg')!
  const axis = root.querySelector('[data-scene-key="plot-body"] g g')!
  visual.resize!({width: 520, height: 320}, 2)
  expect(container.querySelector('svg')).toBe(root)
  expect(root.querySelector('[data-scene-key="plot-body"] g g')).toBe(axis)
  expect(root.getAttribute('width')).toBe('520')
  expect(visual.snapshot!().svg).toContain('width="520"')
  visual.resize!({width: 390, height: 200}, 1)
  expect(root.getAttribute('width')).toBe('520')
  visual.setTheme!({...theme, fontSize: theme.fontSize + 2})
  expect(container.querySelector('svg')).toBe(root)
  expect(visual.svg).toBe(visual.snapshot!().svg)
  visual.resize!({width: 1800, height: 900}, 3)
  expect(root.querySelector('[data-scene-key="plot-body"] g g')).toBe(axis)
  visual.dispose()
  expect(container.childElementCount).toBe(0)
})

it('prepares and resizes native scene nodes without string parsing', () => {
  const parse = vi.spyOn(DOMParser.prototype, 'parseFromString')
  const host = document.createElement('div')
  try {
    const chart = renderBlergChart(bar, readVisualTheme(host), host, 400, 300)
    chart.resize!({width: 520, height: 320}, 1)
    expect(parse).not.toHaveBeenCalled()
    chart.dispose()
  } finally { parse.mockRestore() }
})
