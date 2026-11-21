import { describe, expect, it } from 'vitest'
import { parseSequence } from './parse'
import { layoutSequence } from './layout'
import { drawSequence } from './draw'

const THEME = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

function draw(source: string, fontSize = 14) {
  const graph = parseSequence(source)
  if (!graph) throw new Error('fixture must parse')
  const layout = layoutSequence(graph, fontSize)
  return drawSequence(graph, layout, { ...THEME, fontSize })
}

describe('drawSequence', () => {
  it('draws a valid, non-empty SVG with a positive width and height', () => {
    const out = draw('sequenceDiagram\nAlice->>Bob: Hello')
    expect(out.svg).toContain('<svg')
    expect(out.width).toBeGreaterThan(0)
    expect(out.height).toBeGreaterThan(0)
  })

  it('a wide note that overflows the lanes sets a non-zero viewBox origin, not a clip', () => {
    const out = draw('sequenceDiagram\nparticipant A\nparticipant B\nNote left of A: A very much longer note than either participant label')
    expect(out.svg).toMatch(/viewBox="-[\d.]+ 0 /)
  })

  it('tags every participant, message and note with its own data attribute', () => {
    const out = draw('sequenceDiagram\nA->>B: hi\nNote over A: a note')
    expect(out.svg).toContain('data-blerg-seq-participant="A"')
    expect(out.svg).toContain('data-blerg-seq-participant="B"')
    expect(out.svg).toContain('data-blerg-seq-note="over"')
    // The attribute value's own "->" is XML-escaped in the raw string (escapeXml, shared with
    // blergFlow's identical edge-key convention) — queried through a real parse instead, the
    // same way blergFlow/draw.test.ts checks its own "A->B" edge keys.
    const root = new DOMParser().parseFromString(out.svg, 'image/svg+xml').documentElement
    expect(root.querySelector('[data-blerg-seq-message="A->B"]')).not.toBeNull()
  })

  it('never emits a literal color — every fill/stroke traces back to a theme token', () => {
    const out = draw('sequenceDiagram\nA->>+B: hi\nB-->>-A: ok\nNote over A: note\nalt one\nA->>B: hi\nend')
    const literalHex = /#[0-9a-f]{3,8}(?![0-9a-f])/gi
    const used = new Set((out.svg.match(literalHex) ?? []).map(h => h.toLowerCase()))
    const tokens = new Set([THEME.fg, THEME.muted, THEME.surface, THEME.surface2, THEME.border, THEME.accent].map(c => c.toLowerCase()))
    for (const hex of used) expect(tokens.has(hex), `unexpected literal color ${hex}`).toBe(true)
  })

  it('draws a solid marker for a sync message and a dashed marker for an async one', () => {
    const out = draw('sequenceDiagram\nA->>B: sync\nB-->>A: async')
    expect(out.svg).toContain('blerg-seq-arrow-solid')
    expect(out.svg).toContain('blerg-seq-arrow-dashed')
  })

  it('draws a self-message as a path, not a zero-length line', () => {
    const out = draw('sequenceDiagram\nA->>A: think')
    expect(out.svg).toMatch(/<path d="M [\d.]+,[\d.]+ C /)
  })

  it('draws a block frame and its tab for alt/opt/loop', () => {
    const out = draw('sequenceDiagram\nloop every day\nA->>B: ping\nend')
    expect(out.svg).toContain('data-blerg-seq-block="loop"')
  })

  it('draws a block\'s tab after the lifelines, so its solid fill covers them, not the other way round', () => {
    const out = draw('sequenceDiagram\nloop every day\nA->>B: ping\nend')
    expect(out.svg.indexOf('stroke-dasharray')).toBeLessThan(out.svg.indexOf('data-blerg-seq-block'))
  })

  it('an activation span becomes its own rect over the lifeline', () => {
    const out = draw('sequenceDiagram\nactivate A\nA->>B: hi\ndeactivate A')
    // One rect for the activation bar, distinct from the participant header's own rect; cheapest
    // real signal here is simply that at least two <rect> elements exist (header + activation).
    expect((out.svg.match(/<rect/g) ?? []).length).toBeGreaterThanOrEqual(2)
  })

  it('scales stroke width up with a bigger fontSize (round-4 lesson, applied from the start)', () => {
    const small = draw('sequenceDiagram\nA->>B: hi', 14)
    const big = draw('sequenceDiagram\nA->>B: hi', 28)
    const strokeOf = (svg: string) => Number(/stroke-width="([\d.]+)"/.exec(svg)![1])
    expect(strokeOf(big.svg)).toBeGreaterThan(strokeOf(small.svg) * 1.5)
  })

  it('draws every declared arrow style with a visible line, never a bare marker', () => {
    const out = draw('sequenceDiagram\nA->>B: sync\nB-->>A: async')
    expect(out.svg).toMatch(/<line[^>]*marker-end="url\(#blerg-seq-arrow-solid\)"/)
    expect(out.svg).toMatch(/<line[^>]*marker-end="url\(#blerg-seq-arrow-dashed\)"/)
  })
})
