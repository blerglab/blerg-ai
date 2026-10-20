// The launch sheet's permissions (POST /api/sessions "permissions"), and which of the person's ticks
// still apply to what the sheet now says (a runtime, board list or MCP selection that changed).
import type { RunRuntime } from './runtimes'
import type { McpSelectionEntry } from '../types'

export interface LaunchPermissionsValue {
  children: string[]
  boards: string[]
  mcp_tools: { connection_id: string; tool: string }[]
}

export const NO_LAUNCH_PERMISSIONS: LaunchPermissionsValue = { children: [], boards: [], mcp_tools: [] }

/** The children runtimes a session on `runtime` can be allowed to start without asking: a sandboxed
 *  parent never pre-approves unsandboxed children; a cluster session starts cluster children. */
export function childRuntimesFor(runtime: RunRuntime): string[] {
  return runtime === 'cluster' ? ['cluster'] : runtime === 'docker' ? ['docker'] : ['docker', 'daemon']
}

export function effectiveLaunchPermissions(v: LaunchPermissionsValue, now: { runtime: RunRuntime; boards: { id: string }[]; mcp: McpSelectionEntry[] }): LaunchPermissionsValue {
  const runtimes = childRuntimesFor(now.runtime)
  const proposes = (c: string, tool: string) => now.mcp.some(e => e.connection === c && e.tools[tool]?.mode === 'propose')
  return {
    children: v.children.filter(c => runtimes.includes(c)),
    boards: v.boards.filter(b => now.boards.some(x => x.id === b)),
    mcp_tools: v.mcp_tools.filter(t => proposes(t.connection_id, t.tool)),
  }
}
