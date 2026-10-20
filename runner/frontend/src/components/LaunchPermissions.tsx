// LaunchPermissions: the launch sheet's "Permissions…" button. It opens the Permissions modal in
// pre-authorize mode: the same permissions as the session panel's (start children without asking
// where this runtime can, file cards on a board, run an MCP tool set to approval mode), held here
// until launch. The server grants them as the session starts, through the panel's own paths; one it
// cannot grant is reported after launch and the session asks as usual. The button counts what
// still applies to the sheet as it stands.
import { useMemo, useState } from 'react'
import type { RunKind, RunRuntime } from '../lib/runtimes'
import type { McpSelectionEntry } from '../types'
import { effectiveLaunchPermissions, type LaunchPermissionsValue } from '../lib/launchPermissions'
import { draftToLaunch, launchChildOptions, launchDraft, toolKey } from '../lib/permissionsModal'
import PermissionsModal, { type ActiveEntry } from './PermissionsModal'

interface Props {
  kind: RunKind
  runtime: RunRuntime
  boards: { id: string; name: string }[]
  /** Board discovery is still running (the modal says so instead of "no boards"). */
  boardsLoading?: boolean
  mcp: McpSelectionEntry[]
  connectionName: (id: string) => string
  /** The engines the chosen desktop or cluster offers, listed under the children toggles. */
  engines?: string[]
  value: LaunchPermissionsValue
  onChange: (v: LaunchPermissionsValue) => void
}

const GIVEN = 'Given as the session starts'

export default function LaunchPermissions({ kind, runtime, boards, boardsLoading, mcp, connectionName, engines = [], value, onChange }: Props) {
  const [open, setOpen] = useState(false)
  const tools = useMemo(() => mcp.flatMap(e => Object.entries(e.tools).filter(([, t]) => t.mode === 'propose')
    .map(([tool]) => ({ connection_id: e.connection, connection_name: connectionName(e.connection), tool }))), [mcp, connectionName])
  const effective = effectiveLaunchPermissions(value, { runtime, boards, mcp })
  const draft = launchDraft(effective)
  const initial = { boards: draft.boards, tools: draft.tools, revokeDelegations: [], revokeDormant: [] }
  if (kind !== 'agent') return null
  const count = effective.children.length + effective.boards.length + effective.mcp_tools.length
  const options = launchChildOptions(runtime, engines).map(o => ({ ...o, state: draft.children[o.runtime] ?? 'off' }))
  const active: ActiveEntry[] = [
    ...options.filter(o => o.state === 'on').map(o => ({ key: `c:${o.runtime}`, kind: 'child' as const, label: o.label, detail: GIVEN, target: { child: o.runtime } })),
    ...effective.boards.map(id => ({ key: `b:${id}`, kind: 'board' as const, label: `File cards on ${boards.find(b => b.id === id)?.name ?? id} without asking`, detail: GIVEN, target: { board: id } })),
    ...effective.mcp_tools.map(t => ({ key: `t:${t.connection_id}:${t.tool}`, kind: 'tool' as const, label: `Run ${connectionName(t.connection_id)} · ${t.tool} without asking`, detail: GIVEN, target: { tool: toolKey(t.connection_id, t.tool) } })),
  ]
  return (
    <div className="launch-permissions" data-testid="launch-permissions">
      <button type="button" className="launch-permissions-button" aria-haspopup="dialog" onClick={() => setOpen(true)}>
        Permissions…{count > 0 && <>{' '}<span data-testid="launch-permissions-count">({count})</span></>}
      </button>
      <span className="launch-permissions-hint">{count > 0 ? 'Given as the session starts; revoke them in the session panel.' : 'What the session may do without asking you.'}</span>
      {open && <PermissionsModal mode="launch" onClose={() => setOpen(false)}
        childSessions={{ state: 'ready', value: { options, note: runtime === 'cluster' ? 'Children of this session on the cluster, with the engines listed.' : 'Children of this session on this desktop, in any repository. This does not change desktop rules.' } }}
        boards={boardsLoading ? { state: 'loading' } : { state: 'ready', value: boards }}
        tools={tools} canGrant initial={initial} active={active}
        onSave={async next => { onChange(draftToLaunch(next, { runtime, boards, tools })) }} />}
    </div>
  )
}
