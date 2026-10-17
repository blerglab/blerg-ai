import { describe, expect, it } from 'vitest'
import { buildTimeline, sharedTimeline, timelineBuilder, type TimelineItem } from './toolGroups'
import { ev as mk } from '../test/chat'
import type { AgentEvent } from '../types'

let seq = 0
function ev(kind: string, payload: unknown): AgentEvent {
  seq++
  return mk({ client_event_id: `t${seq}`, seq, kind, payload })
}
const call = (id: string) => ev('tool_call', { tool: 'Bash', call_id: id, input: { command: `echo ${id}` } })
const result = (id: string) => ev('tool_result', { call_id: id, output: 'ok' })
const groups = (items: TimelineItem[]) => items.filter(i => i.type === 'tools')

describe('buildTimeline keeps an unchanged group as it was', () => {
  it('returns the same entries array for a group that did not change across two calls', () => {
    const events = [call('a'), result('a'), call('b'), result('b'), ev('assistant_text', { text: 'hi', done: true })]
    const first = buildTimeline(events)
    // A live event after the group: the group itself is unchanged.
    const second = buildTimeline([...events, ev('user_message', { text: 'next' })], first)
    const [g1] = groups(first)
    const [g2] = groups(second)
    if (g1.type !== 'tools' || g2.type !== 'tools') throw new Error('expected groups')
    expect(g2.entries).toBe(g1.entries)
  })

  it('gives a group a new entries array when a call joins it or a result arrives', () => {
    const base = [call('a'), result('a'), call('b')]
    const first = buildTimeline(base)
    const withResult = buildTimeline([...base, result('b')], first)
    const g1 = groups(first)[0]
    const g2 = groups(withResult)[0]
    if (g1.type !== 'tools' || g2.type !== 'tools') throw new Error('expected groups')
    expect(g2.entries).not.toBe(g1.entries)
    expect(g2.entries[1].result).toBeDefined()
    const withCall = buildTimeline([...base, result('b'), call('c')], withResult)
    const g3 = groups(withCall)[0]
    if (g3.type !== 'tools') throw new Error('expected a group')
    expect(g3.entries).not.toBe(g2.entries)
    expect(g3.entries).toHaveLength(3)
  })

  it('a builder remembers its last timeline', () => {
    const build = timelineBuilder()
    const events = [call('a'), result('a')]
    const first = build(events)
    const second = build([...events, ev('user_message', { text: 'later' })])
    const g1 = groups(first)[0]
    const g2 = groups(second)[0]
    if (g1.type !== 'tools' || g2.type !== 'tools') throw new Error('expected groups')
    expect(g2.entries).toBe(g1.entries)
  })
})

// ─── subagents ──────────────────────────────────────────────────────────────

const agentCall = (id: string) => ev('tool_call', { tool: 'Agent', call_id: id, input: { description: 'Survey', prompt: 'Look around' } })
const started = (id: string) => ev('subagent_started', { child_id: id, agent_type: 'Explore', model: '', summary: 'Survey' })
const done = (id: string, summary = 'found it') => ev('subagent_done', { child_id: id, agent_type: 'Explore', model: '', summary })
const childCall = (id: string, parent: string) => ev('tool_call', { tool: 'Bash', call_id: id, input: { command: 'ls' }, parent_call_id: parent })
const childResult = (id: string, parent: string) => ev('tool_result', { call_id: id, output: 'ok', parent_call_id: parent })
const childText = (text: string, parent: string) => ev('assistant_text', { text, done: true, parent_call_id: parent })
const thinking = (text: string, parent?: string) => ev('assistant_thinking', parent ? { text, parent_call_id: parent } : { text })
const agentResult = (id: string) => ev('tool_result', { call_id: id, output: 'the report' })

