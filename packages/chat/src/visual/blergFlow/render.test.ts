import { describe, expect, it } from 'vitest'
import { tryRenderFlowchart } from './render'
import type { VisualFit } from '../chartSize'

const THEME = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

function fit(width: number, { height = 0, fill = false } = {}): VisualFit {
  return { available: { width, height }, fill }
}

describe('tryRenderFlowchart', () => {
  it('returns null (falls back) for an unsupported diagram, touching the container', () => {
    const container = document.createElement('div')
    expect(tryRenderFlowchart('sequenceDiagram\nAlice->>Bob: Hi', container, THEME, fit(1024))).toBeNull()
    expect(container.children).toHaveLength(0)
  })

  it('attaches the sanitized SVG and an aria-live sibling, and returns a matching RenderedVisual', () => {
    const container = document.createElement('div')
    const visual = tryRenderFlowchart('flowchart LR\nA[Start] --> B[Finish]', container, THEME, fit(1024))
    expect(visual).not.toBeNull()
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')
    expect(svg).not.toBeNull()
    expect(container.querySelector('[aria-live="polite"]')).not.toBeNull()
    // The renderer-identifying attributes are live-DOM only, added after sanitizeSvg — the same
    // split BLG-452's own renderBlergChart uses — so the exported svg string is the plain drawing.
    expect(visual!.svg).toContain('data-blerg-flow-node="A"')
    expect(visual!.width).toBeGreaterThan(0)
  })

  it('a non-fill diagram never draws larger than its own natural size, even in a very wide message', () => {
    const container = document.createElement('div')
    // A tiny two-node LR chain naturally draws far narrower than 1200px.
    const natural = tryRenderFlowchart('flowchart LR\nA --> B', document.createElement('div'), THEME, fit(0))!
    tryRenderFlowchart('flowchart LR\nA --> B', container, THEME, fit(1200))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')!
    expect(Number(svg.getAttribute('width'))).toBe(natural.width)
  })

  it('a non-fill diagram shrinks to fit a message narrower than its own natural size', () => {
    const container = document.createElement('div')
    const visual = tryRenderFlowchart('flowchart LR\nStart([Request]) --> Review{Review}\nReview -->|approve| Approved[Done]', container, THEME, fit(100))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')!
    expect(Number(svg.getAttribute('width'))).toBe(100)
    expect(Number(svg.getAttribute('width'))).toBeLessThan(visual!.width)
  })

  it('a filling diagram grows past its natural size, capped at MAX_FILL_SCALE (2x text)', () => {
    const container = document.createElement('div')
    const natural = tryRenderFlowchart('flowchart LR\nA --> B', document.createElement('div'), THEME, fit(0))!
    tryRenderFlowchart('flowchart LR\nA --> B', container, THEME, fit(4000, { height: 4000, fill: true }))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')!
    const width = Number(svg.getAttribute('width'))
    expect(width).toBeGreaterThan(natural.width)
    expect(width).toBeLessThanOrEqual(natural.width * 2 * 1.05)
  })

  it('a filling diagram is bounded by the available height, never needing to scroll', () => {
    const container = document.createElement('div')
    tryRenderFlowchart('flowchart TB\nA --> B\nB --> C\nC --> D', container, THEME, fit(2000, { height: 200, fill: true }))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')!
    expect(Number(svg.getAttribute('height'))).toBeLessThanOrEqual(200)
  })

  it('review round 3 §1: a filling diagram is never smaller than the same diagram in chat', () => {
    const source = 'flowchart LR\nStart([Request]) --> Review{Review}\nReview -->|approve| Approved[Done]'
    const chatContainer = document.createElement('div')
    const tabContainer = document.createElement('div')
    tryRenderFlowchart(source, chatContainer, THEME, fit(2400))
    tryRenderFlowchart(source, tabContainer, THEME, fit(2400, { height: 1200, fill: true }))
    const chatWidth = Number(chatContainer.querySelector('svg[data-diagram-renderer="blerg-flow"]')!.getAttribute('width'))
    const tabWidth = Number(tabContainer.querySelector('svg[data-diagram-renderer="blerg-flow"]')!.getAttribute('width'))
    expect(tabWidth).toBeGreaterThanOrEqual(chatWidth - 1)
  })

  it('review round 3 §1: a filling diagram is centered in the available space', () => {
    const container = document.createElement('div')
    tryRenderFlowchart('flowchart LR\nA --> B', container, THEME, fit(4000, { height: 4000, fill: true }))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')!
    expect(svg.style.position).toBe('absolute')
    expect(svg.style.top).toBe('50%')
    expect(svg.style.left).toBe('50%')
    expect(svg.style.transform).toBe('translate(-50%, -50%)')
  })

  it('dispose empties the container', () => {
    const container = document.createElement('div')
    const visual = tryRenderFlowchart('flowchart LR\nA --> B', container, THEME, fit(1024))!
    visual.dispose()
    expect(container.children).toHaveLength(0)
  })

  it('announces a node\'s aria-label into the live region on focus', () => {
    const container = document.createElement('div')
    document.body.append(container)
    tryRenderFlowchart('flowchart LR\nA[Start] --> B[Finish]', container, THEME, fit(1024))
    const node = container.querySelector('[data-blerg-flow-node="A"]') as unknown as HTMLElement
    const live = container.querySelector('[aria-live="polite"]')!
    node.dispatchEvent(new FocusEvent('focus'))
    expect(live.textContent).toBe('Start')
    container.remove()
  })

  it('activating a chip node calls onChipOpen with its resolved card key', () => {
    const container = document.createElement('div')
    document.body.append(container)
    const opened: string[] = []
    tryRenderFlowchart('flowchart LR\nA[BLG-468] --> B[Plain]', container, THEME, fit(1024), key => opened.push(key))
    const chip = container.querySelector('[data-blerg-chip-key]') as unknown as HTMLElement
    chip.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(opened).toEqual(['card:BLG:468'])
    container.remove()
  })

  it('a plain node (no chip) ignores click — no callback, no throw', () => {
    const container = document.createElement('div')
    const opened: string[] = []
    tryRenderFlowchart('flowchart LR\nA[Plain] --> B[Also plain]', container, THEME, fit(1024), key => opened.push(key))
    const node = container.querySelector('[data-blerg-flow-node="A"]') as unknown as HTMLElement
    expect(() => node.dispatchEvent(new MouseEvent('click', { bubbles: true }))).not.toThrow()
    expect(opened).toEqual([])
  })

  it('switches a left-right diagram to top-to-bottom only when the authored layout doesn\'t fit', () => {
    const narrow = document.createElement('div')
    const wide = document.createElement('div')
    const narrowVisual = tryRenderFlowchart('flowchart LR\nA[Start] --> B[Finish]', narrow, THEME, fit(100))!
    const wideVisual = tryRenderFlowchart('flowchart LR\nA[Start] --> B[Finish]', wide, THEME, fit(1024))!
    // Stacked (TB) is taller relative to its own width than the same graph laid out LR.
    expect(narrowVisual.height / narrowVisual.width).toBeGreaterThan(wideVisual.height / wideVisual.width)
  })

  it('never reflows a top-to-bottom diagram (already narrow)', () => {
    const container = document.createElement('div')
    const visual = tryRenderFlowchart('flowchart TB\nA[Start] --> B[Finish]', container, THEME, fit(100))!
    expect(visual.width).toBeLessThan(visual.height * 3)
  })

  it('keeps the authored direction when the available width is unmeasured (0)', () => {
    const container = document.createElement('div')
    const lr = tryRenderFlowchart('flowchart LR\nA[Start] --> B[Finish]', container, THEME, fit(0))!
    expect(lr.width).toBeGreaterThan(lr.height)
  })
})
