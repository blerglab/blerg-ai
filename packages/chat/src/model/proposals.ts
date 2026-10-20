// proposals.ts: what a proposal card says. A session proposes an action (proposal_request); its
// outcome comes back as proposal_decided (more than one when an unknown outcome is resolved later:
// the newest wins). Pure functions.
import type { AgentEvent, FiledCard, ProposalDecidedPayload, ProposalDetail, ProposalState } from '../types'

/** By proposal_id: the newest decision seen. */
export function proposalStates(events: readonly AgentEvent[]): ReadonlyMap<string, ProposalDecidedPayload> {
  const out = new Map<string, ProposalDecidedPayload>()
  for (const ev of events) {
    if (ev.kind !== 'proposal_decided') continue
    const p = readDecided(ev.payload)
    if (p) out.set(p.proposal_id, p)
  }
  return out
}

const STATES: readonly ProposalState[] = ['pending', 'executing', 'done', 'rejected', 'expired', 'failed', 'unknown']
const text = (v: unknown, max: number) => (typeof v === 'string' ? v.slice(0, max) : '')

/** A filed card from untrusted JSON, or null. */
export function readCard(v: unknown): FiledCard | null {
  if (!v || typeof v !== 'object') return null
  const c = v as Record<string, unknown>
  if (typeof c.id !== 'string' || !c.id || typeof c.board_id !== 'string' || !c.board_id || typeof c.number !== 'number' || !Number.isSafeInteger(c.number) || c.number < 1) return null
  return {
    id: c.id, board_id: c.board_id, number: c.number,
    board_name: text(c.board_name, 200) || undefined, title: text(c.title, 200) || undefined, type: text(c.type, 64) || undefined,
    priority: text(c.priority, 64) || undefined, column: text(c.column, 200) || undefined,
  }
}

export function readDecided(v: unknown): ProposalDecidedPayload | null {
  if (!v || typeof v !== 'object') return null
  const p = v as Record<string, unknown>
  if (typeof p.proposal_id !== 'string' || !p.proposal_id || !STATES.includes(p.state as ProposalState)) return null
  const card = readCard(p.card)
  return {
    proposal_id: p.proposal_id, kind: p.kind === 'board_file' ? 'board_file' : 'mcp', state: p.state as ProposalState,
    ...(p.auto === true ? { auto: true } : {}), ...(typeof p.grant_id === 'string' ? { grant_id: p.grant_id } : {}),
    ...(card ? { card } : {}),
  }
}

/** The proposal API's view (GET /api/proposals/{id}) as a card reads it. */
export function readProposalView(v: unknown): ProposalDetail {
  if (!v || typeof v !== 'object') throw new Error('Invalid proposal.')
  const p = v as Record<string, unknown>
  if (typeof p.id !== 'string' || !STATES.includes(p.state as ProposalState) || typeof p.arguments_raw !== 'string') throw new Error('Invalid proposal.')
  const args = (p.arguments && typeof p.arguments === 'object' ? p.arguments : {}) as Record<string, unknown>
  const board = p.kind === 'board_file'
  const result = (p.result && typeof p.result === 'object' ? p.result : null) as Record<string, unknown> | null
  const body = text(args.body, 4000)
  return {
    id: p.id,
    kind: board ? 'board_file' : p.kind === 'plugin_request' ? 'plugin_request' : 'mcp',
    state: p.state as ProposalState,
    target: board ? text(args.board_name, 200) || text(args.board_id, 200) : text(p.connection_name, 200),
    tool: board ? '' : text(p.tool, 200),
    title: board ? text(args.title, 200) : text(p.agent_summary, 400),
    summary: board ? (body.length > 240 ? `${body.slice(0, 240)}…` : body) : '',
    argumentsRaw: p.arguments_raw,
    approvedByGrant: typeof p.approved_by_grant === 'string' ? p.approved_by_grant : null,
    card: board && p.state === 'done' ? readCard(result) : null,
    error: result && typeof result.error === 'string' ? result.error.slice(0, 400) : '',
  }
}

/** One line for a decided proposal, or '' while it waits. */
export function outcomeLine(kind: 'board_file' | 'mcp', state: ProposalState, auto: boolean): string {
  const by = auto ? ' by a session permission' : ''
  switch (state) {
    case 'done': return kind === 'board_file' ? `Card created${by ? ` · approved${by}` : ''}` : `Approved${by} · it ran`
    case 'rejected': return kind === 'board_file' ? 'Rejected · no card was created' : 'Rejected · nothing was run'
    case 'expired': return kind === 'board_file' ? 'Expired · no card was created' : 'Expired · nothing was run'
    case 'failed': return kind === 'board_file' ? `Failed · no card was created` : `Approved${by} · it failed`
    case 'unknown': return 'Outcome unknown · check the target before deciding on the Proposals page'
    case 'executing': return `Approved${by} · running…`
    default: return ''
  }
}
