import { describe, expect, it } from 'vitest'
import { parseFlowchart } from './parse'
import { layoutFlowchart } from './layout'
import { drawFlowchart } from './draw'
import { sanitizeSvg } from '../policy'

const THEME = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

function draw(source: string) {
  const graph = parseFlowchart(source)
  if (!graph) throw new Error('fixture must parse')
  const layout = layoutFlowchart(graph)
  return { graph, layout, drawn: drawFlowchart(graph, layout, THEME) }
}

function parse(svg: string) {
  return new DOMParser().parseFromString(svg, 'image/svg+xml').documentElement
}

describe('drawFlowchart', () => {
  it('draws one group per node and per edge, with a positive width and height', () => {
    const { drawn } = draw('flowchart LR\nA[Start] --> B[Finish]')
    const svg = parse(drawn.svg)
    expect(svg.querySelectorAll('[data-blerg-flow-node]')).toHaveLength(2)
    expect(svg.querySelectorAll('[data-blerg-flow-edge]')).toHaveLength(1)
    expect(drawn.width).toBeGreaterThan(0)
    expect(drawn.height).toBeGreaterThan(0)
  })

  it('every node and edge group is keyboard-focusable', () => {
    const { drawn } = draw('flowchart LR\nA --> B')
    const svg = parse(drawn.svg)
    for (const el of svg.querySelectorAll('[data-blerg-flow-node], [data-blerg-flow-edge]')) {
      expect(el.getAttribute('tabindex')).toBe('0')
    }
  })

  it('draws every theme color token, never a literal hex', () => {
    const { drawn } = draw('flowchart LR\nA --> B')
    expect(drawn.svg).toContain(THEME.surface2)
    expect(drawn.svg).toContain(THEME.border)
    expect(drawn.svg).toContain(THEME.fg)
    // Every color value in the drawn SVG must be one of the theme's own tokens.
    const colors = [...drawn.svg.matchAll(/(?:fill|stroke)="(#[0-9a-f]{3,8}|[a-z]+)"/gi)].map(m => m[1])
    const allowed = new Set([THEME.fg, THEME.muted, THEME.surface, THEME.surface2, THEME.border, THEME.accent, 'none', 'transparent'])
    for (const color of colors) expect(allowed.has(color), color).toBe(true)
  })

  it('marks a node whose label is a card key with a chip attribute', () => {
    const { drawn } = draw('flowchart LR\nA[BLG-468] --> B[Plain]')
    const svg = parse(drawn.svg)
    const a = svg.querySelector('[data-blerg-flow-node="A"]')!
    const b = svg.querySelector('[data-blerg-flow-node="B"]')!
    expect(a.getAttribute('data-blerg-chip-key')).toBe('card:BLG:468')
    expect(a.getAttribute('role')).toBe('button')
    expect(b.hasAttribute('data-blerg-chip-key')).toBe(false)
  })

  it('marks a node whose label is a commit hash with a chip attribute, drawn in the accent token', () => {
    const { drawn } = draw('flowchart LR\nA[08025f2c] --> B[Plain]')
    const svg = parse(drawn.svg)
    const a = svg.querySelector('[data-blerg-flow-node="A"]')!
    expect(a.getAttribute('data-blerg-chip-key')).toBe('commit:08025f2c')
    expect(a.getAttribute('role')).toBe('button')
    expect(a.querySelector('text')!.getAttribute('fill')).toBe(THEME.accent)
    expect(a.querySelector('text')!.getAttribute('text-decoration')).toBe('underline')
  })

  it('draws a visible label for an edge and an aria-label naming its endpoints', () => {
    const { drawn } = draw('flowchart LR\nA -->|go| B')
    expect(drawn.svg).toContain('>go<')
    const svg = parse(drawn.svg)
    expect(svg.querySelector('[data-blerg-flow-edge]')!.getAttribute('aria-label')).toBe('A to B: go')
  })

  it('produces a document sanitizeSvg accepts (no href, image, foreignObject, external resource)', () => {
    const { drawn } = draw([
      'flowchart TB', 'subgraph outer[Platform]', 'A{Decision} --> B([Stadium])', 'end',
      'C((Circle))\nC --> D[[Subroutine]]', 'B --> C',
    ].join('\n'))
    expect(() => sanitizeSvg(drawn.svg)).not.toThrow()
    expect(drawn.svg).not.toMatch(/href=|<image|<foreignObject/i)
  })

  it('draws a dashed stroke for a dotted edge and a thicker stroke for a thick edge', () => {
    const { drawn } = draw('flowchart LR\nA -.-> B\nB ==> C')
    expect(drawn.svg).toContain('stroke-dasharray="4 3"')
    expect(drawn.svg).toContain('stroke-width="3"')
  })
})

