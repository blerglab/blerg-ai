import { describe, expect, it } from 'vitest'
import { doingLine, subagentEffort, subagentModel } from './subagents'
import { buildTimeline, type SubagentEntry } from './toolGroups'
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


describe('subagentEffort', () => {
  it('uses only child metadata and lets unknown clear a previous report', () => {
    const sub = { info: { effort: 'low' }, update: { payload: { effort: 'high' } } } as unknown as SubagentEntry
    expect(subagentEffort(sub)).toBe('high')
    sub.info.model = 'child'
    sub.update = { payload: { model: 'child' } } as AgentEvent
    expect(subagentEffort(sub)).toBe('low')
    sub.update = { payload: { model: 'other-child' } } as AgentEvent
    expect(subagentEffort(sub)).toBe('')
    sub.done = { payload: { effort: '' } } as AgentEvent
    expect(subagentEffort(sub)).toBe('')
    sub.done = { payload: { effort: null } } as AgentEvent
    expect(subagentEffort(sub)).toBe('')
    expect(subagentEffort({ info: {}, call: { call: { input: { effort: 'high' } } } } as unknown as SubagentEntry)).toBe('')
  })
})


it('pairs the latest child model and effort after an earlier background launch', () => {
  const rows = buildTimeline([
    { client_event_id: '1', kind: 'subagent_started', payload: { child_id: 'c', model: 'a', effort: 'high' } },
    { client_event_id: '2', kind: 'subagent_done', payload: { child_id: 'c', model: 'a', effort: 'high', background: true } },
    { client_event_id: '3', kind: 'subagent_update', payload: { child_id: 'c', model: 'b', effort: 'low' } },
  ] as AgentEvent[])
  const child = rows.find(row => row.type === 'subagent')
  if (!child || child.type !== 'subagent') throw new Error('missing child')
  expect(subagentModel(child.sub)).toBe('b')
  expect(subagentEffort(child.sub)).toBe('low')
  const finished = buildTimeline([
      { client_event_id: '1', kind: 'subagent_started', payload: { child_id: 'c', model: 'a', effort: 'high' } },
      { client_event_id: '2', kind: 'subagent_update', payload: { child_id: 'c', model: 'b', effort: 'low' } },
      { client_event_id: '3', kind: 'subagent_done', payload: { child_id: 'c', model: 'final', effort: 'medium' } },
  ] as AgentEvent[]).find(row => row.type === 'subagent')
  if (!finished || finished.type !== 'subagent') throw new Error('missing final child')
  expect(subagentModel(finished.sub)).toBe('final')
  expect(subagentEffort(finished.sub)).toBe('medium')
})
