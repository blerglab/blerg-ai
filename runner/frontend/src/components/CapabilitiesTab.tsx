// CapabilitiesTab: the session panel's Capabilities — what the session loaded (skills, plugins, MCP
// servers, slash commands, tools, subagents) from its latest capabilities report, what was the Skills &
// plugins dialog. Kept mounted while another tab shows, so its filter and opened groups survive a
// switch. The panel reads the report (the server is asked only while this tab is on screen) and
// counts the enabled ones for the tab's badge. On top: the report's header card, then one toolbar row
// (the filter and, for the session's owner, "+ Add", which opens the plugin browser, PluginBrowser).
// An MCP server that needs a sign-in gets its action under the row (McpSignIn), for the owner.
import { useState } from 'react'
import type { McpLive } from '@blerglab/chat'
import type { CapabilitiesReport } from '../lib/capabilities'
import type { useSessionPlugins } from '../hooks/useSessionPlugins'
import { CapabilitiesView } from './CapabilitiesPanel'
import { PluginAddButton, PluginRequests, DisallowedPlugins } from './SessionPlugins'
import PluginBrowser from './PluginBrowser'
import McpSignIn from './McpSignIn'
import './CapabilitiesPanel.css'

export interface CapabilitiesTabProps {
  loading?: boolean
  active?: boolean
  startedBySession?: boolean
  report: CapabilitiesReport | null
  /** Tool changes waiting on the session: noted on the MCP servers group. */
  pendingToolChanges: number
  /** The live state's MCP servers: their status now, over the start-up snapshot. */
  liveMcp?: McpLive | null
  /** The session's plugin choices, when the person may add plugins to it (its owner). */
  plugins?: ReturnType<typeof useSessionPlugins> | null
  /** The session's runtime ("daemon", "docker", "cluster"): sign-in is for host sessions. */
  runtime?: string
  /** The name of the machine that runs the session, for the sign-in confirm. */
  machine?: string
}

export default function CapabilitiesTab({ loading = false, active = true, report, pendingToolChanges, liveMcp, startedBySession, plugins, runtime, machine }: CapabilitiesTabProps) {
  // Open for one session only: a switch to another session closes the form.
  const [addFor, setAddFor] = useState<string | null>(null)
  const adding = !!plugins && addFor === plugins.sessionId
  return (
    <div className="rail-capabilities" data-testid="rail-capabilities">
      {startedBySession && <p className="rail-empty caps-skipped">Plugins: skipped (started by another session)</p>}
      <CapabilitiesView initialLoading={loading && active} report={report} pendingToolChanges={pendingToolChanges} liveMcp={liveMcp}
        toolbarAction={plugins && <PluginAddButton open={adding} onToggle={() => setAddFor(adding ? null : plugins.sessionId)} />}
        belowToolbar={plugins && (
          <div className="caps-addons">
            <PluginRequests model={plugins} />
 <DisallowedPlugins model={plugins} />
          </div>
        )}
        itemAction={plugins ? (group, item) => group === 'mcp' ? <McpSignIn key={plugins.sessionId} item={item} ctx={{ sessionId: plugins.sessionId, runtime, machine }} /> : null : undefined} />
      {adding && plugins && <PluginBrowser key={plugins.sessionId} model={plugins} onClose={() => setAddFor(null)} />}
    </div>
  )
}