describe('drawFlowchart — back-edge routing (review round 2 §2)', () => {
  it('bows a mirrored edge pair apart instead of drawing them on top of each other', () => {
    const { drawn } = draw('flowchart TD\nA --> B\nB -->|reject| A')
    const svg = parse(drawn.svg)
    const paths = [...svg.querySelectorAll('[data-blerg-flow-edge] path')].map(p => p.getAttribute('d')!)
    expect(paths).toHaveLength(2)
    // Each mirrored edge gets at least one quadratic (Q) segment — a bow, not a straight line —
    // and the two bow to opposite sides (their curve commands are not identical).
    for (const d of paths) expect(d).toContain('Q')
    expect(paths[0]).not.toBe(paths[1])
  })

  it('places a mirrored edge\'s label off its own line, with a background behind it', () => {
    const { drawn } = draw('flowchart TD\nA --> B\nB -->|reject| A')
    const svg = parse(drawn.svg)
    const mirroredEdge = [...svg.querySelectorAll('[data-blerg-flow-edge]')].find(g => g.getAttribute('data-blerg-flow-edge') === 'B->A')!
    const labelRect = mirroredEdge.querySelector('rect')
    const labelText = mirroredEdge.querySelector('text')
    expect(labelRect, 'label background').not.toBeNull()
    expect(labelText?.textContent).toBe('reject')
    expect(labelRect!.getAttribute('fill')).toBe(THEME.surface)
  })

  it('sizes a label\'s background to its own text, not a wide node-sized box (§2: a continuous arc)', () => {
    const { drawn } = draw('flowchart TD\nA --> B\nB -->|reject| A')
    const svg = parse(drawn.svg)
    const mirroredEdge = [...svg.querySelectorAll('[data-blerg-flow-edge]')].find(g => g.getAttribute('data-blerg-flow-edge') === 'B->A')!
    const labelRect = mirroredEdge.querySelector('rect')!
    // "reject" at fontSize-1 (13px): a tight text box is roughly 6 chars * 13 * 0.58 ≈ 47px, +8px
    // padding — nowhere near measureLabel's node-oriented ~16px*2 extra padding would add.
    expect(Number(labelRect.getAttribute('width'))).toBeLessThan(70)
    expect(Number(labelRect.getAttribute('height'))).toBeLessThan(30)
  })

  it('draws a non-mirrored edge through dagre\'s own points, unoffset', () => {
    const { graph, layout, drawn } = draw('flowchart LR\nA --> B')
    const svg = parse(drawn.svg)
    const d = svg.querySelector('[data-blerg-flow-edge] path')!.getAttribute('d')!
    const margin = 12
    for (const point of layout.edges[0].points) {
      expect(d, `point (${point.x},${point.y})`).toContain(`${point.x + margin},${point.y + margin}`)
    }
    expect(graph.edges).toHaveLength(1) // sanity: this fixture has no mirror to bow against
  })
})

