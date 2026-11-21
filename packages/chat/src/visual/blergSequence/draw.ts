// BLG-468 §4/§5/§6 (sequence): the one canonical SVG string this renderer draws, from the parsed
// graph (ast.ts) and its layout (layout.ts) alone — theme tokens only (VisualTheme), same
// discipline as blergFlow/draw.ts, including that file's own round-4 lesson applied from the
// start here: every stroke/dash/pad below is `* scale` (fontSize / 14), never a bare constant.
import { circle as svgCircle, escapeXml, group, line as svgLine, rect as svgRect, text as svgText } from '../blerg/svg'
import type { SeqGraph } from './ast'
import type { SeqLayout } from './layout'
import type { VisualTheme } from '../theme'
import { labelRefKey } from '../refChip'

type Attrs = Record<string, string | number | undefined | false>

function marker(id: string, color: string): string {
  return `<marker id="${id}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">`
    + `<path d="M0,0 L10,5 L0,10 z" fill="${escapeXml(color)}"/></marker>`
}

function labelLines(lines: readonly string[], cx: number, cy: number, fontSize: number, fill: string, chip = false): string {
  const lineHeight = fontSize * 1.3
  const firstBaseline = cy - ((lines.length - 1) * lineHeight) / 2 + fontSize * 0.35
  return group(lines.map((line, index) => svgText(cx, firstBaseline + index * lineHeight, line, {
    'text-anchor': 'middle', 'font-size': fontSize, fill, 'text-decoration': chip ? 'underline' : undefined,
  })))
}

function drawActorFigure(cx: number, top: number, height: number, theme: VisualTheme, strokeWidth: number): string {
  const headR = height * 0.22
  const headCy = top + headR
  const bodyTop = headCy + headR
  const bodyBottom = top + height
  const common: Attrs = { stroke: theme.fg, 'stroke-width': strokeWidth, fill: 'none' }
  return group([
    svgCircle(cx, headCy, headR, { ...common, fill: theme.surface2 }),
    svgLine(cx, bodyTop, cx, bodyBottom - headR * 0.6, common),
    svgLine(cx - headR * 1.1, bodyTop + headR * 0.4, cx + headR * 1.1, bodyTop + headR * 0.4, common),
    svgLine(cx, bodyBottom - headR * 0.6, cx - headR, bodyBottom, common),
    svgLine(cx, bodyBottom - headR * 0.6, cx + headR, bodyBottom, common),
  ])
}

export interface DrawnSequence { svg: string; width: number; height: number }

