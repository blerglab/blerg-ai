// BLG-468 §3 (sequence): "Sequence needs no graph solver — participants in a row, messages as a
// vertical timeline — a small hand-rolled layout pass" (design doc §3's own words). No dagre
// here: lanes are placed left to right in first-appearance order (graph.participants' own order,
// the same order Mermaid's own SequenceDB builds), and every event advances a single vertical
// cursor top to bottom. Proportional to fontSize from the start (the baseline ratio flowchart's
// own layout.ts/draw.ts only adopted after round 3/4 review — carried forward here on day one).
import type { SeqArrowStyle, SeqBlockKind, SeqEvent, SeqGraph, SeqNotePosition } from './ast'
import { measureLabel, measureTextBox } from '../blergFlow/sizing'

export interface SeqLane { id: string; x: number; width: number; height: number; label: string; actor: boolean }
export interface SeqLaidMessage { from: string; to: string; label: string | null; style: SeqArrowStyle; y: number; fromX: number; toX: number; selfLoop: boolean }
export interface SeqLaidNote { position: SeqNotePosition; x: number; width: number; y: number; height: number; label: string }
export interface SeqLaidBlock { blockKind: SeqBlockKind; label: string; x: number; width: number; yTop: number; yBottom: number; elseY: number | null; elseLabel: string | null }
export interface SeqLaidActivation { id: string; x: number; yStart: number; yEnd: number }

export interface SeqLayout {
  width: number
  height: number
  /** The viewBox's own left edge, in the same coordinate space every other field here uses — 0
   *  unless a note overflows past the lanes' own left edge (above). */
  minX: number
  lanes: readonly SeqLane[]
  lifelineTop: number
  lifelineBottom: number
  messages: readonly SeqLaidMessage[]
  notes: readonly SeqLaidNote[]
  blocks: readonly SeqLaidBlock[]
  activations: readonly SeqLaidActivation[]
}

const MIN_LANE_WIDTH = 60
const SELF_LOOP_WIDTH = 40

