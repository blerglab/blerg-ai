// The launch sheet's permissions (POST /api/sessions "permissions"), and which of the person's ticks
// still apply to what the sheet now says (a runtime, board list or MCP selection that changed).
import type { RunRuntime } from './runtimes'
import type { McpSelectionEntry } from '../types'
import type { BoardLevel } from './permissionsModal'

export interface LaunchPermissionsValue {
  children: string[]
  /** A board's access level from the start (read, file without asking, or full). */
  board_levels: { board_id: string; level: BoardLevel }[]
  mcp_tools: { connection_id: string; tool: string }[]
}

export const NO_LAUNCH_PERMISSIONS: LaunchPermissionsValue = { children: [], board_levels: [], mcp_tools: [] }

/** The children runtimes a session on `runtime` can be allowed to start without asking: a sandboxed
 *  parent never pre-approves unsandboxed children; a cluster session starts cluster children; an
 *  unsandboxed desktop session can also be allowed cluster children where the install has a
 *  cluster, and a sandboxed one only where the runner allows it (BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN
 *  = "any", `sandboxClusterAny`); otherwise it asks for each one. */
export function childRuntimesFor(runtime: RunRuntime, clusterConfigured = false, sandboxClusterAny = false): string[] {
  if (runtime === 'cluster') return ['cluster']
  const desktop = runtime === 'docker' ? ['docker'] : ['docker', 'daemon']
  return clusterConfigured && (runtime === 'daemon' || sandboxClusterAny) ? [...desktop, 'cluster'] : desktop
}

export function effectiveLaunchPermissions(v: LaunchPermissionsValue, now: { runtime: RunRuntime; boards: { id: string }[]; mcp: McpSelectionEntry[]; clusterConfigured?: boolean; sandboxClusterAny?: boolean }): LaunchPermissionsValue {
  const runtimes = childRuntimesFor(now.runtime, now.clusterConfigured, now.sandboxClusterAny)
  const proposes = (c: string, tool: string) => now.mcp.some(e => e.connection === c && e.tools[tool]?.mode === 'propose')
  return {
    children: v.children.filter(c => runtimes.includes(c)),
    board_levels: v.board_levels.filter(b => b.level !== 'propose' && now.boards.some(x => x.id === b.board_id)),
    mcp_tools: v.mcp_tools.filter(t => proposes(t.connection_id, t.tool)),
  }
}
