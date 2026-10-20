// What the session panel's permissions block reads: GET /api/sessions/{id}/permissions.

export interface ProposalGrant {
  id: string
  kind: 'board_file' | 'mcp_tool'
  board_id?: string
  board_name?: string
  connection_id?: string
  connection_name?: string
  tool?: string
  created_via: 'card' | 'panel' | 'launch'
  created_at: string
  /** False when it no longer approves (absent on an older server: active). */
  active?: boolean
  /** Why not: its sign-in ended, or the tool's definition changed since. */
  inactive?: 'signed_out' | 'tool_changed' | string
}
export interface McpToolOption { connection_id: string; connection_name: string; tool: string }
export interface PermissionsView { can_grant: boolean; reason: string; grants: ProposalGrant[]; mcp_tools: McpToolOption[] }

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
  return p
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

const HOW: Record<string, string> = { card: 'Given from a card in the chat', panel: 'Given in the session panel', launch: 'Given at launch' }
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
