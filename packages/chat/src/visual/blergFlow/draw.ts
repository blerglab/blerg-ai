// BLG-468 §4/§5/§6: the one canonical SVG string this renderer draws, from the parsed graph
// (ast.ts) and its layout (layout.ts) alone — theme tokens only (VisualTheme), never a literal
// color. This is the exact string returned as RenderedVisual.svg; render.ts then sanitizes it
// (sanitizeSvg) and parses the live DOM *from* that sanitized string, the same sequence BLG-452
// already uses, so the on-screen diagram and the exported SVG can never visually disagree.
import { attrs, circle as svgCircle, escapeXml, group, rect as svgRect, text as svgText } from '../blerg/svg'
import type { FlowGraph, FlowNode } from './ast'
import type { FlowLayout } from './layout'
import type { VisualTheme } from '../theme'
import { labelRefKey } from '../refChip'
import { measureTextBox } from './sizing'

type Attrs = Record<string, string | number | undefined | false>

function polygon(points: ReadonlyArray<{ x: number; y: number }>, attributes: Attrs = {}): string {
  return `<polygon${attrs({ points: points.map(p => `${p.x},${p.y}`).join(' '), ...attributes })}/>`
}
function path(d: string, attributes: Attrs = {}): string {
  return `<path${attrs({ d, ...attributes })}/>`
}
function marker(id: string, color: string): string {
  return `<marker id="${id}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">`
    + `<path d="M0,0 L10,5 L0,10 z" fill="${escapeXml(color)}"/></marker>`
}

type Point = { x: number; y: number }

// Review round 2 (§2): a back edge (A->B plus a separate B->A) routed through dagre's own raw
// points lands right on top of the forward edge — both go directly between the same two points,
// reading as one doubled line (or, with a label, a shape that looks like its own node). Routed to
// its own side instead: a perpendicular bow through the midpoint (or, for a longer dagre-routed
// polyline, every interior point), signed by the edge's own (from, to) order so a mirrored pair
// always bows to opposite sides of each other, deterministically — not by draw order.
function perpOffset(a: Point, b: Point, amount: number): Point {
  const dx = b.x - a.x, dy = b.y - a.y
  const len = Math.hypot(dx, dy) || 1
  return { x: (-dy / len) * amount, y: (dx / len) * amount }
}

function bowPoints(points: readonly Point[], sign: number, amount: number): Point[] {
  if (points.length < 2) return [...points]
  const first = points[0], last = points[points.length - 1]
  const perp = perpOffset(first, last, amount * sign)
  if (points.length === 2) {
    return [first, { x: (first.x + last.x) / 2 + perp.x, y: (first.y + last.y) / 2 + perp.y }, last]
  }
  return points.map((p, i) => (i === 0 || i === points.length - 1) ? p : { x: p.x + perp.x, y: p.y + perp.y })
}

/** A smooth curve through 3+ points (a quadratic through each point, meeting its neighbor at
 *  their shared midpoint) — straight when there are only two, the common case for a direct edge
 *  between adjacent ranks. Not used for a 3-point bow (bowPoints' own 2-node case): that one
 *  quadratic segment ends at the *midpoint* of its own control and end point, then runs straight
 *  the rest of the way — correct for smoothing a long dagre-routed polyline, but an asymmetric
 *  shape for a single bow, where `bowQuadraticD` below draws a true, symmetric curve instead. */
function smoothPathD(points: readonly Point[]): string {
  if (points.length < 2) return ''
  if (points.length === 2) return `M ${points[0].x},${points[0].y} L ${points[1].x},${points[1].y}`
  let d = `M ${points[0].x},${points[0].y}`
  for (let i = 1; i < points.length - 1; i++) {
    const p = points[i], next = points[i + 1]
    d += ` Q ${p.x},${p.y} ${(p.x + next.x) / 2},${(p.y + next.y) / 2}`
  }
  const last = points[points.length - 1]
  return d + ` L ${last.x},${last.y}`
}

/** A single quadratic bezier through exactly [start, control, end] — bowPoints' own 2-node case.
 *  Review round 4 (§2): the generic smoothPathD above treats its middle point as a *control*
 *  point for a curve that then runs straight the rest of the way, never actually touching it —
 *  anchoring a label there (as round 2's own fix did) put it off the real curve, which is why the
 *  arc still looked broken near the label even after round 2's background-sizing fix. This one
 *  draws the real, symmetric bow the 3-point case is meant to be. */
function bowQuadraticD(points: readonly [Point, Point, Point]): string {
  const [start, control, end] = points
  return `M ${start.x},${start.y} Q ${control.x},${control.y} ${end.x},${end.y}`
}

/** The point actually on a quadratic bezier at t=0.5 — where a label anchored at the raw control
 *  point (not on the curve at all) would sit visibly off to one side of it. */
