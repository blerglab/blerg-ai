import { describe, expect, it } from 'vitest'
import type { AgentEvent } from '../types'
import {
  safeChecklistAssignments, checklistAssignment, checklistFromEvents, checklistRowText, commitsText, pinnedFromEvents, planLabel, priorityGlyph, readChecklist, readTicket,
  readTicketEvent, sourceLabel, statusTone, ticketChanges, ticketHref, ticketMeta,
} from './worklist'

let n = 0
function ev(kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  n++
  return { type: 'agent_event', session_id: 's1', client_event_id: `e${n}`, seq: n, ts: '2026-10-16T09:00:00Z', kind, payload }
}
const items = (...xs: [string, boolean][]) => xs.map(([text, done]) => ({ text, done }))

describe('checklist rows', () => {
  it('says the count and the change the server recorded', () => {
    const p = { items: items(['Read', true], ['Write tests', true], ['Ship', false]), op: 'tick', changed: { index: 2, text: 'Write tests', done: true } }
    expect(checklistRowText(p)).toBe('Checklist · 2 of 3 done · ticked “Write tests”')
    expect(checklistRowText({ ...p, op: 'untick', changed: { index: 2, text: 'Write tests', done: false } })).toContain('unticked “Write tests”')
    expect(checklistRowText({ items: items(['a', false]), op: 'set' })).toBe('Checklist · 0 of 1 done · set')
    expect(checklistRowText({ items: [], op: 'clear' })).toBe('Checklist cleared')
  })

  it('says when Blerg ticked an item itself because its card was done', () => {
    const raw = { items: [{ text: 'Parser #12', done: true }], op: 'tick', changed: { index: 1, text: 'Parser #12', done: true }, note: 'ticked: #12 done' }
    const p = readChecklist(raw)!
    expect(p.note).toBe('ticked: #12 done')
    expect(checklistRowText(p)).toBe('Checklist · 1 of 1 done · ticked “Parser #12” (Blerg: #12 done)')
  })

  it('drops what is not a checklist', () => {
    expect(readChecklist({ items: 'x' })).toBeNull()
    expect(readChecklist({ items: [{ text: 1 }, { text: 'ok', done: 'yes' }], op: 'set' })).toEqual({ items: [{ text: 'ok', done: false }], op: 'set' })
  })

  it('reads the diff id of an item only when it is an artifact id and the item has commits', () => {
    const id = 'a'.repeat(32)
    const p = readChecklist({
      items: [
        { text: 'a', done: true, commits: ['abcdef1'], diff: id },
        { text: 'b', done: true, commits: ['abcdef1'], diff: 'javascript:alert(1)' },
        { text: 'c', done: true, diff: id },
      ],
      op: 'tick',
      changed: { index: 1, text: 'a', done: true, commits: ['abcdef1'], diff: id },
    })
    expect(p?.items.map(i => i.diff)).toEqual([id, undefined, undefined])
    expect(p?.changed?.diff).toBe(id)
  })

  it('reads the commits an item holds, well-formed and distinct only', () => {
    const p = readChecklist({
      items: [{ text: 'a', done: true, commits: ['ABCDEF1', 'abcdef1', 'nope', 7, '1234567890'] }, { text: 'b', done: false }],
      op: 'tick', changed: { index: 1, text: 'a', done: true, commits: ['abcdef1'] },
    })
    expect(p?.items[0].commits).toEqual(['abcdef1', '1234567890'])
    expect(p?.items[1]).toEqual({ text: 'b', done: false })
    expect(p?.changed?.commits).toEqual(['abcdef1'])
    expect(commitsText(1)).toBe('1 commit')
    expect(commitsText(3)).toBe('3 commits')
  })

  it('the newest checklist held, none once cleared', () => {
    const a = ev('checklist', { items: items(['a', false]), op: 'set' })
    const b = ev('checklist', { items: items(['a', true]), op: 'tick' })
    expect(checklistFromEvents([a, ev('user_message', {}), b])?.items[0].done).toBe(true)
    expect(checklistFromEvents([a, b, ev('checklist', { items: [], op: 'clear' })])).toBeNull()
  })
})

