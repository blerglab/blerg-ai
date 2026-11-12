// BLG-468 §2 (sequence): our own small parser for the declared sequence subset, same discipline
// as blergFlow/parse.ts — `parseSequence` is also the classifier: `null` for anything outside the
// subset, never a throw (an agent's own diagram text is untrusted input, not a program bug). A
// line only ever succeeds by being one of the productions below; everything else (`par`/`and`,
// nesting, `box`, `autonumber`, a lost-message arrow `-x`/`--x`/`-)`/`--)`, a second `else`) fails
// to match and falls the whole diagram back to Mermaid, the same "unknown, not just rejected"
// rule blergFlow/parse.ts's own header explains.
import type { SeqArrowStyle, SeqBlock, SeqBlockKind, SeqEvent, SeqGraph, SeqParticipant } from './ast'
import { decodeLabel } from '../blergFlow/label'

function skipFrontmatter(lines: readonly string[]): number {
  if (!/^\s*---\s*$/.test(lines[0] ?? '')) return 0
  const close = lines.findIndex((line, index) => index > 0 && /^-{3}\s*$/.test(line))
  return close < 0 ? -1 : close + 1
}
const isBlankOrComment = (line: string) => line.trim() === '' || /^\s*%%/.test(line)

const PARTICIPANT = /^(participant|actor)\s+(\S+)(?:\s+as\s+(.+))?$/i
const ACTIVATION = /^(activate|deactivate)\s+(\S+)$/i
const NOTE = /^note\s+(left of|right of|over)\s+([^,:]+?)(?:\s*,\s*([^,:]+?))?\s*:\s*(.*)$/i
const BLOCK_START = /^(alt|opt|loop)\s+(.*)$/i
const ELSE = /^else\s*(.*)$/i
const END = /^end$/i
// `->>`/`-->>`, optionally `+`/`-` right after the arrow (design doc §2's own placement, matching
// Mermaid's own grammar: `+` activates the target, `-` deactivates the source).
const MESSAGE = /^(\S+?)\s*(-->>|->>)([+-]?)\s*(\S+)\s*:\s*(.*)$/

function arrowStyle(token: string): SeqArrowStyle { return token === '-->>' ? 'dashed' : 'solid' }

/** Declares a participant on first mention (explicit or implicit, e.g. a bare id used in a
 *  message) — matches Mermaid's own "first appearance order" rule the differential gate (§8)
 *  diffs against, so an id never declared with `participant`/`actor` still gets drawn. */
function ensureParticipant(participants: Map<string, SeqParticipant>, order: string[], id: string, actor = false): void {
  const existing = participants.get(id)
  if (existing) { if (actor) existing.actor = true; return }
  participants.set(id, { id, label: id, actor })
  order.push(id)
}

interface ParseState { participants: Map<string, SeqParticipant>; order: string[] }

/** Parses one nesting level (design doc §2: `par`/`and` and any nesting of `alt`/`opt`/`loop`
 *  are unsupported — `inBlock` makes a second `alt`/`opt`/`loop` inside the first fail here,
 *  the same "unknown, falls back" path as any other construct outside the grammar). Returns the
 *  index just past the closing `end` for a nested call, or past the last line at top level. */
function parseEvents(lines: readonly string[], start: number, state: ParseState, inBlock: boolean): { events: SeqEvent[]; next: number } | null {
  const events: SeqEvent[] = []
  let i = start
  for (; i < lines.length; i++) {
    const raw = lines[i]
    const line = raw.trim()
    if (isBlankOrComment(line)) continue
    if (inBlock && ELSE.test(line)) return { events, next: i }
    if (inBlock && END.test(line)) return { events, next: i + 1 }
    if (!inBlock && END.test(line)) return null // an `end` with no block open

    const participant = PARTICIPANT.exec(line)
    if (participant) {
      const id = participant[2]
      const label = participant[3] ? decodeLabel(participant[3].trim()) : id
      if (label === null) return null
      ensureParticipant(state.participants, state.order, id, participant[1].toLowerCase() === 'actor')
      state.participants.get(id)!.label = label
      continue
    }

    const activation = ACTIVATION.exec(line)
    if (activation) {
      ensureParticipant(state.participants, state.order, activation[2])
      events.push({ kind: activation[1].toLowerCase() === 'activate' ? 'activate' : 'deactivate', id: activation[2] })
      continue
    }

    const note = NOTE.exec(line)
    if (note) {
      const position = note[1].toLowerCase() === 'left of' ? 'left' : note[1].toLowerCase() === 'right of' ? 'right' : 'over'
      const a = note[2].trim()
      const b = note[3]?.trim()
      if (position !== 'over' && b) return null // "left of"/"right of" take exactly one participant
      ensureParticipant(state.participants, state.order, a)
      if (b) ensureParticipant(state.participants, state.order, b)
      const label = decodeLabel(note[4])
      if (label === null) return null
      events.push({ kind: 'note', position, participants: b ? [a, b] : [a], label })
      continue
    }

    const blockStart = BLOCK_START.exec(line)
    if (blockStart) {
      if (inBlock) return null // one level only (design doc §1/§2)
      const blockKind = blockStart[1].toLowerCase() as SeqBlockKind
      const label = decodeLabel(blockStart[2].trim())
      if (label === null) return null
      const body = parseEvents(lines, i + 1, state, true)
      if (!body) return null
      const block: SeqBlock = { kind: 'block', blockKind, label, items: body.events }
      let next = body.next
      if (blockKind === 'alt' && ELSE.test(lines[next]?.trim() ?? '')) {
        const elseLabel = decodeLabel(ELSE.exec(lines[next].trim())![1].trim())
        if (elseLabel === null) return null
        const elseBody = parseEvents(lines, next + 1, state, true)
        if (!elseBody) return null
        block.elseLabel = elseLabel
        block.elseItems = elseBody.events
        next = elseBody.next
      } else if (ELSE.test(lines[next]?.trim() ?? '')) {
        return null // a second `else`, or `else` on opt/loop — not in the declared grammar
      }
      events.push(block)
      i = next - 1
      continue
    }

    const message = MESSAGE.exec(line)
    if (message) {
      const [, from, arrow, sign, to] = message
      ensureParticipant(state.participants, state.order, from)
      ensureParticipant(state.participants, state.order, to)
      const label = decodeLabel(message[5])
      if (label === null) return null
      events.push({ kind: 'message', from, to, label, style: arrowStyle(arrow), activate: sign === '+', deactivate: sign === '-' })
      if (sign === '+') events.push({ kind: 'activate', id: to })
      if (sign === '-') events.push({ kind: 'deactivate', id: from })
      continue
    }

    return null
  }
  return inBlock ? null : { events, next: i } // a block never closed by `end`
}

/** `null`: not our declared subset (or not a sequence diagram at all) — the caller falls back to
 *  Mermaid, same contract as `parseFlowchart`. */
export function parseSequence(source: string): SeqGraph | null {
  const lines = source.split('\n')
  let i = skipFrontmatter(lines)
  if (i < 0) return null
  while (i < lines.length && isBlankOrComment(lines[i])) i++
  if (!/^\s*sequenceDiagram\s*$/i.test(lines[i] ?? '')) return null
  i++

  const state: ParseState = { participants: new Map(), order: [] }
  const body = parseEvents(lines, i, state, false)
  if (!body || state.order.length === 0) return null
  return { participants: state.order.map(id => state.participants.get(id)!), events: body.events }
}
