import { describe, expect, it } from 'vitest'
import { buildTimeline, type SubagentEntry } from './toolGroups'
import { liveSubagents, type SubagentLive } from './liveState'
import { handBackReport, resumeAck, resumedSubagent, subagentStatus, subagentTitle } from './subagents'
import { subagentWaitingOn } from './waitingOnSubagent'
import type { AgentEvent } from '../types'

// A finished subagent the main agent resumed with SendMessage: the daemon reports a new instance
// under the SendMessage call's id (subagent_started with resumed, the original's title and type).

let seq = 0
const T0 = Date.parse('2026-10-15T10:00:00Z')
function ev(kind: string, payload: unknown): AgentEvent {
  seq++
  return { type: 'agent_event', client_event_id: `e${seq}`, seq, kind, payload, ts: new Date(T0 + seq * 1000).toISOString() } as AgentEvent
}
const NOW = T0 + 3600_000
const ACK = '{"success":true,"message":"Resuming agent a1b2c3d","resumedAgentId":"a1b2c3d4e5f6a7b80"}'

function session(opts: { background?: boolean; ended?: boolean } = {}) {
  seq = 0
  const bg = opts.background === true
  const events = [
    ev('user_message', { text: 'go', source: 'chat' }),
    ev('tool_call', { tool: 'Agent', call_id: 'A', input: { description: 'Implement the data tasks', prompt: 'Do 1-5', subagent_type: 'Explore', ...(bg ? { run_in_background: true } : {}) } }),
    ev('subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Implement the data tasks' }),
    ev('tool_call', { tool: 'Bash', call_id: 'a1', input: { command: 'ls' }, parent_call_id: 'A' }),
    ...(bg ? [ev('subagent_done', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'running in the background', background: true })] : []),
    ev('subagent_done', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'All in.' }),
    ev('tool_result', { call_id: 'A', output: 'All in.', is_error: false, duration_ms: 0 }),
    ev('user_message', { text: 'review found two issues', source: 'chat' }),
    ev('tool_call', { tool: 'SendMessage', call_id: 'S', input: { to: 'a1b2c3d4e5f6a7b80', summary: 'Fix round 1', message: 'Fix both issues.' } }),
    ev('subagent_started', { child_id: 'S', agent_type: 'Explore', model: '', summary: 'Implement the data tasks', resumed: true, resumed_from: 'A', ...(bg ? { background: true } : {}) }),
    ev('tool_result', { call_id: 'S', output: ACK, is_error: false, duration_ms: 0 }),
    ev('tool_call', { tool: 'Bash', call_id: 's1', input: { command: 'make check' }, parent_call_id: 'S' }),
    ev('turn_done', { stop_reason: 'end_turn' }),
    ...(opts.ended ? [ev('subagent_done', { child_id: 'S', agent_type: 'Explore', model: '', summary: 'Fixed both.', resumed: true, resumed_from: 'A' })] : []),
  ]
  return events
}

function cards(events: AgentEvent[]): SubagentEntry[] {
  return buildTimeline(events).flatMap(item => (item.type === 'subagent' ? [item.sub] : []))
}

