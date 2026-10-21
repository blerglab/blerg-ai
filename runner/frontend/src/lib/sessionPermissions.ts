// What the session panel's permissions block reads: GET /api/sessions/{id}/permissions.
import { BOARD_LEVEL_LABELS, boardLevelChip, boardLevelRank, isBoardLevel, type BoardLevel } from './permissionsModal'

export interface ProposalGrant {
  id: string
  kind: 'board_file' | 'mcp_tool'
  /** A board permission's level (0.4.4). */
  level?: BoardLevel
  board_id?: string
  board_name?: string
  connection_id?: string
  connection_name?: string
  tool?: string
  created_via: 'card' | 'panel' | 'launch' | 'parent'
  created_at: string
  /** False when it no longer approves (absent on an older server: active). */
  active?: boolean
  /** Why not: its sign-in ended, or the tool's definition changed since. */
  inactive?: 'signed_out' | 'tool_changed' | string
}
export interface McpToolOption { connection_id: string; connection_name: string; tool: string }
/** One board above propose in the session (the Boards section and its chips). */
export interface BoardAccess {
  board_id: string
  board_name?: string
  level: BoardLevel
  grant_id?: string
  created_via?: string
  created_at?: string
  /** The board the session's launch gave a connection to (its engine has the board's own tools). */
  launch?: boolean
  /** False when the sign-in it was given from has ended. */
  active?: boolean
  inactive?: string
  /** A session started by another one: its own level, and the level of the session that started it
   *  now. `level` is the lower of the two, what the session acts at. */
  own_level?: BoardLevel
  parent_level?: BoardLevel
}
/** The session that started this one, whose board levels cap this one's. */
export interface StartedBy { id: string; title: string; boards: { board_id: string; board_name?: string; level: BoardLevel }[] }
export interface PermissionsView { can_grant: boolean; reason: string; grants: ProposalGrant[]; mcp_tools: McpToolOption[]; boards: BoardAccess[]; started_by?: StartedBy }

const str = (v: unknown): v is string => typeof v === 'string'
export function readPermissions(v: unknown): PermissionsView {
  if (!v || typeof v !== 'object') throw new Error('invalid permissions')
  const p = v as PermissionsView
  if (typeof p.can_grant !== 'boolean' || !str(p.reason) || !Array.isArray(p.grants) || !Array.isArray(p.mcp_tools)) throw new Error('invalid permissions')
  for (const g of p.grants) {
    if (!g || !str(g.id) || (g.kind !== 'board_file' && g.kind !== 'mcp_tool') || !str(g.created_via)) throw new Error('invalid permissions')
  }
  for (const t of p.mcp_tools) {
    if (!t || !str(t.connection_id) || !str(t.connection_name) || !str(t.tool)) throw new Error('invalid permissions')
  }
  // An older server sends no boards: its filing permissions are level file.
  const boards: BoardAccess[] = p.boards === undefined
    ? p.grants.filter(g => g.kind === 'board_file').map(g => ({ board_id: g.board_id ?? '', board_name: g.board_name, level: 'file' as const, grant_id: g.id, created_via: g.created_via, created_at: g.created_at, active: g.active, inactive: g.inactive }))
    : p.boards
  if (!Array.isArray(boards)) throw new Error('invalid permissions')
  for (const b of boards) {
    if (!b || !str(b.board_id) || !isBoardLevel(b.level)) throw new Error('invalid permissions')
    if ((b.own_level !== undefined && !isBoardLevel(b.own_level)) || (b.parent_level !== undefined && !isBoardLevel(b.parent_level))) throw new Error('invalid permissions')
  }
  const s = p.started_by
  if (s !== undefined && (!s || !str(s.id) || !str(s.title) || !Array.isArray(s.boards) || s.boards.some(b => !b || !str(b.board_id) || !isBoardLevel(b.level)))) {
    throw new Error('invalid permissions')
  }
  return { ...p, boards }
}

/** The session's own level for a board: what Save changes (a started session acts at the lower of
 *  it and its parent's level). */
export const ownLevel = (b: BoardAccess): BoardLevel => b.own_level ?? b.level
/** For a started session: each board's cap, the level the session that started it has now (a board
 *  not listed is at propose). */
export function boardCaps(s: StartedBy | undefined): Record<string, BoardLevel> | undefined {
  if (!s) return undefined
  return Object.fromEntries(s.boards.map(b => [b.board_id, b.level]))
}


/** A board's name, or its id when the server knows no name. */
export const boardName = (b: BoardAccess) => b.board_name || b.board_id
/** "Product · Full", as a chip says it. */
export const boardChip = (b: BoardAccess) => boardLevelChip(boardName(b), b.level)
/** What a board's level allows, spoken: "Full access to Product". */
export const boardScope = (b: BoardAccess) => `${BOARD_LEVEL_LABELS[b.level]} access to ${boardName(b)}`

/** How and when a board's level was set. */
export function boardDetail(b: BoardAccess): string {
  const base = grantDetail({ id: b.grant_id ?? '', kind: 'board_file', created_via: (b.created_via ?? 'launch') as ProposalGrant['created_via'], created_at: b.created_at ?? '' })
  if (b.launch) return `${base} · the board chosen at launch`
  const capped = capNote(b)
  return capped ? `${base} · ${capped}` : base
}

/** For a started session's board held below its own level: "Capped at Read by the session that started it". */
export function capNote(b: BoardAccess): string {
  if (!b.parent_level || !b.own_level || boardLevelRank(b.own_level) <= boardLevelRank(b.parent_level)) return ''
  return `capped at ${BOARD_LEVEL_LABELS[b.parent_level]} by the session that started it (its own level is ${BOARD_LEVEL_LABELS[b.own_level]})`
}

export function grantScope(g: ProposalGrant): string {
  return g.kind === 'board_file' ? `File cards on ${g.board_name || g.board_id} without asking` : `Run ${g.connection_name} · ${g.tool} without asking`
}


/** Why a dormant grant does not approve, as the panel says it. */
export function inactiveLabel(g: ProposalGrant): string {
  return g.inactive === 'tool_changed' ? 'The tool changed since; allow it again to approve the new version'
    : 'Needs you signed in: it was given from a sign-in that has ended; allow it again'
}

/** The target alone, for a chip: the board's name, or "connection · tool". */
export function grantTarget(g: ProposalGrant): string {
  return g.kind === 'board_file' ? (g.board_name || g.board_id || '') : `${g.connection_name} · ${g.tool}`
}

const HOW: Record<string, string> = { card: 'Given from a card in the chat', panel: 'Given in the session panel', launch: 'Given at launch', parent: 'Passed down by the session that started it' }

/** How and when a grant was given, as the Permissions modal lists it. */
export function grantDetail(g: ProposalGrant): string {
  const how = HOW[g.created_via] ?? `Given (${g.created_via})`
  const at = new Date(g.created_at)
  if (!g.created_at || Number.isNaN(at.getTime())) return how
  const sameDay = at.toDateString() === new Date().toDateString()
  const when = sameDay ? at.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
    : at.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
  return `${how}, ${sameDay ? 'today at ' : ''}${when}`
}
