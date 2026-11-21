// BLG-468 §3: node width/height from label text, sized before dagre lays the graph out. A
// heuristic (average glyph width for a generic sans-serif face), not real text metrics — real
// metrics need a live canvas/SVG context, and the layout algorithm (layer 1/2 testable, no DOM)
// should not depend on one. draw.ts draws at exactly these sizes, so what's measured here is
// always what's drawn; this trades a little whitespace around short/wide-glyph labels for a
// layout that is deterministic and testable without a browser.
const AVG_CHAR_WIDTH_FACTOR = 0.58
const LINE_HEIGHT_FACTOR = 1.4
const PAD_X = 16
const PAD_Y = 10
const MIN_WIDTH = 40
const MIN_HEIGHT = 28

export interface MeasuredLabel { width: number; height: number; lines: readonly string[] }

export function measureLabel(label: string, fontSize: number): MeasuredLabel {
  const lines = label.split('\n')
  const longest = Math.max(...lines.map(line => line.length), 1)
  const width = Math.max(MIN_WIDTH, Math.ceil(longest * fontSize * AVG_CHAR_WIDTH_FACTOR) + PAD_X * 2)
  const height = Math.max(MIN_HEIGHT, Math.ceil(lines.length * fontSize * LINE_HEIGHT_FACTOR) + PAD_Y * 2)
  return { width, height, lines }
}

/** The same heuristic as `measureLabel`, but the bare text box — no node-oriented minimum size or
 *  16px/10px padding. Review round 2 (§2): an edge label's background only needs to cover its own
 *  text, padded by a few px (draw.ts's own small margin) — `measureLabel`'s generous node padding
 *  made that background wide enough to visually cut a back edge's curve into two stray pieces. */
export function measureTextBox(label: string, fontSize: number): { width: number; height: number; lines: readonly string[] } {
  const lines = label.split('\n')
  const longest = Math.max(...lines.map(line => line.length), 1)
  return {
    width: Math.ceil(longest * fontSize * AVG_CHAR_WIDTH_FACTOR),
    height: Math.ceil(lines.length * fontSize * LINE_HEIGHT_FACTOR),
    lines,
  }
}
