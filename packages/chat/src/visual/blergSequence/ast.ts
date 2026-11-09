// BLG-468 §1/§2 (sequence): the declared sequence subset's own AST — never Mermaid's. Mirrors
// blergFlow/ast.ts's own framing: one shape for everything this slice draws, nothing a construct
// outside the declared grammar would need — that construct is a reason for parseSequence to
// return null (the failure rule), not a shape this type needs to represent.
export type SeqArrowStyle = 'solid' | 'dashed'

export interface SeqParticipant {
  id: string
  /** Decoded (label.ts) — never raw Mermaid source. Defaults to `id` with no `as Alias`. */
  label: string
  actor: boolean
}

export interface SeqMessage {
  kind: 'message'
  from: string
  to: string
  label: string | null
  style: SeqArrowStyle
  /** `+` on the arrow (design doc §2): activates `to`, as its own `SeqActivate` event
   *  immediately after this message in `events` — kept here too so draw.ts's activation-bar
   *  pass never has to look one event ahead to find it. */
  activate: boolean
  /** `-` on the arrow: deactivates `from`, as its own `SeqDeactivate` event immediately after. */
  deactivate: boolean
}

export interface SeqActivate { kind: 'activate'; id: string }
export interface SeqDeactivate { kind: 'deactivate'; id: string }

export type SeqNotePosition = 'left' | 'right' | 'over'
export interface SeqNote {
  kind: 'note'
  position: SeqNotePosition
  /** One participant for `left of`/`right of`, or `over` on a single participant; two for
   *  `over X,Y` (design doc §2's own form). */
  participants: readonly [string] | readonly [string, string]
  label: string
}

export type SeqBlockKind = 'alt' | 'opt' | 'loop'
export interface SeqBlock {
  kind: 'block'
  blockKind: SeqBlockKind
  label: string
  items: SeqEvent[]
  /** `alt` only (design doc §2: one `else`, not Mermaid's own repeatable form) — absent for
   *  `opt`/`loop`, and absent for an `alt` that happened to have no `else` in its source. */
  elseLabel?: string
  elseItems?: SeqEvent[]
}

export type SeqEvent = SeqMessage | SeqActivate | SeqDeactivate | SeqNote | SeqBlock

export interface SeqGraph {
  participants: SeqParticipant[]
  events: SeqEvent[]
}
