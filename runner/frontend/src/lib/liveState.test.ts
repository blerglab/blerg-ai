import { describe, expect, it } from 'vitest'
import type { AgentEvent } from '@blerglab/chat'
import { foldLive, isLiveTrigger, type LiveReply } from './liveState'

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
    expect(v.subagents).toEqual([{ key: 'k1', childId: 'k1', title: 'Implement', type: 'general-purpose', model: 'sonnet', tokens: 10, doing: 'running Bash · npm test', startedAt: T0, background: false }])
    expect(v.tasks).toEqual([{ taskId: 't1', callId: 'c1', kind: 'bash', description: 'npm run dev', command: '', startedAt: T0, done: null }])
    expect(v.counts).toEqual({ needsYou: 3 })
  })

  it('folds a subagent start, its updates, its background launch and its end', () => {
    const start = ev('subagent_started', { child_id: 'k2', agent_type: 'Explore', model: 'haiku', summary: '\nFind the bug\nmore' }, 11, 2)
    let v = foldLive(reply(), [start], T0)
    expect(v.subagents).toEqual([{ key: 'k2', childId: 'k2', title: 'Find the bug', type: 'Explore', model: 'haiku', tokens: null, doing: '', startedAt: T0 + 2 * 60_000, background: false }])
    const update = ev('subagent_update', { child_id: 'k2', model: 'claude-haiku', usage: { input_tokens: 5, output_tokens: 7 } }, 12)
    const launch = ev('subagent_done', { child_id: 'k2', background: true }, 13)
    const task = ev('background_task_started', { task_id: 'a5', call_id: 'k2', kind: 'agent' }, 14)
    v = foldLive(reply(), [start, update, launch, task], T0)
    expect(v.subagents[0]).toMatchObject({ model: 'claude-haiku', tokens: 12, background: true })
    // Its background task is the same work: counted once, as the subagent.
    expect(v.tasks).toEqual([])
    expect(v.subagents).toHaveLength(1)
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
    const live = (v: ReturnType<typeof foldLive>) => v.subagents.length + v.tasks.length
    expect(live(foldLive(r, [], T0 + 11 * HOUR))).toBe(2)
    expect(live(foldLive(r, [], T0 + 13 * HOUR))).toBe(0)
    // An update is a sign of life.
    expect(foldLive(r, [ev('subagent_update', { child_id: 'k1' }, 11, 10 * 60)], T0 + 13 * HOUR).subagents).toHaveLength(1)
  })
})


it('folds child effort independently and clears stale effort on a model-only reroute', () => {
  const start = ev('subagent_started', { child_id: 'effort', model: 'child', effort: 'low' }, 11)
  const update = ev('subagent_update', { child_id: 'effort', effort: 'high' }, 12)
  expect(foldLive(reply(), [start, update], T0).subagents[0].effort).toBe('high')
  const partial = ev('subagent_update', { child_id: 'effort', model: 'child' }, 13)
  expect(foldLive(reply(), [start, update, partial], T0).subagents[0].effort).toBe('high')
  const unset = ev('subagent_update', { child_id: 'effort', effort: null }, 13)
  expect(foldLive(reply(), [start, update, unset], T0).subagents[0].effort).toBe('')
  const reset = ev('subagent_update', { child_id: 'effort', effort: '' }, 13)
  expect(foldLive(reply(), [start, update, reset], T0).subagents[0].effort).toBe('')
  const reroute = ev('subagent_update', { child_id: 'effort', model: 'other-child' }, 13)
  expect(foldLive(reply(), [start, update, reroute], T0).subagents[0].effort).toBe('')
})