export function layoutSequence(graph: SeqGraph, fontSize = 14): SeqLayout {
  const scale = fontSize / 14
  const laneGap = Math.round(40 * scale)
  const margin = Math.round(16 * scale)
  const rowHeight = Math.round(32 * scale)
  const notePad = Math.round(8 * scale)
  const blockTopPad = Math.round(22 * scale)
  const blockBottomPad = Math.round(10 * scale)
  const blockSidePad = Math.round(10 * scale)

  const laneWidths = new Map<string, number>()
  const laneHeights: number[] = []
  for (const p of graph.participants) {
    const measured = measureLabel(p.label, fontSize)
    laneWidths.set(p.id, Math.max(MIN_LANE_WIDTH, measured.width))
    laneHeights.push(measured.height)
  }
  const laneX = new Map<string, number>()
  let cursorX = margin
  const lanes: SeqLane[] = graph.participants.map(p => {
    const width = laneWidths.get(p.id)!
    const x = cursorX + width / 2
    laneX.set(p.id, x)
    cursorX += width + laneGap
    return { id: p.id, x, width, height: Math.max(...laneHeights, Math.round(28 * scale)), label: p.label, actor: p.actor }
  })
  cursorX -= laneGap

  const headerHeight = Math.max(...laneHeights, Math.round(28 * scale))
  const lifelineTop = margin + headerHeight

  const messages: SeqLaidMessage[] = []
  const notes: SeqLaidNote[] = []
  const blocks: SeqLaidBlock[] = []
  const activeStack = new Map<string, number[]>()
  const activations: SeqLaidActivation[] = []

  function closeActivation(id: string, yEnd: number): void {
    const stack = activeStack.get(id)
    const yStart = stack?.pop()
    if (typeof yStart === 'number') activations.push({ id, x: laneX.get(id) ?? margin, yStart, yEnd })
  }

  function layoutEvents(events: readonly SeqEvent[], startY: number): number {
    let y = startY
    for (const event of events) {
      if (event.kind === 'message') {
        y += rowHeight
        const fromX = laneX.get(event.from) ?? margin
        const toX = laneX.get(event.to) ?? margin
        messages.push({ from: event.from, to: event.to, label: event.label, style: event.style, y, fromX, toX, selfLoop: event.from === event.to })
        if (event.from === event.to) y += SELF_LOOP_WIDTH * scale * 0.4
      } else if (event.kind === 'activate') {
        const stack = activeStack.get(event.id) ?? []
        stack.push(y)
        activeStack.set(event.id, stack)
      } else if (event.kind === 'deactivate') {
        closeActivation(event.id, y)
      } else if (event.kind === 'note') {
        const text = measureTextBox(event.label, fontSize)
        const height = text.height + notePad * 2
        y += Math.round(10 * scale)
        const xs = event.participants.map(id => laneX.get(id) ?? margin)
        const width = Math.max(text.width + notePad * 2, event.participants.length === 2 ? Math.abs(xs[1] - xs[0]) + MIN_LANE_WIDTH : MIN_LANE_WIDTH)
        const center = event.position === 'left' ? xs[0] - width / 2 - laneGap / 4
          : event.position === 'right' ? xs[0] + width / 2 + laneGap / 4
          : (xs[0] + (xs[1] ?? xs[0])) / 2
        notes.push({ position: event.position, x: center, width, y, height, label: event.label })
        y += height
      } else {
        // event.kind === 'block'
        const yTop = y + blockTopPad
        const touched = touchedLanes(event.items).concat(event.elseItems ? touchedLanes(event.elseItems) : [])
        const xs = touched.map(id => laneX.get(id) ?? margin)
        const minX = Math.min(...(xs.length ? xs : [margin]), ...lanes.map(l => l.x)) - blockSidePad
        const maxX = Math.max(...(xs.length ? xs : [margin]), ...lanes.map(l => l.x)) + blockSidePad
        let innerY = layoutEvents(event.items, yTop)
        let elseY: number | null = null
        if (event.elseItems) {
          elseY = innerY + Math.round(6 * scale)
          innerY = layoutEvents(event.elseItems, elseY + Math.round(14 * scale))
        }
        const yBottom = innerY + blockBottomPad
        blocks.push({ blockKind: event.blockKind, label: event.label, x: minX, width: maxX - minX, yTop: y, yBottom, elseY, elseLabel: event.elseLabel ?? null })
        y = yBottom
      }
    }
    return y
  }

  function touchedLanes(events: readonly SeqEvent[]): string[] {
    const ids = new Set<string>()
    for (const event of events) {
      if (event.kind === 'message') { ids.add(event.from); ids.add(event.to) }
      else if (event.kind === 'note') for (const id of event.participants) ids.add(id)
      else if (event.kind === 'block') { for (const id of touchedLanes(event.items)) ids.add(id); if (event.elseItems) for (const id of touchedLanes(event.elseItems)) ids.add(id) }
    }
    return [...ids]
  }

  let bottom = layoutEvents(graph.events, lifelineTop)
  // Review lesson carried from flowchart round 4: an activation left open at the end of the
  // diagram (no matching `deactivate`) still needs a real span to draw, not a dangling start —
  // closed at the final lifeline position rather than silently dropped.
  for (const [id, stack] of activeStack) for (const yStart of stack) activations.push({ id, x: laneX.get(id) ?? margin, yStart, yEnd: bottom })
  bottom += margin

  // A long "left of"/"right of" note can extend past the lane-only bound below (unlike a block,
  // whose frame is always kept within the lanes' own span, `touchedLanes` above) — left
  // unaccounted for, the diagram's own `width`/viewBox origin would end up narrower than what
  // actually draws, clipping the note at the SVG boundary rather than growing to fit it.
  let minX = 0
  let maxX = cursorX + margin
  for (const note of notes) {
    minX = Math.min(minX, note.x - note.width / 2)
    maxX = Math.max(maxX, note.x + note.width / 2)
  }

  return {
    width: Math.max(1, maxX - minX),
    height: Math.max(1, bottom),
    minX,
    lanes, lifelineTop, lifelineBottom: bottom - margin,
    messages, notes, blocks, activations,
  }
}
