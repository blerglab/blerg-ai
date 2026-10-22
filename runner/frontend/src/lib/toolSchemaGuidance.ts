// What a session's tool-schema refresh policy means for updated tool definitions, in words. Adapter-
// reported behavior, never inferred from an engine's display name.
import type { ToolSchemaRefresh } from '../types'

/** The guidance in words, for a caller that also announces it (the Capabilities ⓘ). */
export function toolSchemaGuidanceText(policy?: ToolSchemaRefresh): string {
  return policy === 'new_session'
    ? 'Updated tool definitions require a new session. Start a new session and select the current MCP connections and tools. Keep this conversation available; its existing tool definitions stay loaded. Current Blerg grants continue to apply to every call.'
    : policy === 'next_turn'
      ? 'Accepted tool definitions reload when the engine is idle or before its next turn. An active turn keeps its loaded tools. Current Blerg grants continue to apply to every call.'
      : "This session hasn't reported whether it can reload tool definitions. If updated tools aren't available, start a new session and select the current MCP connections and tools. Your existing conversation stays available."
}