describe('foldLive: the checklist, the plan and the pins', () => {
  const items = (...xs: [string, boolean][]) => xs.map(([text, done]) => ({ text, done }))
  it('reads them from the reply', () => {
    const v = foldLive(reply({
      checklist: { items: items(['a', true], ['b', false]), event_id: 'c1', updated_at: iso(0) },
      plan: { items: [{ text: 'x', status: 'in_progress', active: 'Doing x' }], updated_at: iso(0) },
      tickets: [{ source: 'jira', key: 'A-1', title: 'T', status: 'Open', url: 'https://x.example/A-1', event_id: 't1', seq: 3, updated_at: iso(0) }],
      ticket: { source: 'jira', key: 'A-1', title: 'T', status: 'Open', description: 'd', event_id: 't1', updated_at: iso(0) },
    }), [], T0)
    expect(v.checklist).toEqual({ items: [{id:'legacy-c1-1',text:'a',done:true},{id:'legacy-c1-2',text:'b',done:false}], eventId: 'c1', updatedAt: T0 })
    expect(v.plan?.items[0].active).toBe('Doing x')
    expect(v.tickets.map(t => t.key)).toEqual(['A-1'])
    expect(v.ticket).toMatchObject({ key: 'A-1', description: 'd', eventId: 't1', updatedAt: T0 })
  })

  it('a reply without them (an older server) has none', () => {
    const v = foldLive(reply(), [], T0)
    expect([v.checklist, v.plan, v.tickets, v.ticket]).toEqual([null, null, [], null])
  })

  it('folds the events after the reply: the latest checklist wins, pins and unpins move the list', () => {
    const v = foldLive(reply({
      checklist: { items: items(['a', false]), event_id: 'c1', updated_at: iso(0) },
      tickets: [{ source: 'jira', key: 'A-1', title: 'T', status: 'Open', url: '', event_id: 't1', seq: 3, updated_at: iso(0) }],
      ticket: { source: 'jira', key: 'A-1', title: 'T', status: 'Open', event_id: 't1', updated_at: iso(0) },
    }), [
      ev('checklist', { items: items(['a', true]), op: 'tick', changed: { index: 1, text: 'a', done: true } }, 11),
      ev('ticket', { action: 'pin', ticket: { source: 'github', key: '9', title: 'Nine' } }, 12),
      ev('ticket', { action: 'unpin', ticket: { source: 'jira', key: 'A-1', title: 'T' } }, 13),
    ], T0)
    expect(v.checklist?.items[0].done).toBe(true)
    expect(v.tickets.map(t => t.key)).toEqual(['9'])
    expect(v.ticket?.key).toBe('9')
    expect(foldLive(reply({ checklist: { items: items(['a', false]), event_id: 'c1', updated_at: iso(0) } }), [ev('checklist', { items: [], op: 'clear' }, 11)], T0).checklist).toBeNull()
  })

  it('a checklist, a pin and a main-agent task tool call bring a new read; a subagent\'s does not', () => {
    expect(isLiveTrigger(ev('checklist', {}))).toBe(true)
    expect(isLiveTrigger(ev('ticket', {}))).toBe(true)
    expect(isLiveTrigger(ev('tool_call', { tool: 'TaskUpdate', call_id: 'u', input: {} }))).toBe(true)
    expect(isLiveTrigger(ev('tool_call', { tool: 'TaskUpdate', call_id: 'u', input: {}, parent_call_id: 'A' }))).toBe(true)
    expect(isLiveTrigger(ev('tool_call', { tool: 'Bash', call_id: 'b', input: {} }))).toBe(false)
  })
})

