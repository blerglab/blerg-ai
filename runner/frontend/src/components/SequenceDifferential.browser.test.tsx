// BLG-468 §8 (sequence): identity differential — for every `sequence` corpus fixture we classify
// as ours, parse the same source with Mermaid's own real SequenceDB inside the sandboxed frame
// (mermaidFrame's test-only 'inspect' protocol) and diff participant/message identity against our
// own AST. Mirrors FlowchartDifferential.browser.test.tsx's own shape for the sequence grammar.
import { describe, expect, it } from 'vitest'
import { parseSequence } from '../../../../packages/chat/src/visual/blergSequence/parse'
import type { SeqEvent, SeqMessage } from '../../../../packages/chat/src/visual/blergSequence/ast'
import { inspectInFrame } from '../../../../packages/chat/src/visual/mermaidFrame'
import cases from '../test/mermaid/cases.json'

const CONFIG = { startOnLoad: false, securityLevel: 'strict' }

// Same set corpus.test.ts (packages/chat/src/visual/blergSequence/) classifies as ours.
const OURS = new Set(['readme-1-sequenceDiagram', 'sequence-many-messages', 'sequence-frontmatter-comments', 'sequence-unicode'])

const fixtures = (cases as Array<{ id: string; diagram?: string; source: string }>)
  .filter(f => f.diagram === 'sequence' && OURS.has(f.id))

// Flattens one level of alt/opt/loop (and an alt's else branch) into the same flat, in-source-
// order message list Mermaid's own `getMessages()` produces — our AST keeps blocks as a tree,
// Mermaid's db never does, so comparing identity means walking ours down to the same shape.
function flattenMessages(events: readonly SeqEvent[]): SeqMessage[] {
  const out: SeqMessage[] = []
  for (const event of events) {
    if (event.kind === 'message') out.push(event)
    else if (event.kind === 'block') {
      out.push(...flattenMessages(event.items))
      if (event.elseItems) out.push(...flattenMessages(event.elseItems))
    }
  }
  return out
}

describe('sequence differential: our parser vs Mermaid\'s own SequenceDB', () => {
  it('has fixtures to check', () => { expect(fixtures.length).toBeGreaterThan(2) })

  for (const fixture of fixtures) {
    it(`${fixture.id}: participant/message identity matches Mermaid's own parse`, async () => {
      const ours = parseSequence(fixture.source)
      expect(ours, fixture.id).not.toBeNull()
      const inspected = await inspectInFrame(fixture.source, CONFIG)
      if (inspected.kind !== 'sequence') throw new Error(`${fixture.id}: expected a sequence inspect result`)

      const ourIds = new Set(ours!.participants.map(p => p.id))
      const mermaidIds = new Set(inspected.actors.map(a => a.id))
      expect(ourIds, `${fixture.id}: participant id set`).toEqual(mermaidIds)

      for (const participant of ours!.participants) {
        const actor = inspected.actors.find(a => a.id === participant.id)
        expect(actor, `${fixture.id}: participant ${participant.id} missing from Mermaid's own db`).toBeDefined()
        expect(participant.label, `${fixture.id}: participant ${participant.id} label`).toBe(actor!.text)
      }

      // Real sync/async messages only — Mermaid's own flat `messages` array also carries
      // NOTE/ACTIVE_START/ACTIVE_END/LOOP_*/ALT_*/OPT_* entries for the same source, which have
      // no (from, to, style) triple of their own to diff against.
      const realTypes = new Set([inspected.lineType.SOLID, inspected.lineType.DOTTED])
      const mermaidMessages = inspected.messages.filter(m => realTypes.has(m.type))
      const ourMessages = flattenMessages(ours!.events)
      expect(ourMessages, `${fixture.id}: message count`).toHaveLength(mermaidMessages.length)
      for (let i = 0; i < ourMessages.length; i++) {
        const ours_ = ourMessages[i]
        const mermaid = mermaidMessages[i]
        expect([ours_.from, ours_.to], `${fixture.id}: message ${i} endpoints`).toEqual([mermaid.from, mermaid.to])
        expect(ours_.label ?? '', `${fixture.id}: message ${i} text`).toBe(mermaid.text ?? '')
        expect(ours_.style, `${fixture.id}: message ${i} style`).toBe(mermaid.type === inspected.lineType.DOTTED ? 'dashed' : 'solid')
      }
    }, 30_000)
  }
})
