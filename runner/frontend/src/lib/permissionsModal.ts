// The permissions modal's model: a draft of what the session may do without asking, the search
// filter, "Select all shown", and the diff Save applies. Pure, so the rules are tested apart from
// the dialog. The same draft serves a running session (Save goes through the grant APIs) and the
// launch sheet (Save hands the draft back; the server grants it as the session starts).
import type { ApprovalRuntime, ChildRuntime } from '../hooks/useChildApproval'
import type { LaunchPermissionsValue } from './launchPermissions'
import { childRuntimesFor } from './launchPermissions'
import type { RunRuntime } from './runtimes'

export type ChildState = 'on' | 'off' | 'partial'
export interface PermissionsDraft {
  /** Per runtime: allowed for every engine, none, or (as the server reports it) some engines only. */
  children: Partial<Record<ChildRuntime, ChildState>>
  /** Boards cards may be filed on without asking. */
  boards: string[]
  /** MCP tools (toolKey) that may run without asking. */
  tools: string[]
  /** Per-engine child grants (delegationKey) to revoke. */
  revokeDelegations: string[]
  /** Grants that no longer approve (signed out, tool changed) to revoke. */
  revokeDormant: string[]
}
export const EMPTY_DRAFT: PermissionsDraft = { children: {}, boards: [], tools: [], revokeDelegations: [], revokeDormant: [] }

const SEP = '\u0000'
export const toolKey = (connectionId: string, tool: string) => `${connectionId}${SEP}${tool}`
export const splitToolKey = (key: string) => { const i = key.indexOf(SEP); return { connection_id: key.slice(0, i), tool: key.slice(i + 1) } }
/** A row of per-engine child grants: its runtime first, so turning the runtime off covers it. */
export const delegationKey = (runtime: string, rest: string) => `${runtime}${SEP}${rest}`
const delegationRuntime = (key: string) => key.slice(0, key.indexOf(SEP))

export interface PermissionChanges {
  children: { runtime: ChildRuntime; enabled: boolean }[]
  grantBoards: string[]
  revokeBoards: string[]
  grantTools: { connection_id: string; tool: string }[]
  revokeTools: { connection_id: string; tool: string }[]
  revokeDelegations: string[]
  revokeDormant: string[]
}

/** What Save does to turn `initial` into `next`. `dormant` names each dormant grant's scope, so one
 *  given again is renewed by the grant (the server replaces it) rather than revoked as well. */
export function diffPermissions(initial: PermissionsDraft, next: PermissionsDraft, dormant: Record<string, { board?: string; tool?: string }> = {}): PermissionChanges {
  const children: PermissionChanges['children'] = []
  for (const runtime of Object.keys(next.children) as ChildRuntime[]) {
    const was = initial.children[runtime] ?? 'off'
    const now = next.children[runtime] ?? 'off'
    if (now !== was && now !== 'partial') children.push({ runtime, enabled: now === 'on' })
  }
  const turnedOff = new Set(children.filter(c => !c.enabled).map(c => c.runtime as string))
  const added = <T,>(a: T[], b: T[]) => b.filter(x => !a.includes(x))
  const grantBoards = added(initial.boards, next.boards)
  const grantTools = added(initial.tools, next.tools)
  return {
    children,
    grantBoards,
    revokeBoards: added(next.boards, initial.boards),
    grantTools: grantTools.map(splitToolKey),
    revokeTools: added(next.tools, initial.tools).map(splitToolKey),
    revokeDelegations: next.revokeDelegations.filter(k => !turnedOff.has(delegationRuntime(k))),
    revokeDormant: next.revokeDormant.filter(id => {
      const scope = dormant[id]
      return !(scope?.board && grantBoards.includes(scope.board)) && !(scope?.tool && grantTools.includes(scope.tool))
    }),
  }
}

export function changeCounts(c: PermissionChanges) {
  return {
    grant: c.children.filter(x => x.enabled).length + c.grantBoards.length + c.grantTools.length,
    revoke: c.children.filter(x => !x.enabled).length + c.revokeBoards.length + c.revokeTools.length + c.revokeDelegations.length + c.revokeDormant.length,
  }
}

/** One line: "Grant 2, revoke 1", "Revoke 1", or "No changes". */
export function changeSummary(c: PermissionChanges): string {
  const { grant, revoke } = changeCounts(c)
  if (!grant && !revoke) return 'No changes'
  const parts = [grant ? `grant ${grant}` : '', revoke ? `revoke ${revoke}` : ''].filter(Boolean).join(', ')
  return parts.charAt(0).toUpperCase() + parts.slice(1)
}

