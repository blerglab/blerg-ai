import type { ToolSchemaRefresh } from '../types'

// Adapter-reported behavior, never inferred from an engine's display name.
export default function ToolSchemaGuidance({ policy }: { policy?: ToolSchemaRefresh }) {
  const text = policy === 'new_session'
    ? 'Updated tool definitions require a new session. Start a new session and select the current MCP connections and tools. Keep this conversation available; its existing tool definitions stay loaded. Current Blerg grants continue to apply to every call.'
    : policy === 'next_turn'
      ? 'Accepted tool definitions reload when the engine is idle or before its next turn. An active turn keeps its loaded tools. Current Blerg grants continue to apply to every call.'
      : "This session hasn't reported whether it can reload tool definitions. If updated tools aren't available, start a new session and select the current MCP connections and tools. Your existing conversation stays available."
  return <p data-testid="tool-schema-guidance" style={{ color: 'var(--mist)', fontSize: 12, lineHeight: 1.5, margin: 0 }}>{text}</p>
}
