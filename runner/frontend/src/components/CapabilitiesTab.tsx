// CapabilitiesTab: the session panel's Capabilities — what the session loaded (skills, plugins, MCP
// servers, slash commands, tools, subagents) from its latest capabilities report, what was the Skills &
// plugins dialog. Kept mounted while another tab shows, so its filter and opened groups survive a
// switch. The panel reads the report (the server is asked only while this tab is on screen) and
// counts the enabled ones for the tab's badge.
import type { CapabilitiesReport } from '../lib/capabilities'
import { CapabilitiesView } from './CapabilitiesPanel'
import './CapabilitiesPanel.css'

export interface CapabilitiesTabProps {
  report: CapabilitiesReport | null
  /** Tool changes waiting on the session: noted on the MCP servers group. */
  pendingToolChanges: number
}

export default function CapabilitiesTab({ report, pendingToolChanges }: CapabilitiesTabProps) {
  return (
    <div className="rail-capabilities" data-testid="rail-capabilities">
      <CapabilitiesView report={report} pendingToolChanges={pendingToolChanges} />
    </div>
  )
}
