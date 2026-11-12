import { describe, expect, it } from 'vitest'
import { tryRenderSequence } from './render'
import type { VisualFit } from '../chartSize'

const THEME = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

function fit(width: number, { height = 0, fill = false } = {}): VisualFit {
  return { available: { width, height }, fill }
}

describe('tryRenderSequence', () => {
  it('returns null (falls back) for an unsupported diagram, touching the container', () => {
    const container = document.createElement('div')
    expect(tryRenderSequence('flowchart LR\nA --> B', container, THEME, fit(1024))).toBeNull()
    expect(container.children).toHaveLength(0)
  })

  it('attaches the sanitized SVG and an aria-live sibling, and returns a matching RenderedVisual', () => {
    const container = document.createElement('div')
    const visual = tryRenderSequence('sequenceDiagram\nAlice->>Bob: Hello', container, THEME, fit(1024))
    expect(visual).not.toBeNull()
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')
    expect(svg).not.toBeNull()
    expect(container.querySelector('[aria-live="polite"]')).not.toBeNull()
    expect(visual!.svg).toContain('data-blerg-seq-participant="Alice"')
    expect(visual!.width).toBeGreaterThan(0)
  })

  it('a non-fill diagram never draws larger than its own natural size, even in a very wide message', () => {
    const container = document.createElement('div')
    const natural = tryRenderSequence('sequenceDiagram\nA->>B: hi', document.createElement('div'), THEME, fit(0))!
    tryRenderSequence('sequenceDiagram\nA->>B: hi', container, THEME, fit(1200))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')!
    expect(Number(svg.getAttribute('width'))).toBe(natural.width)
  })

  it('a non-fill diagram shrinks to fit a message narrower than its own natural size', () => {
    const container = document.createElement('div')
    const visual = tryRenderSequence('sequenceDiagram\nparticipant Alice\nparticipant Bob\nAlice->>Bob: Request\nBob-->>Alice: Response', container, THEME, fit(100))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')!
    expect(Number(svg.getAttribute('width'))).toBe(100)
    expect(Number(svg.getAttribute('width'))).toBeLessThan(visual!.width)
  })

  it('a filling diagram grows past its natural size, capped at MAX_FILL_SCALE (2x text)', () => {
    const container = document.createElement('div')
    const natural = tryRenderSequence('sequenceDiagram\nA->>B: hi', document.createElement('div'), THEME, fit(0))!
    tryRenderSequence('sequenceDiagram\nA->>B: hi', container, THEME, fit(4000, { height: 4000, fill: true }))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')!
    const width = Number(svg.getAttribute('width'))
    expect(width).toBeGreaterThan(natural.width)
    expect(width).toBeLessThanOrEqual(natural.width * 2 * 1.05)
  })

  it('a filling diagram is bounded by the available height, never needing to scroll', () => {
    const container = document.createElement('div')
    tryRenderSequence('sequenceDiagram\nA->>B: m1\nB->>A: m2\nA->>B: m3\nB->>A: m4', container, THEME, fit(2000, { height: 200, fill: true }))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')!
    expect(Number(svg.getAttribute('height'))).toBeLessThanOrEqual(200)
  })

  it('a filling diagram is never smaller than the same diagram in chat', () => {
    const source = 'sequenceDiagram\nAlice->>+Bob: Request\nBob-->>-Alice: Response'
    const chatContainer = document.createElement('div')
    const tabContainer = document.createElement('div')
    tryRenderSequence(source, chatContainer, THEME, fit(2400))
    // A sequence diagram's natural aspect is far taller, relative to its width, than a short
    // flowchart's own — generous height headroom here keeps width (not height) the binding, and
    // so the testable, constraint (the same "height has generous headroom" scoping
    // blergFlow/VisualFlowchart.browser.test.tsx's own round-4 review finding needed).
    tryRenderSequence(source, tabContainer, THEME, fit(2400, { height: 8000, fill: true }))
    const chatWidth = Number(chatContainer.querySelector('svg[data-diagram-renderer="blerg-sequence"]')!.getAttribute('width'))
    const tabWidth = Number(tabContainer.querySelector('svg[data-diagram-renderer="blerg-sequence"]')!.getAttribute('width'))
    // Within the bounded refinement loop's own 2% convergence tolerance (render.ts), not exact.
    expect(tabWidth).toBeGreaterThanOrEqual(chatWidth * 0.95)
  })

  it('a filling diagram is centered in the available space', () => {
    const container = document.createElement('div')
    tryRenderSequence('sequenceDiagram\nA->>B: hi', container, THEME, fit(4000, { height: 4000, fill: true }))
    const svg = container.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')!
    expect(svg.style.position).toBe('absolute')
    expect(svg.style.top).toBe('50%')
    expect(svg.style.left).toBe('50%')
    expect(svg.style.transform).toBe('translate(-50%, -50%)')
  })

  it('dispose empties the container', () => {
    const container = document.createElement('div')
    const visual = tryRenderSequence('sequenceDiagram\nA->>B: hi', container, THEME, fit(1024))!
    visual.dispose()
    expect(container.children).toHaveLength(0)
  })

  it('announces a participant\'s aria-label into the live region on focus', () => {
    const container = document.createElement('div')
    document.body.append(container)
    tryRenderSequence('sequenceDiagram\nparticipant Alice\nAlice->>Bob: Hi', container, THEME, fit(1024))
    const node = container.querySelector('[data-blerg-seq-participant="Alice"]') as unknown as HTMLElement
    const live = container.querySelector('[aria-live="polite"]')!
    node.dispatchEvent(new FocusEvent('focus'))
    expect(live.textContent).toBe('Participant Alice')
    container.remove()
  })

  it('activating a chip participant calls onChipOpen with its resolved card key', () => {
    const container = document.createElement('div')
    document.body.append(container)
    const opened: string[] = []
    tryRenderSequence('sequenceDiagram\nparticipant A as BLG-468\nA->>B: Hi', container, THEME, fit(1024), key => opened.push(key))
    const chip = container.querySelector('[data-blerg-chip-key]') as unknown as HTMLElement
    chip.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(opened).toEqual(['card:BLG:468'])
    container.remove()
  })

  it('a plain participant (no chip) ignores click — no callback, no throw', () => {
    const container = document.createElement('div')
    const opened: string[] = []
    tryRenderSequence('sequenceDiagram\nA->>B: hi', container, THEME, fit(1024), key => opened.push(key))
    const node = container.querySelector('[data-blerg-seq-participant="A"]') as unknown as HTMLElement
    expect(() => node.dispatchEvent(new MouseEvent('click', { bubbles: true }))).not.toThrow()
    expect(opened).toEqual([])
  })
})