/** Every word of the query, in any order or case, somewhere in the texts. */
export function matchesQuery(query: string, ...texts: (string | undefined)[]): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (!words.length) return true
  const hay = texts.filter(Boolean).join(' ').toLowerCase()
  return words.every(w => hay.includes(w))
}

/** "Select all shown": selects every item the filter shows; when all of them already are, clears
 *  them instead. Selections the filter hides are kept either way. */
export function toggleShown(selected: string[], shown: string[]): string[] {
  if (!shown.length) return selected
  if (shown.every(id => selected.includes(id))) return selected.filter(id => !shown.includes(id))
  return [...selected, ...shown.filter(id => !selected.includes(id))]
}

export interface ChildOption {
  runtime: ChildRuntime
  label: string
  /** What it means, said once under the toggle. */
  note?: string
  engines: string[]
  grantedEngines: string[]
  standingEngines: string[]
  state: ChildState
  /** Turning it on is allowed. */
  canEnable: boolean
  /** The toggle cannot be used at all (nothing to enable and nothing to revoke). */
  disabled: boolean
  /** Why it is limited, from the server or the launch rules. */
  reason: string
}

const childLabel = (runtime: ChildRuntime) => runtime === 'cluster' ? 'Start children on the cluster without asking'
  : runtime === 'docker' ? 'Start sandboxed children without asking' : 'Start unsandboxed children without asking'
const childNote = (runtime: ChildRuntime) => runtime === 'daemon' ? 'Unsandboxed children run as you with full filesystem access.' : undefined

/** A running session's runtime row, from the server's snapshot. */
export function sessionChildOption(r: ApprovalRuntime): ChildOption {
  return {
    runtime: r.runtime, label: childLabel(r.runtime), note: childNote(r.runtime),
    engines: r.allowed_engines, grantedEngines: r.granted_engines, standingEngines: r.standing_rule_engines,
    state: r.enabled ? 'on' : r.partial ? 'partial' : 'off',
    canEnable: r.can_enable,
    disabled: !r.can_enable && !r.granted_engines.length && !r.enabled,
    reason: r.reason,
  }
}

export const SANDBOXED_PARENT_REASON = "A sandboxed session can't pre-approve unsandboxed children."

/** The launch sheet's runtime rows: a cluster session starts cluster children; a sandboxed one is
 *  shown the unsandboxed row disabled, with the reason. */
export function launchChildOptions(runtime: RunRuntime, engines: string[] = []): ChildOption[] {
  const allowed = childRuntimesFor(runtime) as ChildRuntime[]
  const rows: ChildRuntime[] = runtime === 'cluster' ? ['cluster'] : ['docker', 'daemon']
  return rows.map(r => {
    const ok = allowed.includes(r)
    return {
      runtime: r, label: childLabel(r), note: childNote(r), engines, grantedEngines: [], standingEngines: [],
      state: 'off', canEnable: ok, disabled: !ok, reason: ok ? '' : SANDBOXED_PARENT_REASON,
    }
  })
}

/** The next state of a runtime toggle when clicked: a partial grant becomes all engines when that is
 *  allowed, otherwise it is revoked. */
export function nextChildState(o: ChildOption, current: ChildState): ChildState {
  if (current === 'on') return 'off'
  if (current === 'partial') return o.canEnable ? 'on' : 'off'
  return o.canEnable ? 'on' : 'off'
}

export function launchDraft(v: LaunchPermissionsValue): PermissionsDraft {
  const children: PermissionsDraft['children'] = {}
  for (const c of v.children) children[c as ChildRuntime] = 'on'
  return { ...EMPTY_DRAFT, children, boards: [...v.boards], tools: v.mcp_tools.map(t => toolKey(t.connection_id, t.tool)) }
}

/** The launch value the draft stands for, limited to what the sheet still offers. */
export function draftToLaunch(d: PermissionsDraft, now: { runtime: RunRuntime; boards: { id: string }[]; tools: { connection_id: string; tool: string }[] }): LaunchPermissionsValue {
  const runtimes = childRuntimesFor(now.runtime)
  return {
    children: runtimes.filter(r => d.children[r as ChildRuntime] === 'on'),
    boards: d.boards.filter(b => now.boards.some(x => x.id === b)),
    mcp_tools: d.tools.map(splitToolKey).filter(t => now.tools.some(x => x.connection_id === t.connection_id && x.tool === t.tool)),
  }
}
