import { describe, expect, it } from 'vitest'
import { tryRenderGantt } from './render'
import type { VisualFit } from '../chartSize'

const THEME = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

const NOW = new Date('2025-01-09T00:00:00Z')
const SOURCE = 'gantt\n title Release plan\n dateFormat YYYY-MM-DD\n section Design\n Plan :done, a, 2025-01-06, 2d\n Build :crit, active, b, after a, 3d\n Ship :milestone, after b, 0d'

function fit(width: number, { height = 0, fill = false } = {}): VisualFit {
  return { available: { width, height }, fill }
}

describe('tryRenderGantt', () => {
  it('returns null (falls back) for an unsupported diagram, touching the container', () => {
    const container = document.createElement('div')
    expect(tryRenderGantt('flowchart LR\nA --> B', container, THEME, fit(1024), undefined, NOW)).toBeNull()
    expect(container.children).toHaveLength(0)
  })

  it('attaches the sanitized SVG and aria-live sibling, and returns a matching RenderedVisual with its own toolbar', () => {
    const container = document.createElement('div')
    const visual = tryRenderGantt(SOURCE, container, THEME, fit(1024), undefined, NOW)
    expect(visual).not.toBeNull()
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-gantt"]')
    expect(svg).not.toBeNull()
    expect(container.querySelector('[aria-live="polite"]')).not.toBeNull()
    // The zoom toolbar is never appended into `container` (the first round's own "floating over
    // the content" finding) — it's handed back on the RenderedVisual instead, for the app's
    // existing chrome (VisualBlock.tsx) to mount wherever it belongs.
    expect(container.querySelector('button[aria-label="Zoom in"]')).toBeNull()
    expect(visual!.toolbar).toBeInstanceOf(HTMLElement)
    expect(visual!.toolbar!.querySelector('button[aria-label="Zoom in"]')).not.toBeNull()
    expect(visual!.toolbar!.querySelector('button[aria-label="Zoom out"]')).not.toBeNull()
    expect(visual!.toolbar!.querySelector('button[aria-label="Fit to width"]')).not.toBeNull()
    for (const button of visual!.toolbar!.querySelectorAll('button')) {
      expect(button.className).toBe('refresh-control viewer-action')
    }
    expect(visual!.svg).toContain('data-blerg-gantt-task="a"')
    expect(visual!.width).toBeGreaterThan(0)
  })

  it('the scroll host is horizontally scrollable', () => {
    const container = document.createElement('div')
    tryRenderGantt(SOURCE, container, THEME, fit(200), undefined, NOW)
    const scrollHost = container.querySelector<HTMLElement>('.visual-blerg-gantt-scroll')!
    expect(scrollHost.style.overflowX).toBe('auto')
  })

  it('clicking Zoom in reruns layout at a wider pxPerDay — a real relayout, not a CSS transform', () => {
    const container = document.createElement('div')
    const visual = tryRenderGantt(SOURCE, container, THEME, fit(1024), undefined, NOW)!
    const before = Number(container.querySelector('svg[data-diagram-renderer="blerg-gantt"]')!.getAttribute('width'))
    visual.toolbar!.querySelector<HTMLButtonElement>('button[aria-label="Zoom in"]')!.click()
    const after = Number(container.querySelector('svg[data-diagram-renderer="blerg-gantt"]')!.getAttribute('width'))
    expect(after).toBeGreaterThan(before)
    expect(container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-gantt"]')!.style.transform).toBe('')
  })

  it('clicking Zoom out narrows the chart, bounded by the zoom clamp', () => {
    const container = document.createElement('div')
    const visual = tryRenderGantt(SOURCE, container, THEME, fit(1024), undefined, NOW)!
    const before = Number(container.querySelector('svg[data-diagram-renderer="blerg-gantt"]')!.getAttribute('width'))
    visual.toolbar!.querySelector<HTMLButtonElement>('button[aria-label="Zoom out"]')!.click()
    const after = Number(container.querySelector('svg[data-diagram-renderer="blerg-gantt"]')!.getAttribute('width'))
    expect(after).toBeLessThan(before)
  })

  it('the exported RenderedVisual stays the natural, un-zoomed extent after a zoom interaction', () => {
    const container = document.createElement('div')
    const visual = tryRenderGantt(SOURCE, container, THEME, fit(1024), undefined, NOW)!
    const naturalWidth = visual.width
    visual.toolbar!.querySelector<HTMLButtonElement>('button[aria-label="Zoom in"]')!.click()
    expect(visual.width).toBe(naturalWidth)
  })

  it('scrolling the scroll host translates the pinned lane column, not the rest of the chart', () => {
    const container = document.createElement('div')
    document.body.append(container)
    tryRenderGantt(SOURCE, container, THEME, fit(200), undefined, NOW)
    const scrollHost = container.querySelector<HTMLElement>('.visual-blerg-gantt-scroll')!
    Object.defineProperty(scrollHost, 'scrollLeft', { value: 42, writable: true })
    scrollHost.dispatchEvent(new Event('scroll'))
    const laneColumn = container.querySelector('[data-blerg-gantt-lane-column]')!
    expect(laneColumn.getAttribute('transform')).toBe('translate(42,0)')
    container.remove()
  })

  it('dispose empties the container', () => {
    const container = document.createElement('div')
    const visual = tryRenderGantt(SOURCE, container, THEME, fit(1024), undefined, NOW)!
    visual.dispose()
    expect(container.children).toHaveLength(0)
  })

  it('announces a task\'s aria-label into the live region on focus', () => {
    const container = document.createElement('div')
    document.body.append(container)
    tryRenderGantt(SOURCE, container, THEME, fit(1024), undefined, NOW)
    const node = container.querySelector('[data-blerg-gantt-task="a"]') as unknown as HTMLElement
    const live = container.querySelector('[aria-live="polite"]')!
    node.dispatchEvent(new FocusEvent('focus'))
    expect(live.textContent).toContain('Plan')
    container.remove()
  })

  it('hovering a task highlights its own dependency\'s bar outline and connector line', () => {
    const container = document.createElement('div')
    document.body.append(container)
    tryRenderGantt(SOURCE, container, THEME, fit(1024), undefined, NOW)
    const build = container.querySelector('[data-blerg-gantt-task="b"]') as unknown as HTMLElement
    const planShape = container.querySelector('[data-blerg-gantt-bar-shape="a"]')!
    const depLine = container.querySelector('[data-blerg-gantt-dep-line="a->b"]')!
    expect(depLine.getAttribute('opacity')).toBe('0')
    build.dispatchEvent(new MouseEvent('mouseenter'))
    expect(planShape.getAttribute('stroke')).toBe(THEME.accent)
    expect(depLine.getAttribute('opacity')).toBe('1')
    build.dispatchEvent(new MouseEvent('mouseleave'))
    expect(depLine.getAttribute('opacity')).toBe('0')
    container.remove()
  })

  it('activating a chip task calls onChipOpen with its resolved card key', () => {
    const container = document.createElement('div')
    document.body.append(container)
    const opened: string[] = []
    tryRenderGantt('gantt\n section S\n BLG-468 : t1, 2024-01-01, 1d', container, THEME, fit(1024), key => opened.push(key), NOW)
    const chip = container.querySelector('[data-blerg-chip-key]') as unknown as HTMLElement
    chip.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(opened).toEqual(['card:BLG:468'])
    container.remove()
  })

  it('a plain task (no chip) ignores click — no callback, no throw', () => {
    const container = document.createElement('div')
    const opened: string[] = []
    tryRenderGantt('gantt\n section S\n Plain : t1, 2024-01-01, 1d', container, THEME, fit(1024), key => opened.push(key), NOW)
    const node = container.querySelector('[data-blerg-gantt-task="t1"]') as unknown as HTMLElement
    expect(() => node.dispatchEvent(new MouseEvent('click', { bubbles: true }))).not.toThrow()
    expect(opened).toEqual([])
  })

  it('a filling (tab) render fits the chart to the available width up front, and the export matches it', () => {
    const container = document.createElement('div')
    const chatVisual = tryRenderGantt(SOURCE, document.createElement('div'), THEME, fit(300), undefined, NOW)!
    const visual = tryRenderGantt(SOURCE, container, THEME, fit(2000, { height: 2000, fill: true }), undefined, NOW)!
    const svgWidth = Number(container.querySelector('svg[data-diagram-renderer="blerg-gantt"]')!.getAttribute('width'))
    // Fit-to-width targets the available width directly (no font-scale search needed, render.ts's
    // own header) — close to it, not just "wider than chat's own un-fit natural size". The
    // exported RenderedVisual reflects this same fitted draw (review round 2: it used to always
    // stay at the chat-sized DEFAULT_PX_PER_DAY even when filling, same bug blergFlow/render.ts's
    // own header already explains for its own kind).
    expect(svgWidth).toBeGreaterThan(chatVisual.width)
    expect(svgWidth).toBeLessThanOrEqual(2010)
    expect(visual.width).toBe(svgWidth)
  })

  it('a filling (tab) render grows text/row height up to MAX_FILL_SCALE, bounded by the available height', () => {
    const chatVisual = tryRenderGantt(SOURCE, document.createElement('div'), THEME, fit(1024), undefined, NOW)!
    const tallFill = tryRenderGantt(SOURCE, document.createElement('div'), THEME, fit(2000, { height: 4000, fill: true }), undefined, NOW)!
    expect(tallFill.height).toBeGreaterThan(chatVisual.height)
    expect(tallFill.height).toBeLessThanOrEqual(chatVisual.height * 2 * 1.1)
    const shortFill = tryRenderGantt(SOURCE, document.createElement('div'), THEME, fit(2000, { height: 120, fill: true }), undefined, NOW)!
    expect(shortFill.height).toBeLessThanOrEqual(120 + 1)
  })
})
