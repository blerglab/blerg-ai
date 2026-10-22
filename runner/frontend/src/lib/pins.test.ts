import { describe, expect, it } from 'vitest'
import type { TicketSummaryLive } from '@blerglab/chat'
import { bareStatus, livePins, mentions, pinView, stateOfStatus, withWorkers, workTicket } from './pins'
import type { LiveBoardCard } from './liveBoardCard'

const pin = (key: string, updatedAt: number, o: Partial<TicketSummaryLive> = {}): TicketSummaryLive =>
  ({ source: 'blerg-board', key, title: `Card ${key}`, status: 'In progress', url: '', boardId: 'b1', eventId: `e${key}`, updatedAt, ...o })

const columns = [{ id: 'c-doing', name: 'In progress' }, { id: 'c-done', name: 'Done', is_terminal: true }]
const card = (o: Partial<LiveBoardCard>): LiveBoardCard => ({ id: 'x', board_id: 'b1', number: 1, title: 'Live title', column_id: 'c-doing', column_name: 'In progress', columns, ...o })

describe('pin state, from the board column (card #134)', () => {
  it('a terminal column or an archived card is done; a working column in progress; anything else not started', () => {
    expect(pinView(pin('#1', 1), card({ column_id: 'c-done', column_name: 'Shipped' }))).toMatchObject({ state: 'done', done: true, live: true, status: 'Shipped' })
    expect(pinView(pin('#1', 1), card({ archived_at: '2026-10-20T10:00:00Z' }))).toMatchObject({ state: 'done', status: 'Archived' })
    expect(pinView(pin('#1', 1), card({ column_id: 'c-doing', column_name: 'In progress' })).state).toBe('doing')
    expect(pinView(pin('#1', 1), card({ column_id: 'x', column_name: 'In review' })).state).toBe('doing')
    expect(pinView(pin('#1', 1), card({ column_id: 'x', column_name: 'Blocked' })).state).toBe('doing')
    expect(pinView(pin('#1', 1), card({ column_id: 'x', column_name: 'Inbox' })).state).toBe('todo')
    expect(pinView(pin('#1', 1), card({ column_id: 'x', column_name: 'Ready' })).state).toBe('todo')
  })
  it('another tracker (no column to read): from the status the agent pinned it with', () => {
    expect(stateOfStatus('Closed')).toBe('done')
    expect(stateOfStatus("Won't fix")).toBe('done')
    expect(stateOfStatus('IN-PROGRESS')).toBe('doing')
    expect(stateOfStatus('Open')).toBe('todo')
    expect(stateOfStatus('')).toBe('todo')
    expect(pinView(pin('#1', 1, { source: 'jira', status: 'In Review' }), null)).toMatchObject({ state: 'doing', live: false })
  })
  it('more names for work in flight, and names with numbering, emoji or a count', () => {
    for (const n of ['Review', 'Code review', 'In dev', 'Dev', 'Implementing', 'WIP', 'QA', 'Verify', 'Staging', '2. In progress (3)', '🚧 Doing', 'In Progress [4]', '3) Code review · 2'])
      expect(stateOfStatus(n), n).toBe('doing')
    expect(stateOfStatus('1. Done (12)')).toBe('done')
    expect(stateOfStatus('📥 Inbox (7)')).toBe('todo')
    // A name not known stays not started (a worker on the card makes it in progress).
    expect(stateOfStatus('Parking lot')).toBe('todo')
    expect(bareStatus('2. In progress (3)')).toBe('In progress')
  })
  it('a worker on a pin makes it in progress, whatever its column (done too)', () => {
    const views = [pin('#3', 3, { status: 'To do' }), pin('#2', 2, { status: 'Done' }), pin('#1', 1, { status: 'To do' })].map(p => pinView(p, null))
    const w = withWorkers(views, v => v.pin.key !== '#1')
    expect(w.map(v => v.state)).toEqual(['doing', 'doing', 'todo'])
    expect(livePins(w).map(v => v.pin.key)).toEqual(['#3', '#2'])
  })
  it('a board card waiting for its read is held out of Live until the wait is over', () => {
    const v = { ...pinView(pin('#9', 9), null), pending: true }
    expect(livePins([v], false)).toEqual([])
    expect(livePins([v], true)).toEqual([v])
  })
  it('Live keeps only the pins in progress, newest first', () => {
    const views = [pin('#4', 4, { status: 'To do' }), pin('#3', 3, { status: 'Done' }), pin('#2', 2), pin('#1', 1, { status: 'Blocked' })].map(p => pinView(p, null))
    expect(livePins(views).map(v => v.pin.key)).toEqual(['#2', '#1'])
  })
})

describe('mentions', () => {
  it('matches a key as a word only', () => {
    expect(mentions('Build History fold · #122', '#122')).toBe(true)
    expect(mentions('Build #1220', '#122')).toBe(false)
    expect(mentions('Fix WEB-12 now', 'WEB-12')).toBe(true)
    expect(mentions('Fix WEB-123', 'WEB-12')).toBe(false)
    expect(mentions('fix web-12', 'WEB-12')).toBe(true)
    expect(mentions('anything', '')).toBe(false)
    expect(mentions('see #WEB-12', 'WEB-12')).toBe(true)
    expect(mentions('see WEB-12-3', 'WEB-12')).toBe(false)
  })
})

describe('workTicket', () => {
  const pins = [pin('#122', 3), pin('#125', 2)]
  it('the child\'s own pin first (the parent\'s copy when it has one)', () => {
    expect(workTicket({ text: 'Do #125' }, pins, { source: 'blerg-board', key: '#122', board_id: 'b1', title: 'x', status: 'y' })?.eventId).toBe('e#122')
    const own = workTicket(null, pins, { source: 'jira', key: 'WEB-9', title: 'Elsewhere', status: 'Open' })
    expect(own).toMatchObject({ source: 'jira', key: 'WEB-9', title: 'Elsewhere', eventId: '' })
  })
  it('else the pin the plan item names; none when it names none', () => {
    expect(workTicket({ text: 'Build History fold · #125' }, pins)?.key).toBe('#125')
    // Two named: the first one the item names, not the newest pin.
    expect(workTicket({ text: 'Do #125 after #122' }, pins)?.key).toBe('#125')
    expect(workTicket({ text: 'Unrelated' }, pins)).toBeNull()
    expect(workTicket(null, pins)).toBeNull()
  })
})
