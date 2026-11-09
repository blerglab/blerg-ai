// BLG-468 §1/§8: every flowchart-v2 corpus fixture (runner/frontend/src/test/mermaid/cases.json)
// classified once, so a change to the parser's strictness shows up here instead of silently
// drawing something new (or silently stopping drawing something it used to). The expected set is
// hand-reviewed against the grammar table in docs/design/diagram-renderer.md §2 — adding a fixture
// to EXPECTED_OURS is a deliberate scope decision, not something this test infers on its own.
import { describe, expect, it } from 'vitest'
import cases from '../../../../../runner/frontend/src/test/mermaid/cases.json'
import { parseFlowchart } from './parse'

const EXPECTED_OURS = new Set([
  'readme-0-flowchart',
  'flow-direction-TD', 'flow-direction-TB', 'flow-direction-BT', 'flow-direction-LR', 'flow-direction-RL',
  'flow-image-words',
  'flow-unicode',
  'flow-120-nodes',
  'flow-long-labels',
  'flowchart-v2-frontmatter-comments',
  'theme-light', 'theme-dark',
  'recovery-after-invalid',
  // flow-br-entities is NOT here: its second label uses Mermaid's legacy ampersand-less entity
  // shorthand ("#38;", "#quot;") alongside a real <br/>, which label.ts deliberately doesn't
  // decode (design doc §2) — falls back, correctly.
])

describe('parseFlowchart against the real flowchart-v2 corpus', () => {
  const fixtures = (cases as Array<{ id: string; diagram?: string; source: string; refusal?: string; error?: string }>)
    .filter(fixture => fixture.diagram === 'flowchart-v2' && !fixture.refusal && !fixture.error)

  it('has fixtures to check (the corpus did not shrink out from under this test)', () => {
    expect(fixtures.length).toBeGreaterThan(30)
  })

  for (const fixture of fixtures) {
    const expectOurs = EXPECTED_OURS.has(fixture.id)
    it(`${fixture.id}: ${expectOurs ? 'drawn by us' : 'falls back to Mermaid'}`, () => {
      const graph = parseFlowchart(fixture.source)
      if (expectOurs) expect(graph, fixture.id).not.toBeNull()
      else expect(graph, fixture.id).toBeNull()
    })
  }
})