function quadraticMidpoint(start: Point, control: Point, end: Point): Point {
  return {
    x: 0.25 * start.x + 0.5 * control.x + 0.25 * end.x,
    y: 0.25 * start.y + 0.5 * control.y + 0.25 * end.y,
  }
}

/** Multiple sibling `<text>` lines (not `<tspan>`s): the shared `text()` helper already escapes
 *  and positions one line; a label's `\n` (label.ts's own `<br>` → line-break rule) just repeats
 *  it, vertically centered as a block. */
function labelLines(lines: readonly string[], cx: number, cy: number, fontSize: number, fill: string, chip = false): string {
  const lineHeight = fontSize * 1.3
  const firstBaseline = cy - ((lines.length - 1) * lineHeight) / 2 + fontSize * 0.35
  return group(lines.map((line, index) => svgText(cx, firstBaseline + index * lineHeight, line, {
    'text-anchor': 'middle', 'font-size': fontSize, fill, 'text-decoration': chip ? 'underline' : undefined,
  })))
}

function drawNodeShape(node: FlowNode, x: number, y: number, width: number, height: number, theme: VisualTheme, strokeWidth: number): string {
  const common: Attrs = { fill: theme.surface2, stroke: theme.border, 'stroke-width': strokeWidth }
  switch (node.shape) {
    case 'rect': return svgRect(x, y, width, height, { ...common, rx: 3, ry: 3 })
    case 'round': return svgRect(x, y, width, height, { ...common, rx: Math.min(10, height / 4), ry: Math.min(10, height / 4) })
    case 'stadium': return svgRect(x, y, width, height, { ...common, rx: height / 2, ry: height / 2 })
    case 'circle': return svgCircle(x + width / 2, y + height / 2, Math.min(width, height) / 2, common)
    case 'diamond': return polygon([
      { x: x + width / 2, y }, { x: x + width, y: y + height / 2 },
      { x: x + width / 2, y: y + height }, { x, y: y + height / 2 },
    ], common)
    case 'subroutine': return svgRect(x, y, width, height, common)
    default: return svgRect(x, y, width, height, common)
  }
}

// subroutine's double border: two short inset vertical lines near the left/right edges, drawn
// after the outer rect so they sit on top of its fill.
function subroutineInsets(x: number, y: number, width: number, height: number, theme: VisualTheme, strokeWidth: number): string {
  const inset = 6
  const line = (lx: number) => `<line x1="${lx}" y1="${y}" x2="${lx}" y2="${y + height}" stroke="${theme.border}" stroke-width="${strokeWidth}"/>`
  return line(x + inset) + line(x + width - inset)
}

export interface DrawnFlowchart {
  svg: string
  width: number
  height: number
}