describe('drawFlowchart — stroke/marker/dash scale with font size (review round 4 §1)', () => {
  function drawAt(source: string, fontSize: number) {
    const graph = parseFlowchart(source)!
    const layout = layoutFlowchart(graph, fontSize)
    return drawFlowchart(graph, layout, { ...THEME, fontSize })
  }

  it('scales node stroke-width, subgraph border stroke-width and edge stroke-width together', () => {
    const natural = drawAt('flowchart LR\nsubgraph s[S]\nA --> B\nend\nB ==> C', 14)
    const big = drawAt('flowchart LR\nsubgraph s[S]\nA --> B\nend\nB ==> C', 140) // 10x
    const naturalSvg = parse(natural.svg), bigSvg = parse(big.svg)
    const naturalNodeStroke = Number(naturalSvg.querySelector('[data-blerg-flow-node] rect')!.getAttribute('stroke-width'))
    const bigNodeStroke = Number(bigSvg.querySelector('[data-blerg-flow-node] rect')!.getAttribute('stroke-width'))
    expect(bigNodeStroke).toBeCloseTo(naturalNodeStroke * 10, 0)

    const naturalEdgeStroke = Number(naturalSvg.querySelector('[data-blerg-flow-edge="A->B"] path')!.getAttribute('stroke-width'))
    const bigEdgeStroke = Number(bigSvg.querySelector('[data-blerg-flow-edge="A->B"] path')!.getAttribute('stroke-width'))
    expect(bigEdgeStroke).toBeCloseTo(naturalEdgeStroke * 10, 0)

    const naturalThickStroke = Number(naturalSvg.querySelector('[data-blerg-flow-edge="B->C"] path')!.getAttribute('stroke-width'))
    const bigThickStroke = Number(bigSvg.querySelector('[data-blerg-flow-edge="B->C"] path')!.getAttribute('stroke-width'))
    expect(bigThickStroke).toBeCloseTo(naturalThickStroke * 10, 0)
    expect(bigThickStroke).toBeGreaterThan(bigEdgeStroke) // thick stays thicker than solid at any scale

    const naturalSubgraphStroke = Number(naturalSvg.querySelector('svg > g > rect')!.getAttribute('stroke-width'))
    const bigSubgraphStroke = Number(bigSvg.querySelector('svg > g > rect')!.getAttribute('stroke-width'))
    expect(bigSubgraphStroke).toBeCloseTo(naturalSubgraphStroke * 10, 0)
  })

  it('scales the dotted edge and subgraph border dash pattern together with stroke width', () => {
    const natural = drawAt('flowchart LR\nsubgraph s[S]\nA --> B\nend\nB -.-> C', 14)
    const big = drawAt('flowchart LR\nsubgraph s[S]\nA --> B\nend\nB -.-> C', 140)
    const naturalDash = parse(natural.svg).querySelector('[data-blerg-flow-edge="B->C"] path')!.getAttribute('stroke-dasharray')!
    const bigDash = parse(big.svg).querySelector('[data-blerg-flow-edge="B->C"] path')!.getAttribute('stroke-dasharray')!
    const naturalFirst = Number(naturalDash.split(' ')[0])
    const bigFirst = Number(bigDash.split(' ')[0])
    expect(bigFirst).toBeCloseTo(naturalFirst * 10, 0)
  })

  it('arrowheads scale with their edge\'s own stroke-width via marker-end\'s default markerUnits', () => {
    // SVG's own default markerUnits="strokeWidth" already does this — this just proves the
    // marker definitions never force a different units mode that would opt back out of it.
    const { drawn } = draw('flowchart LR\nA --> B')
    expect(drawn.svg).not.toContain('markerUnits')
  })

  it('scales a mirrored back edge\'s label padding together with its text', () => {
    const natural = drawAt('flowchart TD\nA --> B\nB -->|reject| A', 14)
    const big = drawAt('flowchart TD\nA --> B\nB -->|reject| A', 140)
    const naturalRect = parse(natural.svg).querySelector('[data-blerg-flow-edge="B->A"] rect')!
    const bigRect = parse(big.svg).querySelector('[data-blerg-flow-edge="B->A"] rect')!
    const naturalW = Number(naturalRect.getAttribute('width')), bigW = Number(bigRect.getAttribute('width'))
    // Both the text (scales with fontSize) and the ~4px pad around it (now also scaled) grow —
    // the background should grow by roughly the same 10x factor as everything else, not stay
    // nearly flat the way an unscaled, fixed-px pad alone would next to 10x-bigger text.
    expect(bigW).toBeGreaterThan(naturalW * 8)
  })
})