describe('plan label', () => {
  it('is the in-progress item and the count', () => {
    expect(planLabel({ items: [{ text: 'A', status: 'completed' }, { text: 'Write tests', status: 'in_progress', active: 'Writing tests' }, { text: 'C', status: 'pending' }], updatedAt: null }))
      .toBe('Writing tests · 1 of 3')
    expect(planLabel({ items: [{ text: 'A', status: 'pending' }], updatedAt: null })).toBe('0 of 1')
  })
})

describe('tickets', () => {
  it('links only http(s)', () => {
    expect(ticketHref('https://jira.example.test/browse/A-1')).toBe('https://jira.example.test/browse/A-1')
    for (const bad of ['javascript:alert(1)', 'mailto:a@example.test', 'data:text/html,x', '/relative', 3]) expect(ticketHref(bad)).toBeNull()
  })

  it('drops fields of the wrong type', () => {
    const t = readTicket({ source: 'jira', key: 'A-1', title: 'T', status: 5, labels: ['ui', 3], fields: [{ name: 'n', value: 'v' }, { name: 1 }], comments: [{ body: { x: 1 } }], description: '<b>x</b>' })
    expect(t).toEqual({ source: 'jira', key: 'A-1', title: 'T', labels: ['ui'], fields: [{ name: 'n', value: 'v' }], description: '<b>x</b>' })
    expect(readTicket({ title: 'no key' })).toBeNull()
  })

  it('says what a re-pin changed, from the payload alone', () => {
    const p = readTicketEvent({ action: 'update', ticket: { source: 'jira', key: 'A-1', title: 'New', status: 'Done' }, previous: { status: 'Open', title: 'Old' } })!
    expect(ticketChanges(p)).toEqual(['status Open → Done', 'new title'])
    expect(ticketChanges({ ...p, action: 'pin' })).toEqual([])
  })

  it('folds pins by (source, key), the newest current', () => {
    const pins = pinnedFromEvents([
      ev('ticket', { action: 'pin', ticket: { source: 'jira', key: 'A-1', title: 'One', status: 'Open', description: 'd' } }),
      ev('ticket', { action: 'pin', ticket: { source: 'bugzilla', key: 'A-1', title: 'Other' } }),
      ev('ticket', { action: 'update', ticket: { source: 'jira', key: 'A-1', title: 'One', status: 'Done' }, previous: { status: 'Open', title: 'One' } }),
    ])
    expect(pins.tickets.map(t => `${t.source}:${t.status}`)).toEqual(['jira:Done', 'bugzilla:'])
    expect(pins.ticket?.status).toBe('Done')
    const after = pinnedFromEvents([
      ev('ticket', { action: 'pin', ticket: { source: 'bugzilla', key: 'B', title: 'Other' } }),
      ev('ticket', { action: 'pin', ticket: { source: 'jira', key: 'A-1', title: 'One' } }),
      ev('ticket', { action: 'unpin', ticket: { source: 'jira', key: 'A-1', title: 'One' } }),
    ])
    expect(after.tickets.map(t => t.key)).toEqual(['B'])
    expect(after.ticket?.key).toBe('B')
  })

  it('keeps a Blerg Board card\'s board, so the host can show the live card', () => {
    const pins = pinnedFromEvents([ev('ticket', { action: 'pin', ticket: { source: 'blerg-board', key: '#12', board_id: 'board-a', title: 'Card' } })])
    expect(pins.tickets[0].boardId).toBe('board-a')
    expect(pins.ticket?.board_id).toBe('board-a')
    expect(readTicket({ source: 'jira', key: 'A-1', title: 'x' })?.board_id).toBeUndefined()
  })

  it('keeps #42 on two boards as two pins', () => {
    const pins = pinnedFromEvents([
      ev('ticket', { action: 'pin', ticket: { source: 'blerg-board', key: '#42', board_id: 'board-a', title: 'A' } }),
      ev('ticket', { action: 'pin', ticket: { source: 'blerg-board', key: '#42', board_id: 'board-b', title: 'B' } }),
    ])
    expect(pins.tickets.map(t => t.boardId)).toEqual(['board-b', 'board-a'])
  })

  it('names the tracker', () => {
    expect(sourceLabel('jira')).toBe('JIRA')
    expect(sourceLabel('redmine')).toBe('redmine')
  })
})

