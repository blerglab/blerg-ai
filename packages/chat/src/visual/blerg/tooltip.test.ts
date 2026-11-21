import { describe, expect, it } from 'vitest'
import { tooltipFieldTitle, tooltipFieldValue, tooltipRegionText, attachTooltip, type TooltipRegion } from './tooltip'

describe('tooltipFieldTitle (matches Vega-Lite\'s default tooltip title, confirmed against real Vega output)', () => {
  it('a plain field is its own name', () => expect(tooltipFieldTitle({field: 'y'})).toBe('y'))
  it('count has the special "Count of Records" title, not "Count of y"', () => expect(tooltipFieldTitle({field: 'y', aggregate: 'count'})).toBe('Count of Records'))
  it.each(['sum', 'mean', 'median', 'min', 'max', 'distinct'])('%s capitalizes to "<Aggregate> of <field>"', aggregate => {
    expect(tooltipFieldTitle({field: 'y', aggregate})).toBe(`${aggregate[0]!.toUpperCase()}${aggregate.slice(1)} of y`)
  })
  it('a binned field is titled "<field> (binned)"', () => expect(tooltipFieldTitle({field: 'y', bin: true})).toBe('y (binned)'))
})

describe('tooltipFieldValue', () => {
  it('a plain value is its own string', () => expect(tooltipFieldValue({field: 'y'}, 1.23456)).toBe('1.23456'))
  it('formats temporal fields in UTC with the exact date and time', () => {
    expect(tooltipFieldValue({field: 'x', type: 'temporal'}, '2026-01-05')).toBe('2026-01-05 00:00:00 UTC')
  })
})

describe('tooltipRegionText (the shape the Vega path\'s tooltipText() already formats)', () => {
  it('a single tooltip field is the bare value, no key', () => {
    expect(tooltipRegionText({field: 'y'}, {y: 4})).toBe('4')
  })
  it('an array of tooltip fields is "key: value" lines, one per field', () => {
    expect(tooltipRegionText([{field: 'x'}, {field: 'y'}], {x: 'A', y: 4})).toBe('x: A\ny: 4')
  })
  it('no tooltip channel at all produces no text, matching Vega\'s own no-channel behavior', () => {
    expect(tooltipRegionText(undefined, {x: 'A', y: 4})).toBe('')
  })
})

describe('attachTooltip (hover and touch, design doc §3.4)', () => {
  function svgWithRegion(): {svg: SVGSVGElement; container: HTMLElement; regions: TooltipRegion[]} {
    const container = document.createElement('div')
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg') as unknown as SVGSVGElement
    container.append(svg as unknown as Node)
    document.body.append(container)
    svg.getBoundingClientRect = () => ({left: 0, top: 0, right: 100, bottom: 100, width: 100, height: 100, x: 0, y: 0, toJSON() { return this }})
    container.getBoundingClientRect = () => ({left: 0, top: 0, right: 100, bottom: 100, width: 100, height: 100, x: 0, y: 0, toJSON() { return this }})
    return {svg, container, regions: [{x1: 10, y1: 10, x2: 20, y2: 20, text: 'A: 1'}]}
  }

  it('hover (pointermove) inside a region shows the tooltip text', () => {
    const {svg, container, regions} = svgWithRegion()
    const {tooltip} = attachTooltip(svg, container, regions)
    container.append(tooltip)
    svg.dispatchEvent(new PointerEvent('pointermove', {clientX: 15, clientY: 15}))
    expect(tooltip.hidden).toBe(false)
    expect(tooltip.textContent).toBe('A: 1')
  })

  it('touch (pointerdown) shows the tooltip the same way hover does', () => {
    const {svg, container, regions} = svgWithRegion()
    const {tooltip} = attachTooltip(svg, container, regions)
    container.append(tooltip)
    svg.dispatchEvent(new PointerEvent('pointerdown', {clientX: 15, clientY: 15}))
    expect(tooltip.hidden).toBe(false)
    expect(tooltip.textContent).toBe('A: 1')
  })

  it('moving outside every region hides it again', () => {
    const {svg, container, regions} = svgWithRegion()
    const {tooltip} = attachTooltip(svg, container, regions)
    container.append(tooltip)
    svg.dispatchEvent(new PointerEvent('pointermove', {clientX: 15, clientY: 15}))
    svg.dispatchEvent(new PointerEvent('pointermove', {clientX: 90, clientY: 90}))
    expect(tooltip.hidden).toBe(true)
  })

  it('pointerleave hides the tooltip', () => {
    const {svg, container, regions} = svgWithRegion()
    const {tooltip} = attachTooltip(svg, container, regions)
    container.append(tooltip)
    svg.dispatchEvent(new PointerEvent('pointermove', {clientX: 15, clientY: 15}))
    svg.dispatchEvent(new PointerEvent('pointerleave'))
    expect(tooltip.hidden).toBe(true)
  })

  it('dispose() detaches the listeners', () => {
    const {svg, container, regions} = svgWithRegion()
    const {tooltip, dispose} = attachTooltip(svg, container, regions)
    container.append(tooltip)
    dispose()
    svg.dispatchEvent(new PointerEvent('pointermove', {clientX: 15, clientY: 15}))
    expect(tooltip.hidden).toBe(true)
  })
})

it('keyboard focus and arrows emphasize observations and expose their tooltip', () => {
  const container = document.createElement('div')
  const root = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  root.innerHTML = '<rect data-datum-key="a"/><rect data-datum-key="b"/>'
  container.append(root)
  const live = attachTooltip(root, container, [
    {key:'a', x1:0,y1:0,x2:10,y2:10,text:'First'},
    {key:'b', x1:20,y1:0,x2:30,y2:10,text:'Second'},
  ])
  root.dispatchEvent(new FocusEvent('focus'))
  expect(live.tooltip.textContent).toBe('First')
  expect(root.querySelector('[data-chart-active]')?.getAttribute('data-datum-key')).toBe('a')
  root.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowRight',cancelable:true}))
  expect(live.tooltip.textContent).toBe('Second')
  expect(root.querySelector('[data-chart-active]')?.getAttribute('data-datum-key')).toBe('b')
  root.dispatchEvent(new FocusEvent('blur'))
  expect(live.tooltip.hidden).toBe(true)
  expect(root.querySelector('[data-chart-active]')).toBeNull()
  live.dispose()
})
