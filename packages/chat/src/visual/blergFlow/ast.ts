// BLG-468 §1/§2: the declared flowchart subset's own AST — never Mermaid's. One node shape set,
// one arrow set, one level of subgraphs. Anything else is a reason for parseFlowchart to return
// null (the failure rule), not a shape this type needs to represent.
export type FlowDirection = 'TB' | 'LR' | 'RL' | 'BT'
export type FlowShape = 'rect' | 'round' | 'stadium' | 'circle' | 'subroutine' | 'diamond'
export type ArrowStyle = 'solid' | 'line' | 'dotted' | 'thick'

export interface FlowNode {
  id: string
  /** Decoded (label.ts) — never raw Mermaid source. */
  label: string
  shape: FlowShape
  /** The one subgraph it was first mentioned inside, if any. */
  subgraph?: string
}

export interface FlowEdge {
  from: string
  to: string
  label: string | null
  style: ArrowStyle
}

export interface FlowSubgraph {
  id: string
  label: string
}

export interface FlowGraph {
  direction: FlowDirection
  nodes: FlowNode[]
  edges: FlowEdge[]
  subgraphs: FlowSubgraph[]
}