describe('buildTimeline nests a subagent', () => {
  it('folds the Agent call, its start and end and every child event into one item', () => {
    const events = [
      ev('user_message', { text: 'go', source: 'chat' }),
      agentCall('A'), started('A'),
      thinking('sub thinks', 'A'), childText('sub says', 'A'), childCall('s1', 'A'), childResult('s1', 'A'),
      done('A'), agentResult('A'),
      ev('assistant_text', { text: 'all done', done: true }),
    ]
    const items = buildTimeline(events)
    expect(items.map(i => i.type)).toEqual(['event', 'subagent', 'event'])
    const sub = items[1]
    if (sub.type !== 'subagent') throw new Error('expected a subagent')
    expect(sub.key).toBe('A')
    expect(sub.sub.anchor).toBe(events[1])
    expect(sub.sub.call?.result?.output).toBe('the report')
    expect(sub.sub.done).toBe(events[7])
    expect(sub.sub.toolCalls).toBe(1)
    expect(sub.sub.messages).toBe(1)
    expect(sub.sub.children.map(i => i.type)).toEqual(['event', 'event', 'tools'])
    const g = sub.sub.children[2]
    if (g.type !== 'tools') throw new Error('expected a group')
    expect(g.entries[0].result?.output).toBe('ok')
    // the anchor first, then the start, the end and the children (not the Agent call's result)
    expect(sub.sub.ids[0]).toBe(events[1].client_event_id)
    expect(sub.sub.ids).toHaveLength(7)
  })

  it('nests events whose start is not held in a placeholder card, filled in once the start arrives', () => {
    const late = [childCall('s1', 'far'), childResult('s1', 'far'), childText('a report', 'far')]
    const build = timelineBuilder()
    const first = build(late)
    expect(first.map(i => i.type)).toEqual(['subagent'])
    const p = first[0]
    if (p.type !== 'subagent') throw new Error('expected a subagent')
    expect(p.sub.started).toBeUndefined()
    expect(p.sub.anchor).toBe(late[0])
    expect(p.sub.toolCalls).toBe(1)
    // An older page brings the Agent call and its start, in front.
    const call = ev('tool_call', { tool: 'Agent', call_id: 'far', input: { description: 'Far' } })
    const start = started('far')
    const second = build([call, start, ...late])
    expect(second.map(i => i.type)).toEqual(['subagent'])
    const f = second[0]
    if (f.type !== 'subagent') throw new Error('expected a subagent')
    expect(f.sub.started).toBe(start)
    expect(f.sub.call?.ev).toBe(call)
    expect(f.sub.anchor).toBe(call)
    expect(f.sub.ids).toHaveLength(5)
  })

  it('the latest subagent_done wins, and a background launch is running until its real end', () => {
    const launch = ev('subagent_done', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'running in the background', background: true })
    const base = [agentCall('A'), started('A'), launch, agentResult('A'), childCall('s1', 'A')]
    const one = buildTimeline(base)[0]
    if (one.type !== 'subagent') throw new Error('expected a subagent')
    expect(one.sub.done).toBe(launch)
    expect(one.sub.background).toBe(true)
    expect(one.sub.launched).toBe(true)
    const end = done('A', 'The build is green.')
    const two = buildTimeline([...base, end])[0]
    if (two.type !== 'subagent') throw new Error('expected a subagent')
    expect(two.sub.done).toBe(end)
    expect(two.sub.background).toBe(false)
    expect(two.sub.launched).toBe(true)
  })

  it('folds subagent_update into its card and keeps the latest', () => {
    const u1 = ev('subagent_update', { child_id: 'A', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 } })
    const u2 = ev('subagent_update', { child_id: 'A', model: 'm', usage: { input_tokens: 5, output_tokens: 5, cache_read_tokens: 0, cache_write_tokens: 0 } })
    const items = buildTimeline([agentCall('A'), started('A'), u1, childCall('s1', 'A'), u2])
    expect(items.map(i => i.type)).toEqual(['subagent'])
    const sub = items[0]
    if (sub.type !== 'subagent') throw new Error('expected a subagent')
    expect(sub.sub.update).toBe(u2)
    expect(sub.sub.latest?.kind).toBe('tool_call')
  })

  it('draws a session with no subagent exactly as before', () => {
    const events = [call('a'), result('a'), thinking('hmm'), call('b'), result('b')]
    const items = buildTimeline(events)
    expect(items.map(i => i.type)).toEqual(['tools', 'event', 'tools'])
  })

  it('a native subagent (no Agent call) is one card at its start, holding its end', () => {
    const events = [started('n1'), done('n1')]
    const items = buildTimeline(events)
    expect(items).toHaveLength(1)
    const sub = items[0]
    if (sub.type !== 'subagent') throw new Error('expected a subagent')
    expect(sub.sub.anchor).toBe(events[0])
    expect(sub.sub.call).toBeUndefined()
    expect(sub.sub.done).toBe(events[1])
  })

  it('nests the events of a subagent the subagent started under the same card', () => {
    const events = [agentCall('A'), started('A'), ev('tool_call', { tool: 'Agent', call_id: 'B', input: {}, parent_call_id: 'A' }), childCall('s9', 'B')]
    const items = buildTimeline(events)
    expect(items.map(i => i.type)).toEqual(['subagent'])
    const sub = items[0]
    if (sub.type !== 'subagent') throw new Error('expected a subagent')
    expect(sub.sub.toolCalls).toBe(2)
  })

  it('keeps an unchanged subagent entry, and makes a new one when a child arrives', () => {
    const base = [agentCall('A'), started('A'), childCall('s1', 'A'), childResult('s1', 'A')]
    const build = timelineBuilder()
    const first = build(base)
    const second = build([...base, ev('user_message', { text: 'later', source: 'chat' })])
    const s1 = first[0]
    const s2 = second[0]
    if (s1.type !== 'subagent' || s2.type !== 'subagent') throw new Error('expected subagents')
    expect(s2.sub).toBe(s1.sub)
    const third = build([...base, childText('more', 'A')])
    const s3 = third[0]
    if (s3.type !== 'subagent') throw new Error('expected a subagent')
    expect(s3.sub).not.toBe(s1.sub)
    expect(s3.sub.messages).toBe(1)
  })

  it('without grouping, every call (a child one too) is a card of its own', () => {
    const events = [call('a'), result('a'), call('b'), agentCall('A'), started('A'), childCall('s1', 'A'), childCall('s2', 'A')]
    const items = buildTimeline(events, undefined, { groupTools: false })
    expect(items.map(i => i.type)).toEqual(['card', 'card', 'subagent'])
    const sub = items[2]
    if (sub.type !== 'subagent') throw new Error('expected a subagent')
    expect(sub.sub.children.map(i => i.type)).toEqual(['card', 'card'])
  })
})

