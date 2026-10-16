import { describe, expect, it } from 'vitest'
import type { AgentEvent } from '@blerglab/chat'
import { foldLive, type LiveReply } from './liveState'

const T0 = Date.parse('2026-10-15T10:00:00Z')
const iso = (min: number) => new Date(T0 + min * 60_000).toISOString()
const HOUR = 60 * 60_000

const reply = (over: Partial<LiveReply> = {}): LiveReply => ({
  session_id: 's1', through_seq: 10, generated_at: iso(0), tokens_hidden: false,
  asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 }, ...over,
})

let n = 0
const ev = (kind: string, payload: unknown, seq?: number, min = 1): AgentEvent =>
  ({ type: 'agent_event', session_id: 's1', client_event_id: `e${++n}`, seq, ts: iso(min), kind, payload } as AgentEvent)

const sub = (key: string, over = {}) => ({
  key, title: 'Implement', type: 'general-purpose', model: 'sonnet', tokens: 10, doing: 'running Bash · npm test',
  started_at: iso(0), active_at: iso(0), background: false, seq: 3, ...over,
})

describe('foldLive', () => {
  it('draws the reply as it is when nothing came after it', () => {
    const r = reply({
      asks: [{ event_id: 'a1', message_id: 'm1', question: 'Ship it?', asked_at: iso(0), seq: 2 }],
      subagents: [sub('k1')],
      tasks: [{ task_id: 't1', call_id: 'c1', kind: 'bash', description: 'npm run dev', started_at: iso(0), seq: 4 }],
      tool_changes: 2,
    })
    // Events the reply already covers are not folded again.
    const v = foldLive(r, [ev('subagent_done', { child_id: 'k1' }, 9), ev('ask', { body: 'old', message_id: 'm0' }, 10)], T0)
    expect(v.asks).toEqual([{ eventId: 'a1', messageId: 'm1', question: 'Ship it?', askedAt: T0 }])
    expect(v.subagents).toEqual([{ key: 'k1', title: 'Implement', type: 'general-purpose', model: 'sonnet', tokens: 10, doing: 'running Bash · npm test', startedAt: T0, background: false }])
    expect(v.tasks).toEqual([{ taskId: 't1', callId: 'c1', kind: 'bash', description: 'npm run dev', startedAt: T0, done: null }])
    expect(v.counts).toEqual({ needsYou: 3, running: 2 })
  })

  it('folds a subagent start, its updates, its background launch and its end', () => {
    const start = ev('subagent_started', { child_id: 'k2', agent_type: 'Explore', model: 'haiku', summary: '\nFind the bug\nmore' }, 11, 2)
    let v = foldLive(reply(), [start], T0)
    expect(v.subagents).toEqual([{ key: 'k2', title: 'Find the bug', type: 'Explore', model: 'haiku', tokens: null, doing: '', startedAt: T0 + 2 * 60_000, background: false }])
    const update = ev('subagent_update', { child_id: 'k2', model: 'claude-haiku', usage: { input_tokens: 5, output_tokens: 7 } }, 12)
    const launch = ev('subagent_done', { child_id: 'k2', background: true }, 13)
    const task = ev('background_task_started', { task_id: 'a5', call_id: 'k2', kind: 'agent' }, 14)
    v = foldLive(reply(), [start, update, launch, task], T0)
    expect(v.subagents[0]).toMatchObject({ model: 'claude-haiku', tokens: 12, background: true })
    // Its background task is the same work: counted once, as the subagent.
    expect(v.tasks).toEqual([])
    expect(v.counts.running).toBe(1)
    v = foldLive(reply(), [start, update, launch, task, ev('subagent_done', { child_id: 'k2', summary: 'ok' }, 15)], T0)
    expect(v.subagents).toEqual([])
  })

  it('a message that is not the system\'s ends a foreground subagent, not a background one', () => {
    const r = reply({ subagents: [sub('k1'), sub('k2', { background: true })] })
    expect(foldLive(r, [ev('user_message', { source: 'system', text: 'x' }, 11)], T0).subagents.map(s => s.key)).toEqual(['k1', 'k2'])
    expect(foldLive(r, [ev('user_message', { source: 'chat', text: 'also the docs' }, 11)], T0).subagents.map(s => s.key)).toEqual(['k2'])
  })

  it('a turn\'s end ends a foreground subagent, not a background one', () => {
    const r = reply({ subagents: [sub('k1'), sub('k2', { background: true })] })
    expect(foldLive(r, [ev('turn_done', {}, 11)], T0).subagents.map(s => s.key)).toEqual(['k2'])
  })

  it('a resumed subagent runs on past the person\'s message and the turn\'s end, until its own end', () => {
    const r = reply({ subagents: [sub('S1', { resumed: true })] })
    const start = ev('subagent_started', { child_id: 'S2', agent_type: 'Explore', model: '', summary: 'Watch', background: true, resumed: true, resumed_from: 'A2' }, 11)
    const after = [start, ev('user_message', { source: 'chat', text: 'also the docs' }, 12), ev('turn_done', {}, 13)]
    let v = foldLive(r, after, T0)
    expect(v.subagents.map(s => [s.key, s.background, s.resumed])).toEqual([['S1', false, true], ['S2', true, true]])
    v = foldLive(r, [...after, ev('subagent_done', { child_id: 'S1', summary: 'fixed', resumed: true }, 14)], T0)
    expect(v.subagents.map(s => s.key)).toEqual(['S2'])
  })

  it('hides tokens when the reply does', () => {
    const v = foldLive(reply({ tokens_hidden: true, subagents: [sub('k1', { tokens: null })] }),
      [ev('subagent_update', { child_id: 'k1', usage: { output_tokens: 9 } }, 11)], T0)
    expect(v.subagents[0].tokens).toBeNull()
  })

  it('folds a background task start and end, a live event with no seq yet included', () => {
    const r = reply({ tasks: [{ task_id: 't1', call_id: 'c1', kind: 'bash', description: '', started_at: iso(0), seq: 4 }] })
    const v = foldLive(r, [
      ev('background_task_done', { task_id: 't1', status: 'completed' }, 11),
      ev('background_task_started', { task_id: 't2', call_id: 'c2', kind: 'monitor', description: 'watch', started_at: iso(5) }),
    ], T0)
    expect(v.tasks.map(t => [t.taskId, t.startedAt])).toEqual([['t2', T0 + 5 * 60_000]])
  })

  it('folds an ask and its answer', () => {
    const r = reply({ asks: [{ event_id: 'a1', message_id: 'm1', question: 'Ship it?', asked_at: iso(0), seq: 2 }] })
    let v = foldLive(r, [ev('ask', { body: 'Which branch?', message_id: 'm2' }, 11)], T0)
    expect(v.asks.map(a => a.messageId)).toEqual(['m1', 'm2'])
    v = foldLive(r, [ev('ask', { body: 'Which branch?', message_id: 'm2' }, 11), ev('user_message', { source: 'ask_answer', message_id: 'm1', text: 'yes' }, 12)], T0)
    expect(v.asks.map(a => a.messageId)).toEqual(['m2'])
    // Without a message id, the answer closes the newest.
    v = foldLive(r, [ev('ask', { body: 'Which branch?', message_id: 'm2' }, 11), ev('user_message', { source: 'ask_answer', text: 'main' }, 12)], T0)
    expect(v.asks.map(a => a.messageId)).toEqual(['m1'])
    // A person's message closes only an ask with no message id; a system line closes none.
    v = foldLive(r, [ev('ask', { body: 'Old style?' }, 11), ev('user_message', { source: 'system', text: 'x' }, 12)], T0)
    expect(v.asks).toHaveLength(2)
    v = foldLive(r, [ev('ask', { body: 'Old style?' }, 11), ev('user_message', { source: 'chat', text: 'yes' }, 12)], T0)
    expect(v.asks.map(a => a.messageId)).toEqual(['m1'])
  })

  it('leaves out what has had no sign of life for 12 h, on its own clock', () => {
    const r = reply({
      subagents: [sub('k1', { background: true })],
      tasks: [{ task_id: 't1', call_id: 'c1', kind: 'bash', description: '', started_at: iso(0), seq: 4 }],
    })
    expect(foldLive(r, [], T0 + 11 * HOUR).counts.running).toBe(2)
    expect(foldLive(r, [], T0 + 13 * HOUR).counts.running).toBe(0)
    // An update is a sign of life.
    expect(foldLive(r, [ev('subagent_update', { child_id: 'k1' }, 11, 10 * 60)], T0 + 13 * HOUR).subagents).toHaveLength(1)
  })
})