describe('statusTone', () => {
  it.each([
    ['To Do', 'neutral'], ['open', 'neutral'], ['Backlog', 'neutral'], ['NEW', 'neutral'], ['Triage', 'neutral'],
    ['In Progress', 'active'], ['in review', 'active'], ['Review', 'active'], ['started', 'active'], ['Doing', 'active'],
    ['ACTIVE', 'active'], ['Testing', 'active'], ['QA', 'active'], ['in-progress', 'active'], ['IN_PROGRESS', 'active'],
    ['Blocked', 'blocked'], ['On Hold', 'blocked'], ['waiting', 'blocked'], ['Paused', 'blocked'],
    ['Done', 'done'], ['closed', 'done'], ['RESOLVED', 'done'], ['Fixed', 'done'], ['Complete', 'done'], ['completed', 'done'],
    ['Merged', 'done'], ['released', 'done'], ['Shipped', 'done'],
    ["Won't Do", 'canceled'], ["won’t fix", 'canceled'], ['WONTFIX', 'canceled'], ['Canceled', 'canceled'], ['cancelled', 'canceled'],
    ['Duplicate', 'canceled'], ['invalid', 'canceled'], ['Rejected', 'canceled'],
    ['  In Progress  ', 'active'], ['Ready for lunch', 'neutral'], ['', 'neutral'], ['ASSIGNED', 'neutral'],
  ])('%s → %s', (status, tone) => {
    expect(statusTone(status)).toBe(tone)
  })
})

describe('priorityGlyph', () => {
  it.each([
    ['High', '↑'], ['critical', '↑'], ['Urgent', '↑'], ['Highest', '↑'], ['Medium', '→'], ['normal', '→'],
    ['Low', '↓'], ['lowest', '↓'], ['', ''], ['Sev 2', ''], ['whatever', ''],
    ['P0', '↑'], ['p1', '↑'], ['P2', '→'], ['P2 - Major', '→'], ['P3', '↓'], ['P4', '↓'], ['P5', ''], ['P10', ''], ['p1x', ''],
  ])('%s → %s', (priority, glyph) => {
    expect(priorityGlyph(priority)).toBe(glyph)
  })
})

describe('ticketMeta', () => {
  it('is type, priority and assignee, the missing ones left out', () => {
    expect(ticketMeta({ source: 'x', key: 'K', title: 'T', type: 'Task', priority: 'High', assignee: 'Release owner' }))
      .toEqual(['Task', 'High', 'Release owner'])
    expect(ticketMeta({ source: 'x', key: 'K', title: 'T', priority: 'Low' })).toEqual(['Low'])
    expect(ticketMeta({ source: 'x', key: 'K', title: 'T' })).toEqual([])
  })
})