describe('a resumed subagent', () => {
  it('is a new card where it was resumed; its rows nest under it; the original stays done', () => {
    const [orig, run] = cards(session())
    expect(orig.key).toBe('A')
    expect(run.key).toBe('S')
    expect(subagentStatus(orig, new Set(), NOW)).toBe('done')
    expect(orig.toolCalls).toBe(1)
    expect(resumedSubagent(orig)).toBe(false)
    expect(resumedSubagent(run)).toBe(true)
    expect(run.call?.call.tool).toBe('SendMessage')
    expect(run.toolCalls).toBe(1)
    expect(subagentTitle(run)).toBe('Implement the data tasks')
    // Its call returned at once and the turn ended: it still runs, until its own end.
    expect(subagentStatus(run, new Set(), NOW)).toBe('running')
    const [, ended] = cards(session({ ended: true }))
    expect(subagentStatus(ended, new Set(), NOW)).toBe('done')
  })

  it('runs in the background when its original did', () => {
    const [orig, run] = cards(session({ background: true }))
    expect(subagentStatus(orig, new Set(), NOW)).toBe('done')
    expect(subagentStatus(run, new Set(), NOW)).toBe('background')
  })

  it('is listed live past the turn and the person\'s message, as resumed', () => {
    const subs = liveSubagents(session(), { inFlight: false, live: true }, NOW)
    expect(subs.map(s => [s.key, s.background, s.resumed])).toEqual([['S', false, true]])
    expect(liveSubagents(session({ ended: true }), { inFlight: true, live: true }, NOW)).toEqual([])
  })

  it('is only what the daemon says: a SendMessage of an older daemon\'s session is an ordinary call', () => {
    seq = 0
    const old = [
      ev('user_message', { text: 'go', source: 'chat' }),
      ev('tool_call', { tool: 'SendMessage', call_id: 'S', input: { to: 'a1b2c3d4e5f6a7b80', message: 'again' } }),
      ev('tool_result', { call_id: 'S', output: ACK, is_error: false, duration_ms: 0 }),
      ev('turn_done', { stop_reason: 'end_turn' }),
    ]
    const items = buildTimeline(old, undefined, { groupTools: false })
    expect(items.filter(i => i.type === 'subagent')).toEqual([])
    expect(items.some(i => i.type === 'card' && i.entry.call.tool === 'SendMessage' && i.entry.result !== undefined)).toBe(true)
    expect(liveSubagents(old, { inFlight: false, live: true }, NOW)).toEqual([])
    // With the run's own lines forwarded (and no start), the card they make is not a running one.
    const withLines = [...old.slice(0, 3), ev('tool_call', { tool: 'Bash', call_id: 's1', input: { command: 'ls' }, parent_call_id: 'S' }), old[3]]
    const run = cards(withLines).find(c => c.key === 'S')!
    expect(resumedSubagent(run)).toBe(false)
    expect(subagentStatus(run, new Set(), NOW)).toBe('done')
    expect(liveSubagents(withLines, { inFlight: false, live: true }, NOW)).toEqual([])
  })

  it('reads stopped in a session that is over with no end recorded', () => {
    const [, run] = cards(session())
    expect(subagentStatus(run, new Set(), NOW, true)).toBe('stopped')
    const [, ended] = cards(session({ ended: true }))
    expect(subagentStatus(ended, new Set(), NOW, true)).toBe('done')
  })

  it('shows a hand-back\'s report without the CLI\'s JSON head', () => {
    const out = '{"success":true,"message":"Resumed agent. Its final report is not in this message."}\nRenamed it.\nThe file is now b.'
    expect(resumeAck(out)).toBe(false)
    expect(handBackReport(out)).toBe('Renamed it.\nThe file is now b.')
    expect(handBackReport('{"note":"a {brace} \\" here"}\nplain')).toBe('{"note":"a {brace} \\" here"}\nplain')
    expect(handBackReport('Renamed it.')).toBe('Renamed it.')
  })

  it('recognises the resume acknowledgement, not a report', () => {
    expect(resumeAck(ACK)).toBe(true)
    expect(resumeAck('Message queued for delivery to a1b2 at its next tool round.')).toBe(true)
    expect(resumeAck('{"success":true,"message":"Resuming agent a1b2c3d"}')).toBe(true)
    expect(resumeAck('Renamed the file; resuming work is up to you.')).toBe(false)
  })
})

describe('subagentWaitingOn', () => {
  const sub = (key: string, title: string, startedAt: number | null, background = false): SubagentLive =>
    ({ key, title, type: '', model: '', tokens: null, doing: '', startedAt, background })

  it('names the oldest foreground subagent, its title cut to 40 characters', () => {
    expect(subagentWaitingOn([])).toBeNull()
    expect(subagentWaitingOn([sub('b', 'Watch', 1, true)])).toBeNull()
    expect(subagentWaitingOn([sub('x', 'No start', null), sub('a', 'Fix it', 20), sub('b', 'Older', 10, true), sub('c', 'Oldest foreground', 15)]))
      .toEqual({ tool: 'Oldest foreground', startedAt: 15 })
    const long = 'Implement tasks one through five of the data phase'
    const got = subagentWaitingOn([sub('a', long, 5)])!
    expect(Array.from(got.tool)).toHaveLength(40)
    expect(got.tool).toBe(long.slice(0, 39) + '…')
  })

  it('names none whose start is not held: the edge of a history still loading', () => {
    // A subagent's steps from a page whose start (its call, its subagent_started, its launch into
    // the background) is on an older page not loaded yet: it reads as running, with no start.
    seq = 0
    const events = [
      ev('tool_call', { tool: 'Bash', call_id: 'k1', input: { command: 'make' }, parent_call_id: 'K' }),
      ev('tool_result', { call_id: 'k1', output: 'ok', is_error: false, duration_ms: 1, parent_call_id: 'K' }),
    ]
    const live = liveSubagents(events, { inFlight: true, live: true }, NOW)
    expect(live.map(s => [s.key, s.background, s.startedAt])).toEqual([['K', false, null]])
    expect(subagentWaitingOn(live)).toBeNull()
  })
})