describe('foldLive: MCP and checklist commits', () => {
  const mcp = (status: string, settled: boolean, extra: object = {}) => ({
    servers: [{ name: 'jira', status, source: 'user', tools: 3 }], settled, since: iso(0), engine: 'claude', ...extra,
  })

  it('reads the reply\'s snapshot, hold and the owner\'s right to release', () => {
    const v = foldLive(reply({ mcp: mcp('pending', false), mcp_hold: { since: iso(0), until: iso(0.5) }, can_release_prompt: true }), [], T0)
    expect(v.mcp?.servers[0].status).toBe('pending')
    expect(v.mcpHold).toEqual({ since: T0, until: T0 + 30_000 })
    expect(v.canReleasePrompt).toBe(true)
    const none = foldLive(reply(), [], T0)
    expect(none.mcp).toBeNull()
    expect(none.mcpHold).toBeNull()
    expect(none.canReleasePrompt).toBe(false)
  })

  it('a later mcp_status replaces the snapshot and ends the hold when it settles', () => {
    const r = reply({ mcp: mcp('pending', false), mcp_hold: { since: iso(0), until: iso(0.5) } })
    const held = ev('mcp_status', mcp('pending', false, { hold: { since: iso(0), until: iso(0.5) } }), 11)
    expect(foldLive(r, [held], T0).mcpHold).not.toBeNull()
    const done = ev('mcp_status', mcp('connected', true, { released: 'settled' }), 12)
    const v = foldLive(r, [held, done], T0)
    expect(v.mcp?.servers[0].status).toBe('connected')
    expect(v.mcp?.released).toBe('settled')
    expect(v.mcpHold).toBeNull()
  })

  it('mcp_status is a live trigger', () => {
    expect(isLiveTrigger(ev('mcp_status', mcp('connected', true)))).toBe(true)
  })

  it('carries the checklist\'s commits and their pages through a fold', () => {
    const r = reply({ checklist: { items: [{ text: 'a', done: true, commits: ['abcdef1'] }], event_id: 'c1', updated_at: iso(0), commit_urls: { abcdef1: 'https://forge.example.test/c/abcdef1' } } })
    expect(foldLive(r, [], T0).checklist?.commitUrls).toEqual({ abcdef1: 'https://forge.example.test/c/abcdef1' })
    const next = ev('checklist', { items: [{ text: 'a', done: true, commits: ['abcdef1', '1234567'] }], op: 'tick' }, 11)
    const c = foldLive(r, [next], T0).checklist
    expect(c?.items[0].commits).toEqual(['abcdef1', '1234567'])
    // A commit the reply knew keeps its page; a newer one has none until the next read.
    expect(c?.commitUrls).toEqual({ abcdef1: 'https://forge.example.test/c/abcdef1' })
  })
})


describe('live progress', () => {
  const progress = { completed: 2, total: 5, label: 'Run tests', updated_at: iso(0) }
  it('keeps root and child progress separate', () => {
    const view = foldLive(reply({ progress, subagents: [sub('child', { progress: { ...progress, completed: 1, label: 'Audit' } })] }), [], T0)
    expect(view.progress).toEqual(progress)
    expect(view.subagents[0].progress).toEqual({ ...progress, completed: 1, label: 'Audit' })
  })
  it.each([
    { completed: -1, total: 5, label: 'bad' }, { completed: 6, total: 5, label: 'bad' },
    { completed: 0, total: 0, label: 'bad' }, { completed: 0.5, total: 5, label: 'bad' },
    { completed: 1, total: Number.MAX_SAFE_INTEGER + 1, label: 'bad' },
  ])('hides malformed progress: %j', bad => {
    expect(foldLive(reply({ progress: bad }), [], T0).progress).toBeNull()
  })
  it('refreshes explicit/native root and child plans and Claude task completion', () => {
    for (const kind of ['plan_updated', 'progress_updated']) expect(isLiveTrigger(ev(kind, {}))).toBe(true)
    expect(isLiveTrigger(ev('tool_call', { tool: 'TodoWrite', parent_call_id: 'child' }))).toBe(true)
    expect(isLiveTrigger(ev('tool_result', { call_id: 'create-task' }))).toBe(true)
  })
})

it('counts pending child requests as needing you and keeps every child session', () => {
  const child = { id: 'child', title: 'Review', engine: 'codex', model: 'fixture', effort: 'high', status: 'running', started_at: iso(0), ended_at: null, asking: true, tokens: 123 }
  const out = foldLive(reply({ children: [child, { ...child, id: 'done', status: 'ended', ended_at: iso(1) }], delegation_requests: [{ request_id: 'request', engine: 'codex', title: 'Other review', expires_at: iso(10) }], delegation_grants: [{ id: 'grant', engine: 'codex', runtime: 'docker' }] }), [], T0)
  expect(out.counts).toEqual({ needsYou: 1 })
  expect(out.children).toHaveLength(2)
  expect(out.delegationGrants?.[0].id).toBe('grant')
})

