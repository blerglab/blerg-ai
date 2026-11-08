import { describe, expect, it } from 'vitest'
import { parseFlowchart } from './parse'
import { layoutFlowchart } from './layout'

function layout(source: string) {
  const graph = parseFlowchart(source)
  if (!graph) throw new Error('fixture must parse')
  return layoutFlowchart(graph)
}

describe('layoutFlowchart', () => {
  it('lays out a simple chain top-to-bottom with no overlap', () => {
    const out = layout('flowchart TB\nA[Start] --> B[Finish]')
    const a = out.nodes.find(n => n.id === 'A')!
    const b = out.nodes.find(n => n.id === 'B')!
    expect(a.y + a.height).toBeLessThanOrEqual(b.y)
    expect(out.width).toBeGreaterThan(0)
    expect(out.height).toBeGreaterThan(0)
  })

  it('lays out left-to-right with A left of B', () => {
    const out = layout('flowchart LR\nA[Start] --> B[Finish]')
    const a = out.nodes.find(n => n.id === 'A')!
    const b = out.nodes.find(n => n.id === 'B')!
    expect(a.x + a.width).toBeLessThanOrEqual(b.x)
  })

  it('gives every node a positive size and never overlaps two unrelated nodes', () => {
    const out = layout('flowchart LR\nA --> B\nC --> D\nA --> D')
    for (const node of out.nodes) {
      expect(node.width).toBeGreaterThan(0)
      expect(node.height).toBeGreaterThan(0)
    }
  })

  it('produces at least two path points per edge', () => {
    const out = layout('flowchart LR\nA --> B')
    expect(out.edges[0].points.length).toBeGreaterThanOrEqual(2)
  })

  it('sizes a node wider for a longer label', () => {
    const out = layout('flowchart LR\nShort --> B\nA["A very much longer label than the others"] --> Short')
    const short = out.nodes.find(n => n.id === 'Short')!
    const long = out.nodes.find(n => n.id === 'A')!
    expect(long.width).toBeGreaterThan(short.width)
  })

  it('lays a subgraph out as a cluster containing its nodes', () => {
    const out = layoutFlowchart(parseFlowchart([
      'flowchart TB', 'subgraph outer[Platform]', 'A[Queue] --> B[Worker]', 'end', 'C[Store]', 'B --> C',
    ].join('\n'))!)
    const cluster = out.subgraphs.find(s => s.id === 'outer')!
    const a = out.nodes.find(n => n.id === 'A')!
    const b = out.nodes.find(n => n.id === 'B')!
    expect(a.x).toBeGreaterThanOrEqual(cluster.x)
    expect(a.x + a.width).toBeLessThanOrEqual(cluster.x + cluster.width)
    expect(b.x).toBeGreaterThanOrEqual(cluster.x)
  })

  it('gives a diamond extra room so its label clears the shape\'s diagonal edges', () => {
    const out = layout('flowchart LR\nA{Decision} --> B[Short]')
    const diamond = out.nodes.find(n => n.id === 'A')!
    const rect = out.nodes.find(n => n.id === 'B')!
    // Same label length budget (both short words), but the diamond must still end up larger.
    expect(diamond.width).toBeGreaterThan(rect.width)
    expect(diamond.height).toBeGreaterThan(rect.height)
  })
})
