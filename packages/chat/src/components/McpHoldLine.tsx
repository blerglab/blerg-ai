// The line above the composer while the first message is held for the session's MCP servers:
// "Waiting for MCP servers before sending your first message · 0:12 / 0:30 · Send now". Send now is
// the host's (`onSendNow`): absent, for a viewer who may not release, there is no button.
import { holdClock, type McpHold } from '../model/mcp'
import { useTicker } from '../hooks/useTicker'

export function McpHoldLine({ hold, clockOffset = 0, onSendNow }: { hold: McpHold; clockOffset?: number; onSendNow?: () => void }) {
  const now = useTicker(true) - clockOffset
  return (
    <div className="agent-composer-hint mcp-hold" role="status" data-testid="mcp-hold">
      <span className="queue-spinner" aria-hidden="true" />
      <span>Waiting for MCP servers before sending your first message · {holdClock(hold, now)}</span>
      {onSendNow && <> · <button type="button" className="mcp-send-now" onClick={onSendNow}>Send now</button></>}
    </div>
  )
}
