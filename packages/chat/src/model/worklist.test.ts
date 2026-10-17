import { describe, expect, it } from 'vitest'
import type { AgentEvent } from '../types'
import {
  checklistFromEvents, checklistRowText, commitsText, pinnedFromEvents, planLabel, priorityGlyph, readChecklist, readTicket,
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
