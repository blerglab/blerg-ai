// proposals.ts: what a proposal card says. A session proposes an action (proposal_request); its
// outcome comes back as proposal_decided (more than one when an unknown outcome is resolved later:
// the newest wins). Pure functions.
import type { AgentEvent, CreatedBoard, FiledCard, ProposalDecidedPayload, ProposalDetail, ProposalKind, ProposalState } from '../types'

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

const ACCESS: readonly CreatedBoard['access'][] = ['propose', 'read', 'file', 'full']
/** The kind of an untrusted payload: an unknown kind reads as an MCP call. */
const kindOf = (v: unknown): ProposalKind => (v === 'board_file' || v === 'board_create' ? v : 'mcp')

/** A created board from untrusted JSON, or null. */
export function readCreatedBoard(v: unknown): CreatedBoard | null {
  if (!v || typeof v !== 'object') return null
  const b = v as Record<string, unknown>
  if (typeof b.board_id !== 'string' || !b.board_id || !ACCESS.includes(b.access as CreatedBoard['access'])) return null
  return { board_id: b.board_id.slice(0, 200), board_name: text(b.board_name, 200), access: b.access as CreatedBoard['access'] }
}

export function readDecided(v: unknown): ProposalDecidedPayload | null {
  if (!v || typeof v !== 'object') return null
  const p = v as Record<string, unknown>
  if (typeof p.proposal_id !== 'string' || !p.proposal_id || !STATES.includes(p.state as ProposalState)) return null
  const card = readCard(p.card)
  const board = readCreatedBoard(p.board)
  return {
    proposal_id: p.proposal_id, kind: kindOf(p.kind), state: p.state as ProposalState,
    ...(p.auto === true ? { auto: true } : {}), ...(typeof p.grant_id === 'string' ? { grant_id: p.grant_id } : {}),
    ...(card ? { card } : {}), ...(board ? { board } : {}),
  }
}

/** The proposal API's view (GET /api/proposals/{id}) as a card reads it. */
export function readProposalView(v: unknown): ProposalDetail {
  if (!v || typeof v !== 'object') throw new Error('Invalid proposal.')
  const p = v as Record<string, unknown>
  if (typeof p.id !== 'string' || !STATES.includes(p.state as ProposalState) || typeof p.arguments_raw !== 'string') throw new Error('Invalid proposal.')
  const args = (p.arguments && typeof p.arguments === 'object' ? p.arguments : {}) as Record<string, unknown>
  const result0 = (p.result && typeof p.result === 'object' ? p.result : null) as Record<string, unknown> | null
  if (p.kind === 'board_create') {
    const repos = Array.isArray(args.repos) ? args.repos.filter((r): r is string => typeof r === 'string').slice(0, 20) : []
    const access = ACCESS.includes(args.access as CreatedBoard['access']) ? args.access as string : 'full'
    const shape = text(args.template, 64) ? `template ${text(args.template, 64)}` : Array.isArray(args.columns) && args.columns.length
      ? `columns ${args.columns.filter((c): c is string => typeof c === 'string').slice(0, 20).join(' · ')}` : 'default columns'
    return {
      id: p.id, kind: 'board_create', state: p.state as ProposalState,
      target: text(args.name, 200), tool: '', title: text(args.name, 200),
      summary: `${repos.length ? `Repositories: ${repos.join(', ')}` : 'No repositories'} · ${shape} · asks for ${access === 'file' ? 'file without asking' : access} access`,
      argumentsRaw: p.arguments_raw, approvedByGrant: null, card: null,
      board: p.state === 'done' ? readCreatedBoard(result0) : null,
      error: result0 && typeof result0.error === 'string' ? result0.error.slice(0, 400) : '',
    }
  }
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

const ACCESS_WORDS: Record<CreatedBoard['access'], string> = { propose: 'no access', read: 'Read access', file: 'File without asking', full: 'Full access' }

/** One line for a decided proposal, or '' while it waits. */
export function outcomeLine(kind: ProposalKind, state: ProposalState, auto: boolean, board?: CreatedBoard | null): string {
  const by = auto ? ' by a session permission' : ''
  if (kind === 'board_create') {
    switch (state) {
      case 'done': return board ? `Board created · this session has ${ACCESS_WORDS[board.access]}` : 'Board created'
      case 'rejected': return 'Rejected · no board was created'
      case 'expired': return 'Expired · no board was created'
      case 'failed': return 'Failed · no board was created'
      case 'unknown': return 'Outcome unknown · check the Board app before deciding on the Proposals page'
      case 'executing': return 'Approved · creating the board…'
      default: return ''
    }
  }
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
