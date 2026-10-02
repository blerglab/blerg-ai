// What the MCP picker chooses and how it becomes the launch payload (spec 6.1, 6.3). Kept out
// of the component file so the launch sheet, the crons form and tests share one source.
import type { McpSelectionEntry, McpToolInfo, McpToolMode } from '../types'

export const MCP_ALLOW_WARNING = 'this can change or send things without asking you'
export const MCP_PROPOSE_LABEL = 'queued for your approval'

export type McpToolsState =
  | { status: 'loading' }
  | { status: 'error'; message: string }
  | { status: 'ready'; tools: McpToolInfo[] }
// connection id -> tool name -> mode
export type McpModes = Record<string, Record<string, McpToolMode>>
// connection id -> tool name -> the hash a value came in with
export type McpSeenHashes = Record<string, Record<string, string>>

export const isReadOnlyTool = (t: McpToolInfo) => t.annotations?.readOnlyHint === true

// buildSelection turns the picker's state into the launch payload: only checked connections,
// only tools that are not off, each with the hash the person saw (the live one when the tools
// are loaded, else the one the value came in with). A connection with no tool left is omitted:
// an entry with an empty tool set would be refused.
export function buildSelection(
  checked: Set<string>, modes: McpModes, tools: Record<string, McpToolsState>, seen: McpSeenHashes,
): McpSelectionEntry[] {
  const out: McpSelectionEntry[] = []
  for (const id of checked) {
    const picked: McpSelectionEntry['tools'] = {}
    for (const [name, mode] of Object.entries(modes[id] ?? {})) {
      if (mode === 'off') continue
      const st = tools[id]
      const live = st?.status === 'ready' ? st.tools.find(t => t.name === name)?.hash : undefined
      const hash = live ?? seen[id]?.[name]
      if (!hash) continue
      picked[name] = { mode, hash }
    }
    if (Object.keys(picked).length > 0) out.push({ connection: id, tools: picked })
  }
  return out
}
