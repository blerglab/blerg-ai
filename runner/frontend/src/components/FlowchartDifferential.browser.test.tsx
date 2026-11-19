// BLG-468 §8: identity differential — for every flowchart-v2 corpus fixture we classify as ours,
// parse the same source with Mermaid's own real parser inside the sandboxed frame (mermaidFrame's
// test-only 'inspect' protocol) and diff node/edge/direction identity against our own AST. Proves
// our parser agrees with Mermaid's own understanding of the source, not just our corpus's
// expected output.
import { describe, expect, it } from 'vitest'
import { parseFlowchart } from '../../../../packages/chat/src/visual/blergFlow/parse'
import { inspectInFrame } from '../../../../packages/chat/src/visual/mermaidFrame'
import cases from '../test/mermaid/cases.json'

const CONFIG = { startOnLoad: false, securityLevel: 'strict', htmlLabels: false, flowchart: { htmlLabels: false } }

// Same set corpus.test.ts (packages/chat/src/visual/blergFlow/) classifies as ours.
const OURS = new Set([
  'readme-0-flowchart',
  'flow-direction-TD', 'flow-direction-TB', 'flow-direction-BT', 'flow-direction-LR', 'flow-direction-RL',
  'flow-image-words', 'flow-unicode', 'flow-120-nodes', 'flow-long-labels',
  'flowchart-v2-frontmatter-comments', 'theme-light', 'theme-dark', 'recovery-after-invalid',
])

const fixtures = (cases as Array<{ id: string; diagram?: string; source: string }>)
  .filter(f => f.diagram === 'flowchart-v2' && OURS.has(f.id))

describe('flowchart differential: our parser vs Mermaid\'s own flowDb', () => {
  it('has fixtures to check', () => { expect(fixtures.length).toBeGreaterThan(5) })

  for (const fixture of fixtures) {
    it(`${fixture.id}: node/edge identity matches Mermaid's own parse`, async () => {
      const ours = parseFlowchart(fixture.source)
      expect(ours, fixture.id).not.toBeNull()
      const mermaid = await inspectInFrame(fixture.source, CONFIG)
      if (mermaid.kind !== 'flowchart') throw new Error(`${fixture.id}: expected a flowchart inspect result`)

      const ourIds = new Set(ours!.nodes.map(n => n.id))
      const mermaidIds = new Set(mermaid.vertices.map(v => v.id))
      expect(ourIds, `${fixture.id}: node id set`).toEqual(mermaidIds)

      for (const node of ours!.nodes) {
        const v = mermaid.vertices.find(x => x.id === node.id)
        expect(v, `${fixture.id}: node ${node.id} missing from Mermaid's own db`).toBeDefined()
        expect(node.label, `${fixture.id}: node ${node.id} label`).toBe(v!.text)
      }

      const ourEdges = new Set(ours!.edges.map(e => `${e.from}->${e.to}`))
      const mermaidEdges = new Set(mermaid.edges.map(e => `${e.start}->${e.end}`))
      expect(ourEdges, `${fixture.id}: edge triples (endpoints)`).toEqual(mermaidEdges)

      for (const edge of ours!.edges) {
        const m = mermaid.edges.find(e => e.start === edge.from && e.end === edge.to)
        expect(edge.label ?? '', `${fixture.id}: edge ${edge.from}->${edge.to} label`).toBe(m!.text ?? '')
      }

      // Mermaid's own direction string is TD/TB/LR/RL/BT; TD normalizes to TB the same way our
      // parser does (parse.ts's normalizeDirection).
      const mermaidDirection = mermaid.direction === 'TD' ? 'TB' : mermaid.direction
      expect(ours!.direction, `${fixture.id}: direction`).toBe(mermaidDirection)

      for (const subgraph of ours!.subgraphs) {
        const s = mermaid.subGraphs.find(x => x.id === subgraph.id)
        expect(s, `${fixture.id}: subgraph ${subgraph.id} missing from Mermaid's own db`).toBeDefined()
        const ourMembers = new Set(ours!.nodes.filter(n => n.subgraph === subgraph.id).map(n => n.id))
        expect(ourMembers, `${fixture.id}: subgraph ${subgraph.id} membership`).toEqual(new Set(s!.nodes))
      }
    }, 30_000)
  }
})
