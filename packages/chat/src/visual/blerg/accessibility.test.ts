import { describe, expect, it, vi } from 'vitest'
import { renderBlergChart } from './render'
import { readVisualTheme } from '../theme'
import {buildAccessibleExtras} from './accessibility'

const values = [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}, {month: 'Mar', open: 2}]
const bar = {data: {values}, mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}

describe('BLG-452 step 4: accessibility (design doc §3.5)', () => {
  it('the chart root is role=group, not role=img (L11)', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(bar, theme, container, 400, 300)
    const root = container.querySelector('[data-chart-renderer="blerg"]')!
    expect(root.getAttribute('role')).toBe('group')
    expect(root.getAttribute('role')).not.toBe('img')
    expect(root.getAttribute('aria-label')).toBe('bar chart: 3 observations')
  })

  it('exposes one roving-focus series target (not one per point) for a single-series chart', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(bar, theme, container, 400, 300)
    const list = container.querySelector('[role="list"].visual-blerg-series')!
    const items = list.querySelectorAll('[role="listitem"]')
    expect(items.length).toBe(1) // one series, not one per category/point
    expect(items[0].getAttribute('tabindex')).toBe('0')
  })

  it('arrow keys move the cursor and announce the value via aria-live', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(bar, theme, container, 400, 300)
    const item = container.querySelector('[role="listitem"]') as HTMLElement
    const live = container.querySelector('[aria-live="polite"]')!
    // Vega-Lite's default nominal domain sort is alphabetical (confirmed empirically, design
    // doc §3.3/color.ts): "Feb" < "Jan" < "Mar", so that's the drawn and cursor order, not the
    // input order.
    item.dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowRight', bubbles: true, cancelable: true}))
    expect(live.textContent).toBe('Jan: 3') // cursor starts at Feb (index 0); the first move is forward
    item.dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowRight', bubbles: true, cancelable: true}))
    expect(live.textContent).toBe('Mar: 2')
    item.dispatchEvent(new KeyboardEvent('keydown', {key: 'End', bubbles: true, cancelable: true}))
    expect(live.textContent).toBe('Mar: 2')
    item.dispatchEvent(new KeyboardEvent('keydown', {key: 'Home', bubbles: true, cancelable: true}))
    expect(live.textContent).toBe('Feb: 4')
    // A key the handler doesn't own leaves the announcement alone.
    item.dispatchEvent(new KeyboardEvent('keydown', {key: 'Tab', bubbles: true, cancelable: true}))
    expect(live.textContent).toBe('Feb: 4')
  })

  it('the data table shows the drawn (post-aggregation) rows, truthfully captioned (M9)', () => {
    const aggregated = {
      data: {values: [{month: 'Jan', open: 1}, {month: 'Jan', open: 2}, {month: 'Feb', open: 5}]},
      mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative', aggregate: 'sum'}},
    }
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(aggregated, theme, container, 400, 300)
    const table = container.querySelector('table')!
    // Two input rows for "Jan" collapse into one drawn (summed) row — the table reflects that,
    // not the three raw input rows. "Feb" sorts first (alphabetical domain order).
    const bodyRows = [...table.querySelectorAll('tr')].slice(1) // skip the header row
    expect(bodyRows).toHaveLength(2)
    expect(bodyRows[0].textContent).toContain('Feb')
    expect(bodyRows[0].textContent).toContain('5')
    expect(bodyRows[1].textContent).toContain('Jan')
    expect(bodyRows[1].textContent).toContain('3') // 1 + 2 summed
    const caption = table.querySelector('caption')!
    expect(caption.textContent).toBe('2 rows') // under the cap: no "Showing N of M" needed
  })

  it('caps the table and captions it truthfully when there are more drawn rows than the cap', () => {
    const many = Array.from({length: 600}, (_, i) => ({month: `m${i}`, open: i}))
    const spec = {data: {values: many}, mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(spec, theme, container, 400, 300)
    const table = container.querySelector('table')!
    const bodyRows = [...table.querySelectorAll('tr')].slice(1)
    expect(bodyRows).toHaveLength(500)
    expect(table.querySelector('caption')!.textContent).toBe('Showing 500 of 600 rows')
  })

  it('marks a card key in a data-table label with a chip span (BLG-485), as a plain cell otherwise', () => {
    const spec = {
      data: {values: [{month: 'BLG-452', open: 3}, {month: 'Plain', open: 1}]},
      mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}},
    }
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(spec, theme, container, 400, 300)
    const table = container.querySelector('table')!
    const rows = [...table.querySelectorAll('tr')].slice(1)
    const chipRow = rows.find(r => r.textContent?.includes('BLG-452'))!
    const plainRow = rows.find(r => r.textContent?.includes('Plain'))!
    const chip = chipRow.querySelector('[data-blerg-chip-key]')!
    expect(chip.getAttribute('data-blerg-chip-key')).toBe('card:BLG:452')
    expect(chip.textContent).toBe('BLG-452')
    expect(plainRow.querySelector('[data-blerg-chip-key]')).toBeNull()
  })

  it('a composed (faceted) spec draws without the single-series accessibility layer (a stated v1 gap)', () => {
    const faceted = {data: {values}, facet: {field: 'month', type: 'nominal'}, spec: {mark: 'bar', encoding: bar.encoding}}
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    renderBlergChart(faceted, theme, container, 400, 300)
    expect(container.querySelector('[role="list"].visual-blerg-series')).toBeNull()
    expect(container.querySelector('table')).toBeNull()
  })

  it('dispose() removes the SVG and every accessibility extra', () => {
    const container = document.createElement('div')
    const theme = readVisualTheme(container)
    const result = renderBlergChart(bar, theme, container, 400, 300)
    expect(container.children.length).toBeGreaterThan(1)
    result.dispose()
    expect(container.children.length).toBe(0)
  })
})

it('refreshes the retained table without allocating accessibility wrappers',()=>{
 const extras=buildAccessibleExtras([{key:'a',label:'A',value:'1'}],readVisualTheme(document.body)),table=extras[2].querySelector('table')!
 const create=vi.spyOn(document,'createElement')
 try {
  extras.update([{key:'a',label:'A',value:'2'}])
  expect(extras[2].querySelector('table')).toBe(table);expect(table).toHaveTextContent('2')
  expect(create.mock.calls.map(([tag])=>tag)).not.toContain('details')
  expect(create.mock.calls.map(([tag])=>tag)).not.toContain('div')
 } finally {create.mockRestore()}
})
