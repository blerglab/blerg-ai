// BLG-468 §1/§8 (sequence): every `sequence` corpus fixture (runner/frontend/src/test/mermaid/
// cases.json) classified once, mirroring blergFlow/corpus.test.ts's own discipline — a change to
// the parser's strictness shows up here, not silently.
import { describe, expect, it } from 'vitest'
import cases from '../../../../../runner/frontend/src/test/mermaid/cases.json'
import { parseSequence } from './parse'

// readme-1-sequenceDiagram and sequence-frontmatter-comments: plain participants, a self-message
// inside one level of loop, a note — the declared subset exactly.
// sequence-many-messages: plain participants and many messages, no blocks at all.
// sequence-unicode: unicode ids/aliases/labels, a note over one participant.
// NOT here: sequence-fragments (autonumber, and a loop nested inside an alt's else branch —
// nesting), sequence-parallel (par/and, critical/option), sequence-boxes (box grouping).
const EXPECTED_OURS = new Set([
  'readme-1-sequenceDiagram',
  'sequence-many-messages',
  'sequence-frontmatter-comments',
  'sequence-unicode',
])

describe('parseSequence against the real sequence corpus', () => {
  const fixtures = (cases as Array<{ id: string; diagram?: string; source: string; refusal?: string; error?: string }>)
    .filter(fixture => fixture.diagram === 'sequence' && !fixture.refusal && !fixture.error)

  it('has fixtures to check (the corpus did not shrink out from under this test)', () => {
    expect(fixtures.length).toBeGreaterThan(5)
  })

  for (const fixture of fixtures) {
    const expectOurs = EXPECTED_OURS.has(fixture.id)
    it(`${fixture.id}: ${expectOurs ? 'drawn by us' : 'falls back to Mermaid'}`, () => {
      const graph = parseSequence(fixture.source)
      if (expectOurs) expect(graph, fixture.id).not.toBeNull()
      else expect(graph, fixture.id).toBeNull()
    })
  }
})
