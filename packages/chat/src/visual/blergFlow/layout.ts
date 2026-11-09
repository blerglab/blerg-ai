// BLG-468 §3: @dagrejs/dagre 3.1.3 (MIT) — org-maintained, no lodash-es, no d3; 17.3 KB gzip
// measured from its published tarball (docs/design/diagram-renderer.md §3). Importing only
// `Graph` and `layout`, not the package's default export, so nothing beyond those two symbols'
// own transitive graph (just @dagrejs/graphlib) is retained. This module is reached only through
// render.ts's lazy `await import('./blergFlow/render')` on a successful parse — never loaded for
// a diagram that falls back to Mermaid.
import { Graph, layout as dagreLayout, type EdgeLabel, type GraphLabel, type NodeLabel, type Point } from '@dagrejs/dagre'
import type { FlowDirection, FlowGraph } from './ast'
import { measureLabel } from './sizing'

export interface LaidOutNode { id: string; x: number; y: number; width: number; height: number }
export interface LaidOutEdge { from: string; to: string; points: ReadonlyArray<{ x: number; y: number }>; labelX: number | null; labelY: number | null }
export interface LaidOutSubgraph { id: string; x: number; y: number; width: number; height: number }
export interface FlowLayout {
  width: number
  height: number
  nodes: ReadonlyArray<LaidOutNode>
  edges: ReadonlyArray<LaidOutEdge>
  subgraphs: ReadonlyArray<LaidOutSubgraph>
}

// Diamond and circle need extra room around the label so text doesn't reach past the shape's
// own diagonal/curved edge before the shape's own bounding box does (draw.ts draws each shape to
// fill exactly the box laid out here).
function shapeSize(width: number, height: number, shape: FlowGraph['nodes'][number]['shape']): { width: number; height: number } {
  if (shape === 'diamond') return { width: Math.ceil(width * 1.6), height: Math.ceil(height * 1.6) }
  if (shape === 'circle') { const side = Math.ceil(Math.max(width, height) * 1.3); return { width: side, height: side } }
  if (shape === 'stadium' || shape === 'round') return { width: Math.ceil(width * 1.1), height }
  return { width, height }
}

const RANKDIR: Record<FlowDirection, 'TB' | 'BT' | 'LR' | 'RL'> = { TB: 'TB', BT: 'BT', LR: 'LR', RL: 'RL' }

/** Lays the parsed graph out with dagre, including one level of subgraphs as dagre's own
 *  compound-graph clusters (`setParent`) — the same mechanism Mermaid's own flowchart renderer
 *  uses, so our differential corpus (§8) is diffing against a layout algorithm proven on the same
 *  input shape, not just the same library. Pure and synchronous: dagre does no DOM/canvas work,
 *  so this is fully testable without a browser (label sizing is a heuristic, sizing.ts). */
export function layoutFlowchart(graph: FlowGraph, fontSize = 14): FlowLayout {
  // Review round 3 (§1): proportional to fontSize (14px's own existing values as the baseline
  // ratio), not fixed pixel constants — render.ts's own fill mode re-lays-out at a scaled
  // fontSize to grow the whole diagram, and rank/node spacing fixed in absolute pixels would
  // otherwise dominate the result at a smaller scale (a 4-node chain barely shrinking at all,
  // measured) and barely matter at a larger one (nodes huge, gaps still tiny).
  const spacingScale = fontSize / 14
  const g = new Graph<GraphLabel, NodeLabel, EdgeLabel>({ compound: true, multigraph: false, directed: true })
  g.setGraph({
    rankdir: RANKDIR[graph.direction],
    nodesep: Math.round(32 * spacingScale), ranksep: Math.round(48 * spacingScale),
    marginx: Math.round(8 * spacingScale), marginy: Math.round(8 * spacingScale),
  })
  g.setDefaultEdgeLabel(() => ({}))

  for (const subgraph of graph.subgraphs) g.setNode(subgraph.id, { width: 0, height: 0 })
  for (const node of graph.nodes) {
    const measured = measureLabel(node.label, fontSize)
    const size = shapeSize(measured.width, measured.height, node.shape)
    g.setNode(node.id, size)
    if (node.subgraph) g.setParent(node.id, node.subgraph)
  }
  for (const edge of graph.edges) {
    const label = edge.label ? measureLabel(edge.label, fontSize - 1) : null
    g.setEdge(edge.from, edge.to, label ? { width: label.width, height: label.height } : {})
  }

  dagreLayout(g)

  const nodes: LaidOutNode[] = graph.nodes.map(node => {
    const n = g.node(node.id)
    return { id: node.id, x: (n.x ?? 0) - n.width / 2, y: (n.y ?? 0) - n.height / 2, width: n.width, height: n.height }
  })
  const subgraphs: LaidOutSubgraph[] = graph.subgraphs.map(subgraph => {
    const n = g.node(subgraph.id)
    return { id: subgraph.id, x: (n.x ?? 0) - n.width / 2, y: (n.y ?? 0) - n.height / 2, width: n.width, height: n.height }
  })
  const edges: LaidOutEdge[] = graph.edges.map(edge => {
    const e = g.edge(edge.from, edge.to)
    return {
      from: edge.from, to: edge.to,
      points: (e.points ?? []).map((point: Point) => ({ x: point.x, y: point.y })),
      labelX: edge.label && typeof e.x === 'number' ? e.x : null,
      labelY: edge.label && typeof e.y === 'number' ? e.y : null,
    }
  })
  const graphLabel = g.graph()
  return { width: graphLabel.width ?? 0, height: graphLabel.height ?? 0, nodes, edges, subgraphs }
}
