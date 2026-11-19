import { describe, expect, it } from 'vitest'
import { parseSequence } from './parse'
import { layoutSequence } from './layout'

function layout(source: string) {
  const graph = parseSequence(source)
  if (!graph) throw new Error('fixture must parse')
  return layoutSequence(graph)
}

describe('layoutSequence', () => {
  it('lays out lanes left to right in first-appearance order', () => {
    const out = layout('sequenceDiagram\nA->>B: hi\nB->>C: hi')
    const [a, b, c] = out.lanes
    expect(a.id).toBe('A')
    expect(b.id).toBe('B')
    expect(c.id).toBe('C')
    expect(a.x).toBeLessThan(b.x)
    expect(b.x).toBeLessThan(c.x)
  })

  it('sizes a lane wider for a longer label', () => {
    const out = layout('sequenceDiagram\nparticipant A as A Very Much Longer Alias\nparticipant B\nA->>B: hi')
    const [a, b] = out.lanes
    expect(a.width).toBeGreaterThan(b.width)
  })

  it('every message advances strictly below the one before it, top to bottom', () => {
    const out = layout('sequenceDiagram\nA->>B: m1\nB->>A: m2\nA->>B: m3')
    expect(out.messages[1].y).toBeGreaterThan(out.messages[0].y)
    expect(out.messages[2].y).toBeGreaterThan(out.messages[1].y)
  })

  it('a self-message keeps the same x on both ends and is flagged', () => {
    const out = layout('sequenceDiagram\nA->>A: think')
    expect(out.messages[0].selfLoop).toBe(true)
    expect(out.messages[0].fromX).toBe(out.messages[0].toX)
  })

  it('pairs an activate with its matching deactivate into one span', () => {
    const out = layout('sequenceDiagram\nactivate A\nA->>B: hi\ndeactivate A')
    expect(out.activations).toHaveLength(1)
    expect(out.activations[0].id).toBe('A')
    expect(out.activations[0].yEnd).toBeGreaterThan(out.activations[0].yStart)
  })

  it('the "+"/"-" shorthand produces the same activation span as explicit statements', () => {
    const out = layout('sequenceDiagram\nAlice->>+Bob: Hello\nBob-->>-Alice: Great')
    expect(out.activations).toHaveLength(1)
    expect(out.activations[0].id).toBe('Bob')
  })

  it('an activation never closed is still drawn, closed at the final lifeline position', () => {
    const out = layout('sequenceDiagram\nactivate A\nA->>B: hi')
    expect(out.activations).toHaveLength(1)
    expect(out.activations[0].yEnd).toBe(out.lifelineBottom)
  })

  it('a wide "right of" note grows the diagram width rather than clipping', () => {
    const lanesOnly = layout('sequenceDiagram\nparticipant A\nparticipant B\nA->>B: hi')
    const out = layout('sequenceDiagram\nparticipant A\nparticipant B\nNote right of A: A very much longer note than either participant label')
    const note = out.notes[0]
    expect(out.width).toBeGreaterThan(lanesOnly.width)
    expect(note.x + note.width / 2).toBeLessThanOrEqual(out.minX + out.width)
  })

  it('a wide "left of" note extends the viewBox origin left instead of clipping or going negative unaccounted', () => {
    const out = layout('sequenceDiagram\nparticipant A\nparticipant B\nNote left of A: A very much longer note than either participant label')
    expect(out.minX).toBeLessThan(0)
    const note = out.notes[0]
    expect(note.x - note.width / 2).toBeGreaterThanOrEqual(out.minX)
  })

  it('a note over two participants spans between them', () => {
    const out = layout('sequenceDiagram\nA->>B: hi\nNote over A,B: shared')
    const [a, b] = out.lanes
    expect(out.notes[0].x).toBeGreaterThan(a.x)
    expect(out.notes[0].x).toBeLessThan(b.x)
  })

  it('a block frame spans at least the lanes its own messages touch', () => {
    const out = layout('sequenceDiagram\nparticipant A\nparticipant B\nparticipant C\nalt one\nA->>B: hi\nend')
    const [a, b] = out.lanes
    const block = out.blocks[0]
    expect(block.x).toBeLessThanOrEqual(a.x)
    expect(block.x + block.width).toBeGreaterThanOrEqual(b.x)
    expect(block.yBottom).toBeGreaterThan(block.yTop)
  })

  it('an alt with an else records the divider position between the two branches', () => {
    const out = layout('sequenceDiagram\nalt one\nA->>B: hi\nelse two\nA->>B: hi\nend')
    const block = out.blocks[0]
    expect(block.elseY).not.toBeNull()
    expect(block.elseY!).toBeGreaterThan(block.yTop)
    expect(block.elseY!).toBeLessThan(block.yBottom)
  })

  it('gives every lane a positive width and height', () => {
    const out = layout('sequenceDiagram\nA->>B: hi')
    for (const lane of out.lanes) {
      expect(lane.width).toBeGreaterThan(0)
      expect(lane.height).toBeGreaterThan(0)
    }
  })

  it('scales lane spacing and row height up with a bigger fontSize', () => {
    // Short single-letter ids/labels sit at MIN_LANE_WIDTH's own floor, the same sub-linear-growth
    // shape flowchart's own sizing.ts has for a short label (round 3's own finding) — width still
    // grows, just not linearly; row height (no such floor) does.
    const small = layoutSequence(parseSequence('sequenceDiagram\nA->>B: hi\nB->>A: hi')!, 14)
    const big = layoutSequence(parseSequence('sequenceDiagram\nA->>B: hi\nB->>A: hi')!, 28)
    expect(big.width).toBeGreaterThan(small.width)
    expect(big.height).toBeGreaterThan(small.height * 1.5)
  })
})