describe('checklist work identity', () => {
  it('retains explicit identity, Doing and assignments without counting Doing as done', () => {
    const item = { id: 'item-a', text: 'Build', done: false, in_progress: true,
      assignees: [{ kind: 'subagent', id: 'native-a' }, { kind: 'child_session', id: 'child-a' }] }
    const payload = readChecklist({ items: [item], op: 'doing', changed: { ...item, index: 1 } })!
    expect(payload.items).toEqual([item])
    expect(payload.changed).toEqual({ ...item, index: 1 })
    expect(checklistRowText(payload)).toBe('Checklist · 0 of 1 done · doing “Build”')
    expect(checklistAssignment(payload.items, 'subagent', 'native-a')).toEqual(item)
    expect(checklistAssignment(payload.items, 'child_session', 'native-a')).toBeUndefined()
  })

  it('drops malformed identity and assignments and resolves inconsistent completed state safely', () => {
    const payload = readChecklist({ items: [{ text: 'Done', done: true, in_progress: true, id: '<bad>',
      assignees: [{ kind: 'subagent', id: 'a' }, { kind: 'subagent', id: 'a' },
        { kind: 'engine', id: 'b' }, { kind: 'child_session', id: 42 }] }] })!
    expect(payload.items).toEqual([{ text: 'Done', done: true, assignees: [{ kind: 'subagent', id: 'a' }] }])
    expect(checklistAssignment(payload.items, 'subagent', 'a')).toBeUndefined() // no stable target
  })

  it('never guesses assignment from title or index and follows identity after reorder', () => {
    const a = { id: 'item-a', text: 'Same title', done: false, assignees: [{ kind: 'subagent' as const, id: 'agent-a' }] }
    const b = { id: 'item-b', text: 'Same title', done: false }
    expect(checklistAssignment([b, a], 'subagent', 'agent-a')?.id).toBe('item-a')
    expect(checklistAssignment([b], 'subagent', 'agent-a')).toBeUndefined()
    expect(checklistAssignment([a, b], 'subagent', 'Same title')).toBeUndefined()
  })

  it('uses the legacy event identity only when safely representable', () => {
    const event = ev('checklist', { items: items(['Read', false]), op: 'set' })
    expect(checklistFromEvents([event])?.items[0].id).toBe(`legacy-${event.client_event_id}-1`)
    expect(checklistFromEvents([{ ...event, client_event_id: 'x'.repeat(150) }])?.items[0].id).toBeUndefined()
  })
})


describe('safe current worker associations', () => {
  const item = (id: string) => [{ id: 'work', text: 'Build', done: false, assignees: [{ kind: 'subagent' as const, id }] }]
  it('resolves only an explicit call-to-child relationship, without changing history', () => {
    const original = item('call-a')
    const events = [ev('subagent_started', { call_id: 'call-a' }), ev('subagent_update', { call_id: 'call-a', child_id: 'native-a' })]
    expect(safeChecklistAssignments(original, events)[0].assignees?.[0].id).toBe('native-a')
    expect(original[0].assignees[0].id).toBe('call-a')
    const canonical = item('native-a')
    expect(safeChecklistAssignments(canonical, events)).toBe(canonical)
  })
  it('omits ambiguous IDs instead of joining a different worker', () => {
    const events = [ev('subagent_started', { call_id: 'call-a', child_id: 'native-a' }),
      ev('subagent_started', { call_id: 'call-b', child_id: 'call-a' })]
    expect(safeChecklistAssignments(item('call-a'), events)[0].assignees).toEqual([])
    expect(safeChecklistAssignments(item('native-a'), events)[0].assignees?.[0].id).toBe('native-a')
  })
  it('does not mistake an unrelated child for a call-only worker lacking a permanent ID', () => {
    const events = [ev('subagent_started', { call_id: 'call-a' }), ev('subagent_started', { call_id: 'call-b', child_id: 'call-a' })]
    expect(safeChecklistAssignments(item('call-a'), events)[0].assignees).toEqual([])
    expect(safeChecklistAssignments(item('call-a'), [ev('subagent_started', { call_id: 'call-a', child_id: 'call-a' })])[0].assignees?.[0].id).toBe('call-a')
  })
  it('keeps authoritative identities when no loaded event contradicts them, and leaves owned sessions alone', () => {
    const original = item('native-a')
    expect(safeChecklistAssignments(original, [ev('assistant_text', { child_id: 'native-a', call_id: 'fake' })])).toBe(original)
    const owned = [{ ...original[0], assignees: [{ kind: 'child_session' as const, id: 'call-a' }] }]
    expect(safeChecklistAssignments(owned, [ev('subagent_started', { call_id: 'call-b', child_id: 'call-a' })])).toBe(owned)
  })
})