export function drawFlowchart(graph: FlowGraph, layout: FlowLayout, theme: VisualTheme): DrawnFlowchart {
  const fontSize = theme.fontSize
  // Proportional to fontSize (14px's own existing value as the baseline), same reason as
  // layout.ts's own rank/node spacing (review round 3 §1): a fixed pixel margin would stay the
  // same size next to text that's grown or shrunk to fill a pane.
  const scale = fontSize / 14
  const margin = Math.round(12 * scale)
  const width = Math.max(1, Math.ceil(layout.width + margin * 2))
  const height = Math.max(1, Math.ceil(layout.height + margin * 2))
  const dx = (value: number) => value + margin
  const dy = (value: number) => value + margin
  // Review round 4 (§1): a node/edge stroke, an arrowhead and the subgraph border are all drawn
  // in absolute pixels too, same as the spacing above — at a 2x text scale they stayed hairline
  // while everything around them doubled, so a connection almost disappeared. Arrowheads need no
  // separate scaling call of their own: SVG's own default `markerUnits="strokeWidth"` already
  // sizes a `<marker>` as a multiple of the stroke width of whatever references it, so scaling
  // the edge's own stroke-width (below) scales its arrowhead the same way, for free.
  const strokeWidth = 1.5 * scale
  const thickStrokeWidth = 3 * scale
  const dash = (pattern: string) => pattern.split(' ').map(n => Math.round(Number(n) * scale * 10) / 10).join(' ')

  const subgraphEls = layout.subgraphs.map(s => {
    const sub = graph.subgraphs.find(g => g.id === s.id)!
    return group([
      svgRect(dx(s.x), dy(s.y), s.width, s.height, { fill: 'none', stroke: theme.border, 'stroke-width': strokeWidth, 'stroke-dasharray': dash('4 3'), rx: 6 }),
      svgText(dx(s.x) + 8, dy(s.y) + fontSize + 2, sub.label, { 'font-size': fontSize * 0.85, fill: theme.muted }),
    ])
  })

  const nodeEls = layout.nodes.map(n => {
    const node = graph.nodes.find(g => g.id === n.id)!
    const chip = labelRefKey(node.label)
    const shape = drawNodeShape(node, dx(n.x), dy(n.y), n.width, n.height, theme, strokeWidth)
    const inset = node.shape === 'subroutine' ? subroutineInsets(dx(n.x), dy(n.y), n.width, n.height, theme, strokeWidth) : ''
    const label = labelLines(node.label.split('\n'), dx(n.x) + n.width / 2, dy(n.y) + n.height / 2, fontSize, chip ? theme.accent : theme.fg, !!chip)
    return group([shape, inset, label], {
      'data-blerg-flow-node': node.id,
      'data-blerg-chip-key': chip ?? undefined,
      role: chip ? 'button' : 'group',
      tabindex: 0,
      'aria-label': chip ? `Open ${node.label}` : node.label,
    })
  })

  const BOW = Math.max(14, fontSize)
  const edgeEls = layout.edges.map(e => {
    const edge = graph.edges.find(g => g.from === e.from && g.to === e.to)!
    const mirrored = graph.edges.some(other => other.from === edge.to && other.to === edge.from)
    const sign = edge.from < edge.to ? 1 : -1
    const routed = (mirrored ? bowPoints(e.points, sign, BOW) : e.points).map(p => ({ x: dx(p.x), y: dy(p.y) }))
    // The common case: a mirrored edge between two adjacent-rank nodes, dagre's own 2-point
    // path bowed into exactly 3 (bowPoints, above) — a single, true symmetric quadratic, not
    // smoothPathD's generic multi-segment smoothing (meant for a longer routed polyline, and
    // asymmetric for exactly 3 points — round 4 §2's own finding).
    const singleBow = mirrored && routed.length === 3
    const dashArray = edge.style === 'dotted' ? dash('4 3') : undefined
    const width_ = edge.style === 'thick' ? thickStrokeWidth : strokeWidth
    const markerId = `blerg-flow-arrow-${edge.style}`
    const line = path(singleBow ? bowQuadraticD(routed as [Point, Point, Point]) : smoothPathD(routed), {
      fill: 'none', stroke: theme.border, 'stroke-width': width_, 'stroke-dasharray': dashArray,
      'marker-end': edge.style === 'line' ? undefined : `url(#${markerId})`,
    })
    // On the curve's own real midpoint when routed off to one side (not the raw bow control
    // point, which the curve doesn't actually pass through — round 4 §2); dagre's own label
    // anchor otherwise. Both land clear of the other edge in the pair (round 2's own finding).
    const labelPoint = singleBow ? quadraticMidpoint(routed[0], routed[1], routed[2])
      : mirrored ? routed[Math.floor(routed.length / 2)]
      : e.labelX !== null && e.labelY !== null ? { x: dx(e.labelX), y: dy(e.labelY) } : null
    const labelEl = edge.label && labelPoint
      ? (() => {
        // Review round 2 (§2): the text's own box, padded a few px — not measureLabel's
        // generous node padding, which made the background wide/tall enough to cut a bowed back
        // edge's curve into two visually disconnected pieces either side of it. Review round 4
        // (§2): that pad is proportional to scale too, same reason as everything else in this
        // file — fixed at 4px, it stayed a sliver next to text grown several times over, letting
        // the heuristic text measurement's own small error (sizing.ts's char-width estimate, not
        // real glyph metrics) show as the arc peeking through right next to the label.
        const LABEL_PAD = Math.max(4, Math.round(4 * scale))
        const text = measureTextBox(edge.label!, fontSize - 1)
        const width = text.width + LABEL_PAD * 2, height = text.height + LABEL_PAD * 2
        return group([
          svgRect(labelPoint.x - width / 2, labelPoint.y - height / 2, width, height, { fill: theme.surface }),
          svgText(labelPoint.x, labelPoint.y + fontSize * 0.3, edge.label!, { 'text-anchor': 'middle', 'font-size': fontSize * 0.9, fill: theme.fg }),
        ])
      })()
      : ''
    return group([line, labelEl], { 'data-blerg-flow-edge': `${e.from}->${e.to}`, tabindex: 0, role: 'group', 'aria-label': `${e.from} to ${e.to}${edge.label ? `: ${edge.label}` : ''}` })
  })

  const defs = `<defs>${marker('blerg-flow-arrow-solid', theme.border)}${marker('blerg-flow-arrow-dotted', theme.border)}${marker('blerg-flow-arrow-thick', theme.border)}</defs>`
  const body = defs + subgraphEls.join('') + edgeEls.join('') + nodeEls.join('')
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" font-family="${escapeXml(theme.fontFamily)}">${body}</svg>`
  return { svg, width, height }
}
