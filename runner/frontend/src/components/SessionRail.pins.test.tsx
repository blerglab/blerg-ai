// Card #127: Live lists every pin (newest first, a few then "+N more"), a done one marked and
// dropped at the next pin; Running now rows say which card a worker is on, from its plan item or
// its started session's own pin.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { TicketSummaryLive } from '@blerglab/chat'
import SessionRail, { type SessionRailProps } from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import type { LiveChildSession } from '../lib/liveState'
import { OpenSessionContext } from '../lib/openSession'

const pin = (key: string, title: string, updatedAt: number, status = 'In progress'): TicketSummaryLive =>
  ({ source: 'tracker', key, title, status, url: '', eventId: `e-${key}`, updatedAt })

function rail(live: ReturnType<typeof makeLive>, over: Partial<SessionRailProps> = {}) {
  const props: SessionRailProps = {
    id: 'rail', session: makeSession({ status: 'running' }), live, toolChanges: 0, mode: 'panel', open: true, onClose: () => {},
    onOpenArtifact: () => {}, ...over,
  }
  return render(<MemoryRouter><SessionRail {...props} /></MemoryRouter>)
}

beforeEach(() => localStorage.clear())

describe('Live lists every pin', () => {
  it('newest first, three then "+2 more" in place; each line opens its ticket', () => {
    const tickets = [pin('#125', 'Pins on Live', 50), pin('#123', 'Main first', 40), pin('#122', 'Build History fold', 30), pin('#110', 'Older', 20), pin('#100', 'Oldest', 10)]
    rail(makeLive({ tickets }))
    const section = screen.getByTestId('rail-pins')
    expect(within(section).getByTestId('rail-pins-count')).toHaveTextContent('5')
    expect(within(section).getAllByTestId('rail-pin').map(p => p.dataset.ticketKey)).toEqual(['#125', '#123', '#122'])
    const more = within(section).getByTestId('rail-pin-more')
    expect(more).toHaveTextContent('+2 more')
    fireEvent.click(more)
    expect(within(section).getAllByTestId('rail-pin').map(p => p.dataset.ticketKey)).toEqual(['#125', '#123', '#122', '#110', '#100'])
    expect(more).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(within(section).getAllByTestId('rail-pin-open')[2])
    expect(screen.getByRole('dialog')).toHaveTextContent('Build History fold')
  })

  it('shows without a checklist too, and not at all without a pin', () => {
    rail(makeLive({ tickets: [pin('#1', 'Only', 1)] }))
    expect(screen.getByTestId('rail-pins')).toHaveTextContent('Ticket')
  })

  it('Live lists only the cards in progress: a done one leaves it as it closes, one not started is not there', () => {
    const tickets = [pin('#125', 'Next card', 60, 'To do'), pin('#122', 'Shipped', 50, 'Done'), pin('#121', 'Working', 40, 'In review'), pin('#120', 'Earlier', 10, 'Blocked')]
    rail(makeLive({ tickets }))
    const lines = screen.getAllByTestId('rail-pin')
    expect(lines.map(p => p.dataset.ticketKey)).toEqual(['#121', '#120'])
    for (const l of lines) expect(within(l).getByTestId('rail-pin-state')).toHaveTextContent('◐')
    expect(within(screen.getByTestId('rail-pins')).getByTestId('rail-pins-count')).toHaveTextContent('2')
  })

  it('a card a worker is on is in progress on Live, whatever its column: assigned through the checklist, or pinned by the child', () => {
    const tickets = [pin('#125', 'Not moved yet', 60, 'To do'), pin('#124', 'Closed but worked', 50, 'Done'), pin('#123', 'Idle child', 40, 'To do')]
    const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Do #125', done: false, assignees: [{ kind: 'subagent' as const, id: 'S1' }] }] }
    const own = { source: 'tracker', key: '#124', title: 'Closed but worked', status: 'Done' }
    rail(makeLive({ tickets, checklist, subagents: [{ key: 'S1', title: 'Worker', type: '', model: '', tokens: null, doing: '', startedAt: now, background: false }] }),
      { childSessions: [child({ id: 'c-run', ticket: own }), child({ id: 'c-idle', status: 'idle', ticket: { ...own, key: '#123', title: 'Idle child', status: 'To do' } })] })
    const lines = within(screen.getByTestId('rail-pins')).getAllByTestId('rail-pin')
    expect(lines.map(l => [l.dataset.ticketKey, within(l).getByTestId('rail-pin-state').textContent])).toEqual([['#125', '◐'], ['#124', '◐']])
  })

  it('Plan lists every pin, one glyph each, no status chips, the done count in its header', () => {
    const tickets = [pin('#125', 'Next card', 60, 'To do'), pin('#122', 'Shipped', 50, 'Done'), pin('#121', 'Working', 40, 'In progress'), pin('#120', 'Dropped', 10, "Won't do")]
    rail(makeLive({ tickets, checklist: { eventId: 'c1', updatedAt: null, items: [{ id: 'a', text: 'Step', done: false }] } }))
    fireEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    const section = screen.getByTestId('rail-ticket')
    expect(within(section).getByTestId('rail-ticket-done')).toHaveTextContent('2 of 4 done')
    const rows = within(section).getAllByTestId('rail-ticket-row')
    expect(rows.map(r => [r.dataset.ticketKey, within(r).getByTestId('rail-pin-state').textContent])).toEqual([['#125', '○'], ['#122', '✓'], ['#121', '◐'], ['#120', '✓']])
    expect(section.querySelector('[data-testid="ticket-status"], .board-live-mark')).toBeNull()
    expect(within(rows[0]).getByTestId('rail-ticket-open')).toHaveAccessibleName('#125 Next card, Not started (To do)')
    // The checklist sits right under the tickets.
    expect(section.nextElementSibling).toBe(screen.getByTestId('rail-checklist'))
  })

})

