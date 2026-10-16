import { describe, expect, it } from 'vitest'
import { doingLine } from './subagents'
import type { SubagentEntry } from './toolGroups'
import type { AgentEvent } from '../types'

const doing = (input: unknown) => doingLine({
  latest: { type: 'agent_event', session_id: 's', client_event_id: 'c', ts: '', kind: 'tool_call', payload: { tool: 'Bash', call_id: 'b', input } } as AgentEvent,
} as SubagentEntry)

// The panel's Running now line (and the server's, runner/internal/livestate doingLine) says what a
// subagent's Bash call does when the call says it.
describe('doingLine', () => {
  it("names a Bash call's description, else its command", () => {
    expect(doing({ command: 'npm test', description: 'Run the unit tests' })).toBe('running Bash · Run the unit tests')
    expect(doing({ command: 'npm test', description: 'x'.repeat(60) })).toBe(`running Bash · ${'x'.repeat(47)}…`)
    expect(doing({ command: 'npm test', description: 'y'.repeat(48) })).toBe(`running Bash · ${'y'.repeat(48)}`)
    expect(doing({ command: 'npm test\nnpm run lint', description: ' ' })).toBe('running Bash · npm test')
  })
})