describe('sharedTimeline', () => {
  it('builds once per events array and session, and from the session\'s previous timeline', () => {
    const events = [call('a'), result('a'), call('b'), result('b')]
    const first = sharedTimeline('s-shared', events)
    expect(sharedTimeline('s-shared', events)).toBe(first)
    // Another session or grouping has its own.
    expect(sharedTimeline('s-other', events)).not.toBe(first)
    expect(sharedTimeline('s-shared', events, { groupTools: false })).not.toBe(first)
    const next = sharedTimeline('s-shared', [...events, ev('assistant_text', { text: 'hi', done: true })])
    const [g1] = groups(first)
    const [g2] = groups(next)
    if (g1.type !== 'tools' || g2.type !== 'tools') throw new Error('expected groups')
    expect(g2.entries).toBe(g1.entries)
  })
})


it('retains child configured effort across same-model partial updates and clears on reroute', () => {
  const events = [ev('subagent_started', { child_id: 'effort', model: 'a', effort: 'low' }),
    ev('subagent_update', { child_id: 'effort', model: 'a', effort: 'high' }),
    ev('subagent_update', { child_id: 'effort', model: 'a' })]
  const child = buildTimeline(events).find(i => i.type === 'subagent')
  expect(child?.type === 'subagent' && child.sub.effort).toBe('high')
  const rerouted = buildTimeline([...events, ev('subagent_update', { child_id: 'effort', model: 'b' })]).find(i => i.type === 'subagent')
  expect(rerouted?.type === 'subagent' && rerouted.sub.effort).toBe('')
})

it('clears known configured effort on an explicit null observation', () => {
  const items = buildTimeline([ev('subagent_started', { child_id: 'effort', model: 'a', effort: 'high' }),
    ev('subagent_update', { child_id: 'effort', model: 'a', effort: null })])
  const child = items.find(i => i.type === 'subagent')
  expect(child?.type === 'subagent' && child.sub.effort).toBe('')
})