const now = Date.now()
const child = (o: Partial<LiveChildSession>): LiveChildSession => ({
  id: 'child-1', title: 'Port the parser', engine: 'codex', model: '', status: 'running', started_at: new Date(now - 60_000).toISOString(),
  asking: false, tokens: null, ...o,
})

describe('Running now says which card a worker is on', () => {
  it('a subagent on a plan item that names a pin: "↳ <item> · #122", the item to Plan, the key to the ticket', () => {
    const tickets = [pin('#125', 'Pins on Live', 50), pin('#122', 'Build History fold', 30)]
    const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Build History fold · #122', done: false, in_progress: true, assignees: [{ kind: 'subagent' as const, id: 'S1' }] }] }
    rail(makeLive({ tickets, checklist, subagents: [{ key: 'S1', title: 'History builder', type: 'general', model: '', tokens: null, doing: 'Edit', startedAt: now - 1000, background: false }] }))
    const row = screen.getByTestId('rail-subagent')
    const work = within(row).getByTestId('rail-work')
    expect(work).toHaveTextContent('↳Build History fold · #122·#122')
    // The card is said once: no ☰ glyph beside it.
    expect(within(row).queryByTestId('rail-checklist-reference')).toBeNull()
    fireEvent.click(within(work).getByTestId('rail-work-ticket'))
    expect(screen.getByRole('dialog')).toHaveTextContent('Build History fold')
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    fireEvent.click(within(work).getByTestId('rail-work-item'))
    expect(screen.getByRole('tab', { name: /Plan/ })).toHaveAttribute('aria-selected', 'true')
    // Live's line for #122 says a worker is on it.
    fireEvent.click(screen.getByRole('tab', { name: /Live/ }))
    const lines = screen.getAllByTestId('rail-pin')
    expect(within(lines[1]).getByTestId('rail-pin-worker')).toHaveAccessibleName('Worker: History builder')
    expect(within(lines[0]).queryByTestId('rail-pin-worker')).toBeNull()
  })

  it('a plan item that names no pin keeps its ☰ glyph and has no work line', () => {
    const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Tidy up', done: false, assignees: [{ kind: 'subagent' as const, id: 'S1' }] }] }
    rail(makeLive({ tickets: [pin('#122', 'x', 1)], checklist, subagents: [{ key: 'S1', title: 'Tidier', type: '', model: '', tokens: null, doing: '', startedAt: now, background: false }] }))
    const row = screen.getByTestId('rail-subagent')
    expect(within(row).getByTestId('rail-checklist-reference')).toBeInTheDocument()
    expect(within(row).queryByTestId('rail-work')).toBeNull()
  })

  it('a started session that pinned its own card shows it on its row, in Running now and in Started sessions', () => {
    const own = { source: 'tracker', key: 'WEB-9', title: 'Parser port', status: 'In progress' }
    const opened = vi.fn()
    const props: SessionRailProps = { id: 'rail', session: makeSession({ status: 'running' }), live: makeLive({ tickets: [pin('#122', 'Coordinate', 1)] }), toolChanges: 0, mode: 'panel', open: true, onClose: () => {}, onOpenArtifact: () => {}, childSessions: [child({ ticket: own })] }
    const { unmount } = render(<MemoryRouter><OpenSessionContext.Provider value={opened}><SessionRail {...props} /></OpenSessionContext.Provider></MemoryRouter>)
    const row = screen.getByTestId('rail-child')
    expect(within(row).getByTestId('rail-work')).toHaveTextContent('↳Parser port·WEB-9')
    // A pin only the child made: its key opens the child's session (where the pin is), not a ticket
    // dialog whose "Show in conversation" would find nothing here.
    fireEvent.click(within(row).getByTestId('rail-work-ticket'))
    expect(opened).toHaveBeenCalledWith('child-1')
    expect(screen.queryByRole('dialog')).toBeNull()
    unmount()
    rail(makeLive({}), { childSessions: [child({ status: 'idle', ticket: own })] })
    const started = screen.getByTestId('rail-started')
    expect(within(started).getByTestId('rail-work')).toHaveTextContent('WEB-9')
  })
})

describe('two workers on one card', () => {
  it('the pin line names both', () => {
    const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Part one · #122', done: false, assignees: [{ kind: 'subagent' as const, id: 'S1' }] }, { id: 'i2', text: 'Part two · #122', done: false, assignees: [{ kind: 'subagent' as const, id: 'S2' }] }] }
    const sub = (key: string, title: string) => ({ key, title, type: '', model: '', tokens: null, doing: '', startedAt: now, background: false })
    rail(makeLive({ tickets: [pin('#122', 'Build', 1)], checklist, subagents: [sub('S1', 'Alpha'), sub('S2', 'Beta')] }))
    expect(within(screen.getByTestId('rail-pins')).getByTestId('rail-pin-worker')).toHaveAccessibleName('Worker: Alpha, Beta')
  })
})