export function drawSequence(graph: SeqGraph, layout: SeqLayout, theme: VisualTheme): DrawnSequence {
  const fontSize = theme.fontSize
  const scale = fontSize / 14
  const strokeWidth = 1.5 * scale
  const width = Math.max(1, Math.ceil(layout.width))
  const height = Math.max(1, Math.ceil(layout.height))

  const laneHeaders = layout.lanes.map(lane => {
    const participant = graph.participants.find(p => p.id === lane.id)!
    const chip = labelRefKey(lane.label)
    const headerTop = layout.lifelineTop - lane.height
    const figure = lane.actor
      ? drawActorFigure(lane.x, headerTop, lane.height, theme, strokeWidth)
      : svgRect(lane.x - lane.width / 2, headerTop, lane.width, lane.height, { fill: theme.surface2, stroke: theme.border, 'stroke-width': strokeWidth, rx: 3, ry: 3 })
    const label = labelLines(lane.label.split('\n'), lane.x, headerTop + lane.height / 2, fontSize, chip ? theme.accent : theme.fg, !!chip)
    return group([figure, label], {
      'data-blerg-seq-participant': lane.id,
      'data-blerg-chip-key': chip ?? undefined,
      role: chip ? 'button' : 'group',
      tabindex: 0,
      'aria-label': chip ? `Open ${lane.label}` : (participant.actor ? `Actor ${lane.label}` : `Participant ${lane.label}`),
    })
  })

  const lifelines = layout.lanes.map(lane => svgLine(lane.x, layout.lifelineTop, lane.x, layout.lifelineBottom, {
    stroke: theme.border, 'stroke-width': strokeWidth, 'stroke-dasharray': `${Math.round(4 * scale)} ${Math.round(3 * scale)}`,
  }))

  const activationWidth = Math.round(10 * scale)
  const activations = layout.activations.map(a => svgRect(a.x - activationWidth / 2, a.yStart, activationWidth, Math.max(1, a.yEnd - a.yStart), {
    fill: theme.surface2, stroke: theme.border, 'stroke-width': strokeWidth,
  }))

  const messageEls = layout.messages.map(m => {
    const markerId = `blerg-seq-arrow-${m.style}`
    const dashArray = m.style === 'dashed' ? `${Math.round(4 * scale)} ${Math.round(3 * scale)}` : undefined
    const labelY = m.y - Math.round(6 * scale)
    let lineEl: string
    if (m.selfLoop) {
      const loopWidth = Math.round(36 * scale)
      const loopHeight = Math.round(20 * scale)
      const d = `M ${m.fromX},${m.y} C ${m.fromX + loopWidth},${m.y} ${m.fromX + loopWidth},${m.y + loopHeight} ${m.fromX},${m.y + loopHeight}`
      lineEl = `<path d="${d}" fill="none" stroke="${theme.border}" stroke-width="${strokeWidth}" stroke-dasharray="${dashArray ?? ''}" marker-end="url(#${markerId})"/>`
    } else {
      lineEl = svgLine(m.fromX, m.y, m.toX, m.y, {
        stroke: theme.border, 'stroke-width': strokeWidth, 'stroke-dasharray': dashArray, 'marker-end': `url(#${markerId})`,
      })
    }
    const labelEl = m.label ? svgText((m.fromX + m.toX) / 2, labelY, m.label, { 'text-anchor': 'middle', 'font-size': fontSize * 0.9, fill: theme.fg }) : ''
    return group([lineEl, labelEl], {
      'data-blerg-seq-message': `${m.from}->${m.to}`, tabindex: 0, role: 'group',
      'aria-label': `${m.from} to ${m.to}${m.label ? `: ${m.label}` : ''}`,
    })
  })

  const noteEls = layout.notes.map(note => group([
    svgRect(note.x - note.width / 2, note.y, note.width, note.height, { fill: theme.surface2, stroke: theme.border, 'stroke-width': strokeWidth }),
    labelLines(note.label.split('\n'), note.x, note.y + note.height / 2, fontSize * 0.9, theme.fg),
  ], { 'data-blerg-seq-note': note.position, tabindex: 0, role: 'group', 'aria-label': `Note: ${note.label}` }))

  const blockEls = layout.blocks.map(block => {
    const frame = svgRect(block.x, block.yTop, block.width, block.yBottom - block.yTop, {
      fill: 'none', stroke: theme.border, 'stroke-width': strokeWidth, rx: 4,
    })
    const tabWidth = Math.max(Math.round(36 * scale), block.blockKind.length * fontSize * 0.6)
    const tabHeight = Math.round(18 * scale)
    const tab = group([
      svgRect(block.x, block.yTop, tabWidth, tabHeight, { fill: theme.surface2, stroke: theme.border, 'stroke-width': strokeWidth }),
      svgText(block.x + tabWidth / 2, block.yTop + tabHeight * 0.7, block.blockKind, { 'text-anchor': 'middle', 'font-size': fontSize * 0.75, fill: theme.fg, 'font-weight': 'bold' }),
    ])
    const label = block.label ? svgText(block.x + tabWidth + Math.round(6 * scale), block.yTop + tabHeight * 0.7, block.label, { 'font-size': fontSize * 0.8, fill: theme.muted }) : ''
    const elseEls = block.elseY !== null ? group([
      svgLine(block.x, block.elseY, block.x + block.width, block.elseY, { stroke: theme.border, 'stroke-width': strokeWidth, 'stroke-dasharray': `${Math.round(4 * scale)} ${Math.round(3 * scale)}` }),
      block.elseLabel ? svgText(block.x + Math.round(6 * scale), block.elseY + fontSize, `else ${block.elseLabel}`, { 'font-size': fontSize * 0.75, fill: theme.muted }) : '',
    ]) : ''
    return group([frame, tab, label, elseEls], { 'data-blerg-seq-block': block.blockKind, role: 'group', 'aria-label': `${block.blockKind} ${block.label}` })
  })

  const defs = `<defs>${marker('blerg-seq-arrow-solid', theme.border)}${marker('blerg-seq-arrow-dashed', theme.border)}</defs>`
  // Lifelines (and the activation bars drawn over them) first: a block's own tab has a solid
  // fill meant to sit on top of the dashed lifeline passing behind it, not have the lifeline
  // paint back over its own label text.
  const body = defs + lifelines.join('') + activations.join('') + blockEls.join('') + noteEls.join('') + messageEls.join('') + laneHeaders.join('')
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="${layout.minX} 0 ${width} ${height}" font-family="${escapeXml(theme.fontFamily)}">${body}</svg>`
  return { svg, width, height }
}
