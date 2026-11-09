// BLG-471 §8: every gantt corpus fixture (runner/frontend/src/test/mermaid/cases.json)
// classified once, same discipline as blergFlow/corpus.test.ts and blergSequence's own corpus
// coverage — hand-reviewed against the grammar table in docs/design/diagram-renderer.md §1/§2
// and the ticket's own declared subset (dateFormat, axisFormat, title, section, tasks with ids,
// after-dependencies, durations, milestones, crit, active, done). Adding a fixture to
// EXPECTED_OURS is a deliberate scope decision, not something this test infers on its own.
import { describe, expect, it } from 'vitest'
import cases from '../../../../../runner/frontend/src/test/mermaid/cases.json'
import { parseGantt } from './parse'

const EXPECTED_OURS = new Set([
  // readme-2-gantt: no dateFormat directive — Mermaid's own default is YYYY-MM-DD, which this
  // fixture's dates (2014-01-06, …) match; plain comments and frontmatter are supported.
  'readme-2-gantt',
  'gantt-frontmatter-comments',
  // readme-7-gantt (dateFormat X, bare integer "dates") and gantt-features (excludes/todayMarker,
  // both outside the declared subset) are NOT here — both correctly fall back to Mermaid.
])

describe('parseGantt against the real gantt corpus', () => {
  const fixtures = (cases as Array<{ id: string; diagram?: string; source: string; refusal?: string; error?: string }>)
    .filter(fixture => fixture.diagram === 'gantt' && !fixture.refusal && !fixture.error)

  it('has fixtures to check (the corpus did not shrink out from under this test)', () => {
    expect(fixtures.length).toBeGreaterThanOrEqual(4)
  })

  for (const fixture of fixtures) {
    const expectOurs = EXPECTED_OURS.has(fixture.id)
    it(`${fixture.id}: ${expectOurs ? 'drawn by us' : 'falls back to Mermaid'}`, () => {
      const graph = parseGantt(fixture.source)
      if (expectOurs) expect(graph, fixture.id).not.toBeNull()
      else expect(graph, fixture.id).toBeNull()
    })
  }
})
