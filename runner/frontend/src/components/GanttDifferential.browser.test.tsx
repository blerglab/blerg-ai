// BLG-471 §8: identity differential — for every `gantt` corpus fixture we classify as ours,
// parse the same source with Mermaid's own real GanttDB inside the sandboxed frame
// (mermaidFrame's test-only 'inspect' protocol) and diff task identity (id, label, section,
// resolved start/end — "bar positions for the supported syntax", the ticket's own words — and
// the done/active/crit/milestone flags) against our own AST. Mirrors
// SequenceDifferential.browser.test.tsx's own shape for the gantt grammar.
import { describe, expect, it } from 'vitest'
import { parseGantt } from '../../../../packages/chat/src/visual/blergGantt/parse'
import { inspectInFrame } from '../../../../packages/chat/src/visual/mermaidFrame'
import cases from '../test/mermaid/cases.json'

const CONFIG = { startOnLoad: false, securityLevel: 'strict' }

// Same set corpus.test.ts (packages/chat/src/visual/blergGantt/) classifies as ours.
const OURS = new Set(['readme-2-gantt', 'gantt-frontmatter-comments'])

// Both fixtures share a body with no `dateFormat` line, which leaves Mermaid's own dateFormat at
// its empty-string default. Its `getEndDate` (ganttDb) parses a literal end date with
// `dayjs(str, dateFormat, true)` (strict mode) — which always fails against an empty format — and
// then falls back to *duration* parsing, not the native-`Date` fallback `getStartDate` uses for
// the same situation; a literal end date (`des1`'s own two-date form, "2014-01-06,2014-01-08")
// isn't a valid duration either, so Mermaid silently resolves `des1` to zero length
// (end === start), and every task that depends on it (`after des1`) inherits the wrong start,
// cascading through the rest of the chain. That's a real asymmetry in Mermaid's own fallback
// chain for a dateFormat-less source, not a documented behavior worth matching — our own parser
// (parse.ts) resolves a literal end date the same way regardless of whether `dateFormat` was
// declared. Both of this corpus's own fixtures hit it, so dates aren't a meaningful ground truth
// for either; identity (ids, section, label, flags) still is, and is still checked below.
const DATES_UNRELIABLE = new Set(['readme-2-gantt', 'gantt-frontmatter-comments'])

const fixtures = (cases as Array<{ id: string; diagram?: string; source: string }>)
  .filter(f => f.diagram === 'gantt' && OURS.has(f.id))

describe('gantt differential: our parser vs Mermaid\'s own GanttDB', () => {
  it('has fixtures to check', () => { expect(fixtures.length).toBeGreaterThan(0) })

  for (const fixture of fixtures) {
    it(`${fixture.id}: task identity and resolved dates match Mermaid's own parse`, async () => {
      const ours = parseGantt(fixture.source)
      expect(ours, fixture.id).not.toBeNull()
      const inspected = await inspectInFrame(fixture.source, CONFIG)
      if (inspected.kind !== 'gantt') throw new Error(`${fixture.id}: expected a gantt inspect result`)

      const ourIds = new Set(ours!.tasks.map(t => t.id))
      const mermaidIds = new Set(inspected.tasks.map(t => t.id))
      expect(ourIds, `${fixture.id}: task id set`).toEqual(mermaidIds)
      expect(ours!.sections, `${fixture.id}: section order`).toEqual(inspected.sections)

      for (const task of ours!.tasks) {
        const theirs = inspected.tasks.find(t => t.id === task.id)
        expect(theirs, `${fixture.id}: task ${task.id} missing from Mermaid's own db`).toBeDefined()
        // Mermaid's own GanttDB keeps the task description exactly as written, including the
        // padding some authors use to visually align the `:` column (readme-2-gantt's own
        // fixture) — our parser trims it (label.ts/parse.ts's own discipline, matching every
        // other label in this renderer), so trailing/leading whitespace is normalized on both
        // sides here rather than treated as a meaningful content difference.
        expect(task.label, `${fixture.id}: task ${task.id} label`).toBe(theirs!.text.trim())
        expect(task.section, `${fixture.id}: task ${task.id} section`).toBe(theirs!.section)
        // "Bar positions for the supported syntax" (the ticket's own words): the resolved
        // start/end — the two numbers layout.ts turns directly into a bar's x/width — not just
        // the raw source tokens each parser started from. Skipped for a fixture in
        // DATES_UNRELIABLE (above) — Mermaid's own resolution there isn't a meaningful ground
        // truth to diff against.
        if (!DATES_UNRELIABLE.has(fixture.id)) {
          expect(task.start.toISOString(), `${fixture.id}: task ${task.id} start`).toBe(theirs!.start)
          expect(task.end.toISOString(), `${fixture.id}: task ${task.id} end`).toBe(theirs!.end)
        }
        expect(task.milestone, `${fixture.id}: task ${task.id} milestone`).toBe(theirs!.milestone)
        expect(task.crit, `${fixture.id}: task ${task.id} crit`).toBe(theirs!.crit)
        expect(task.active, `${fixture.id}: task ${task.id} active`).toBe(theirs!.active)
        expect(task.done, `${fixture.id}: task ${task.id} done`).toBe(theirs!.done)
      }
    }, 30_000)
  }
})