it('keeps explicit canonical child identity and checklist IDs through native and reply folds',()=>{
 const v=foldLive(reply({subagents:[sub('call',{child_id:'native'})],checklist:{items:[{id:'stable',text:'Work',done:false,in_progress:true,assignees:[{kind:'subagent',id:'native'}]}],event_id:'event',updated_at:iso(0)}}),[],T0)
 expect(v.subagents[0].childId).toBe('native');expect(v.checklist?.items[0]).toMatchObject({id:'stable',in_progress:true,assignees:[{kind:'subagent',id:'native'}]})
 const malformed=foldLive(reply({checklist:{items:[{text:'Old',done:false}],event_id:'not/an/identity',updated_at:iso(0)}}),[],T0)
 expect(malformed.checklist?.items[0].id).toBeUndefined()
})

it('does not attach an old snapshot alias to a newly started worker before authoritative refresh',()=>{
 const r=reply({checklist:{items:[{id:'work',text:'Original work',done:false,assignees:[{kind:'subagent',id:'collision'}]}],event_id:'c1',updated_at:iso(0)}})
 const started=ev('subagent_started',{child_id:'collision',call_id:'other-call',summary:'Different worker'},11)
 const v=foldLive(r,[started],T0)
 expect(v.checklist?.items[0].assignees).toBeUndefined()
 const updated=ev('checklist',{op:'assign',items:[{id:'work',text:'Explicit new work',done:false,assignees:[{kind:'subagent',id:'collision'}]}]},12)
 expect(foldLive(r,[started,updated],T0).checklist?.items[0].assignees).toEqual([{kind:'subagent',id:'collision'}])
 expect(foldLive({...r,through_seq:11},[started],T0).checklist?.items[0].assignees).toEqual([{kind:'subagent',id:'collision'}])
})

it('keeps a reaffirmed snapshot worker assignment and promotes an explicitly linked call-only worker',()=>{
 const checklist={items:[{id:'work',text:'Work',done:false,assignees:[{kind:'subagent' as const,id:'native'}]}],event_id:'c1',updated_at:iso(0)}
 const duplicate=foldLive(reply({subagents:[sub('native')],checklist}),[ev('subagent_started',{child_id:'native',summary:'Worker'},11)],T0)
 expect(duplicate.checklist?.items[0].assignees).toEqual([{kind:'subagent',id:'native'}])
 const after=[ev('subagent_started',{call_id:'call-only',summary:'Original worker'},11),ev('checklist',{op:'assign',items:[{id:'work',text:'Current assignment',done:false,assignees:[{kind:'subagent',id:'call-only'}]}]},12),ev('subagent_update',{child_id:'native',call_id:'call-only',model:'reported-model'},13)]
 const promoted=foldLive(reply(),after,T0)
 expect(promoted.subagents).toHaveLength(1)
 expect(promoted.subagents[0]).toMatchObject({key:'native',childId:'native',title:'Original worker',model:'reported-model'})
 expect(promoted.checklist?.items[0].assignees).toEqual([{kind:'subagent',id:'native'}])
 expect(foldLive(reply(),[...after,ev('subagent_done',{child_id:'native',call_id:'call-only'},14)],T0).subagents).toEqual([])
 expect(foldLive(reply(),[...after,ev('subagent_done',{call_id:'call-only'},14)],T0).subagents).toEqual([])

})

it('carries the names of the checklist\'s workers through a fold', () => {
  const titles = { 'subagent:native-a': 'Audit the plan tab' }
  const r = reply({ checklist: { items: [{ text: 'a', done: false, assignees: [{ kind: 'subagent', id: 'native-a' }] }], event_id: 'c1', updated_at: iso(0), worker_titles: titles } })
  expect(foldLive(r, [], T0).checklist?.workerTitles).toEqual(titles)
  const next = ev('checklist', { items: [{ text: 'a', done: true }], op: 'tick' }, 11)
  expect(foldLive(r, [next], T0).checklist?.workerTitles).toEqual(titles)
})
